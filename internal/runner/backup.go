package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/delvivo/dumpling/internal/artifact"
	"github.com/delvivo/dumpling/internal/engine"
	"github.com/delvivo/dumpling/internal/storage"
	"github.com/delvivo/dumpling/internal/store"
)

type BackupReq struct {
	Conn       store.Connection
	Databases  []string
	Dest       store.Destination
	Plan       *store.Plan // nil for on-demand
	Passphrase string
	Kind       string // "full" (default) or "incremental"; only plans in incremental mode honor it
}

// StartBackup creates one pending operation per database and runs them sequentially in the
// background. Plan runs hold the plan lock; an overlapping plan run is recorded as skipped (FR-015).
func (r *Runner) StartBackup(req BackupReq) ([]string, error) {
	planID := ""
	if req.Plan != nil {
		planID = req.Plan.ID
	}
	ops := make([]*store.Operation, len(req.Databases))
	ids := make([]string, len(req.Databases))
	for i, db := range req.Databases {
		ops[i] = &store.Operation{Type: "backup", PlanID: planID, ConnectionID: req.Conn.ID, Database: db}
		if err := r.newOp(ops[i]); err != nil {
			return nil, err
		}
		ids[i] = ops[i].ID
	}
	lockKey := "plan:" + planID
	if planID != "" && !r.tryLock(lockKey) {
		for _, o := range ops {
			r.finish(o, nil, "skipped", "previous run of this plan still in progress")
		}
		return ids, nil
	}
	r.goRun(func() {
		if planID != "" {
			defer r.unlock(lockKey)
		}
		for _, o := range ops {
			if b, ok := r.backup(o, req, o.Database); ok && req.Plan != nil {
				r.prune(*req.Plan, b)
			}
		}
	})
	return ids, nil
}

// attempt is one artifact write: a standalone dump, or a chain full/incremental.
type attempt struct {
	kind     string // "full" or "incr"
	chain    bool   // part of a chain (incremental-mode plan)
	physical bool   // postgres base backup
	prev     engine.Link
}

func (r *Runner) backup(o *store.Operation, req BackupReq, db string) (store.Backup, bool) {
	l, err := r.start(o)
	if err != nil {
		return store.Backup{}, false
	}
	c := req.Conn
	l.AddSecret(c.Password)
	l.AddSecret(req.Passphrase)

	eng, err := newEngine(c.Engine)
	if err != nil {
		r.finish(o, l, "failed", err.Error())
		return store.Backup{}, false
	}
	if missing := engine.MissingTools(c.Engine); len(missing) > 0 {
		r.finish(o, l, "failed", "missing client tools: "+strings.Join(missing, ", "))
		return store.Backup{}, false
	}
	if c.ServerVersion == "" { // never tested: learn the version, it is part of the backup metadata
		if t, err := eng.Test(r.ctx, r.conn(c, o.ID)); err == nil {
			c.ServerVersion = t.Version
			r.St.SetConnectionVersion(c.ID, t.Version)
		}
	}

	var warnings []string
	at := attempt{kind: "full"}
	var d decision
	inc, chainable := eng.(engine.Incremental)
	if req.Plan != nil && req.Plan.Incremental() && chainable {
		missing, warn, err := inc.Prereqs(r.ctx, r.conn(c, o.ID), db)
		if err != nil {
			missing = append(missing, "prerequisite check failed: "+err.Error())
		}
		warnings = append(warnings, warn...)
		if len(missing) > 0 {
			// FR-006: protect with a full; a cluster plan has no logical fallback, so it tries a base backup
			msg := "prerequisite missing: " + strings.Join(missing, "; ")
			l.Printf("%s → full backup", msg)
			warnings = append(warnings, msg)
			at.chain = db == store.ClusterDB
		} else {
			at.chain = true
			if d, err = r.decideKind(*req.Plan, db, req.Kind); err != nil {
				r.finish(o, l, "failed", err.Error())
				return store.Backup{}, false
			}
			if d.Reason != "" {
				l.Printf("incremental requested, taking a full: %s", d.Reason)
			}
			at.kind = d.Kind
		}
		at.physical = at.chain && c.Engine == "postgres"
	}

	dst, err := r.openDest(req.Dest, l)
	if err != nil {
		r.finish(o, l, "failed", "destination unreachable: "+err.Error())
		return store.Backup{}, false
	}
	defer dst.Close()

	if at.kind == "incr" {
		at.prev = engine.Link{Marker: d.Head.Marker}
		if at.physical {
			if at.prev.State, err = r.manifest(dst, d.Head, l); err != nil {
				l.Printf("previous manifest unavailable (%v) → full backup", err)
				at.kind = "full"
			}
		}
	}

	b, warn, err := r.write(o, l, req, c, eng, dst, db, at, d)
	if errors.Is(err, engine.ErrChainBroken) && at.kind == "incr" {
		l.Printf("cannot continue the chain (%v) → full backup", err)
		at.kind = "full"
		b, warn, err = r.write(o, l, req, c, eng, dst, db, at, d)
	}
	if errors.Is(err, engine.ErrLockUnsafe) {
		r.finish(o, l, "warning", "long-running statement; full postponed: "+err.Error())
		return store.Backup{}, false
	}
	if err != nil {
		r.finish(o, l, "failed", err.Error())
		return store.Backup{}, false
	}
	warnings = append(warnings, warn...)
	if len(warnings) > 0 {
		r.finish(o, l, "warning", strings.Join(warnings, "; "))
	} else {
		r.finish(o, l, "success", "")
	}
	return b, true
}

