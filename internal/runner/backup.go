package runner

import (
	"fmt"
	"strings"
	"time"

	"github.com/delvivo/dumpling/internal/artifact"
	"github.com/delvivo/dumpling/internal/engine"
	"github.com/delvivo/dumpling/internal/store"
)

type BackupReq struct {
	Conn       store.Connection
	Databases  []string
	Dest       store.Destination
	Plan       *store.Plan // nil for on-demand
	Passphrase string
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
	started := time.Now().UTC()
	planName := "adhoc"
	if req.Plan != nil {
		planName = req.Plan.Name
	}
	ext := engine.Ext(c.Engine)
	if req.Passphrase != "" {
		ext += ".age"
	}
	rel := fmt.Sprintf("%s/%s/%s/%s-%s.%s", safeName(planName), safeName(c.Name), safeName(db),
		started.Format("20060102T150405Z"), strings.ToLower(o.ID[len(o.ID)-6:]), ext)
	final := artifactPath(req.Dest, rel)
	l.Printf("backup %s/%s → %s", c.Name, db, final)

	w, err := artifact.Create(final, engine.Gzip(c.Engine), req.Passphrase)
	if err != nil {
		r.finish(o, l, "failed", err.Error())
		return store.Backup{}, false
	}
	warnings, err := eng.Backup(r.ctx, r.conn(c, o.ID), db, w, l)
	if err != nil {
		w.Abort()
		r.finish(o, l, "failed", err.Error())
		return store.Backup{}, false
	}
	b := store.Backup{OperationID: o.ID, ConnectionID: c.ID, ConnectionName: c.Name, Engine: c.Engine,
		ServerVersion: c.ServerVersion, Database: db, DestinationID: req.Dest.ID, Path: rel,
		Encrypted: req.Passphrase != "", Status: "ok", StartedAt: started.Format(time.RFC3339)}
	if req.Plan != nil {
		b.PlanID, b.PlanName = req.Plan.ID, req.Plan.Name
	}
	size, sum, err := w.Commit(map[string]any{
		"engine": b.Engine, "server_version": b.ServerVersion, "database": db, "connection": c.Name,
		"plan": b.PlanName, "encrypted": b.Encrypted, "gzip": engine.Gzip(c.Engine), "started_at": b.StartedAt,
		"finished_at": store.Now(), "operation_id": o.ID,
	})
	if err != nil && size == 0 {
		r.finish(o, l, "failed", err.Error())
		return store.Backup{}, false
	}
	b.SizeBytes, b.SHA256, b.FinishedAt = size, sum, store.Now()
	if err := r.St.InsertBackup(&b); err != nil {
		r.finish(o, l, "failed", "record backup: "+err.Error())
		return store.Backup{}, false
	}
	o.BackupID = b.ID
	l.Printf("stored %d bytes, sha256 %s", size, sum)
	if err != nil { // sidecar failed but artifact is valid
		warnings = append(warnings, err.Error())
	}
	if len(warnings) > 0 {
		r.finish(o, l, "warning", strings.Join(warnings, "; "))
	} else {
		r.finish(o, l, "success", "")
	}
	return b, true
}

// RunPlan starts a backup run for every database of a plan (scheduled or "run now").
func (r *Runner) RunPlan(p store.Plan) ([]string, error) {
	c, err := r.St.Connection(p.ConnectionID)
	if err != nil {
		return nil, err
	}
	d, err := r.St.Destination(p.DestinationID)
	if err != nil {
		return nil, err
	}
	return r.StartBackup(BackupReq{Conn: c, Databases: p.Databases, Dest: d, Plan: &p, Passphrase: p.PassphrasePlain()})
}