// write produces one artifact (+ sidecar, + postgres manifest) and records the backup row.
func (r *Runner) write(o *store.Operation, l *OpLog, req BackupReq, c store.Connection, eng engine.Engine,
	dst storage.Backend, db string, at attempt, d decision) (store.Backup, []string, error) {
	started := time.Now().UTC()
	planName := "adhoc"
	if req.Plan != nil {
		planName = req.Plan.Name
	}
	ext := engine.Ext(c.Engine, at.kind, at.physical)
	if req.Passphrase != "" {
		ext += ".age"
	}
	suffix := ""
	if at.kind == "incr" {
		suffix = "-incr"
	}
	rel := fmt.Sprintf("%s/%s/%s/%s-%s%s.%s", safeName(planName), safeName(c.Name), safeName(db),
		started.Format("20060102T150405Z"), strings.ToLower(o.ID[len(o.ID)-6:]), suffix, ext)
	l.Printf("%s backup %s/%s → %s", map[string]string{"full": "full", "incr": "incremental"}[at.kind], c.Name, db, storage.Location(req.Dest, rel))

	up, err := dst.Create(rel)
	if err != nil {
		return store.Backup{}, nil, err
	}
	gz := engine.Gzip(c.Engine, at.physical)
	w, err := artifact.Create(up, gz, req.Passphrase)
	if err != nil {
		return store.Backup{}, nil, err
	}
	var state *os.File
	if at.physical {
		if state, err = os.CreateTemp(filepath.Join(r.DataDir, "tmp"), o.ID+"-*.state"); err != nil {
			w.Abort()
			return store.Backup{}, nil, err
		}
		defer func() { state.Close(); os.Remove(state.Name()) }()
	}
	var stateW io.Writer
	if state != nil {
		stateW = state
	}

	var warnings []string
	var marker string
	inc, _ := eng.(engine.Incremental) // set whenever at.chain (checked by backup)
	switch {
	case !at.chain:
		warnings, err = eng.Backup(r.ctx, r.conn(c, o.ID), db, w, l)
	case at.kind == "incr":
		marker, warnings, err = inc.BackupIncr(r.ctx, r.conn(c, o.ID), db, at.prev, w, stateW, l)
	default:
		marker, warnings, err = inc.BackupFull(r.ctx, r.conn(c, o.ID), db, w, stateW, l)
	}
	if err != nil {
		w.Abort()
		return store.Backup{}, nil, err
	}

	b := store.Backup{ID: store.NewID(), OperationID: o.ID, ConnectionID: c.ID, ConnectionName: c.Name, Engine: c.Engine,
		ServerVersion: c.ServerVersion, Database: db, DestinationID: req.Dest.ID, Path: rel,
		Encrypted: req.Passphrase != "", Status: "ok", StartedAt: started.Format(time.RFC3339), Kind: at.kind, Marker: marker}
	if req.Plan != nil {
		b.PlanID, b.PlanName = req.Plan.ID, req.Plan.Name
	}
	if at.chain {
		b.ChainID = b.ID
	}
	if at.kind == "incr" {
		b.ChainID, b.Seq = d.Head.ChainID, d.Head.Seq+1
		b.ParentID = d.Head.ID
		if c.Engine == "mssql" { // differentials are cumulative: each one applies on the full
			b.ParentID = d.Full.ID
		}
	}
	size, sum, err := w.Commit(dst, rel, map[string]any{
		"engine": b.Engine, "server_version": b.ServerVersion, "database": db, "connection": c.Name,
		"plan": b.PlanName, "plan_id": b.PlanID, "encrypted": b.Encrypted, "gzip": gz, "started_at": b.StartedAt,
		"finished_at": store.Now(), "operation_id": o.ID, "backup_id": b.ID,
		"kind": b.Kind, "chain_id": b.ChainID, "parent_id": b.ParentID, "seq": b.Seq, "marker": b.Marker,
	})
	if err != nil && size == 0 {
		return store.Backup{}, nil, err
	}
	if err != nil { // sidecar failed but artifact is valid
		warnings = append(warnings, err.Error())
	}
	if state != nil {
		if err := r.storeManifest(dst, b, rel, state, req.Passphrase); err != nil {
			// without its manifest the link cannot be continued; the next run starts a new chain
			warnings = append(warnings, "manifest not stored: "+err.Error())
		}
	}
	b.SizeBytes, b.SHA256, b.FinishedAt = size, sum, store.Now()
	if err := r.St.InsertBackup(&b); err != nil {
		return store.Backup{}, nil, fmt.Errorf("record backup: %w", err)
	}
	o.BackupID = b.ID
	l.Printf("stored %d bytes, sha256 %s", size, sum)
	return b, warnings, nil
}

// RunPlan starts a backup run for every database of a plan (scheduled or "run now").
func (r *Runner) RunPlan(p store.Plan, kind string) ([]string, error) {
	c, err := r.St.Connection(p.ConnectionID)
	if err != nil {
		return nil, err
	}
	d, err := r.St.Destination(p.DestinationID)
	if err != nil {
		return nil, err
	}
	return r.StartBackup(BackupReq{Conn: c, Databases: p.Databases, Dest: d, Plan: &p, Passphrase: p.PassphrasePlain(), Kind: kind})
}
