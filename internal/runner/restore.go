package runner

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/delvivo/dumpling/internal/artifact"
	"github.com/delvivo/dumpling/internal/engine"
	"github.com/delvivo/dumpling/internal/storage"
	"github.com/delvivo/dumpling/internal/store"
)

type RestoreReq struct {
	BackupID           string `json:"backup_id"`
	TargetConnectionID string `json:"target_connection_id"`
	TargetDatabase     string `json:"target_database"`
	ConfirmDatabase    string `json:"confirm_database"`
	Passphrase         string `json:"passphrase"`
	SkipOwnership      bool   `json:"skip_ownership"`
	User               string `json:"-"`
}

type Preflight struct {
	TargetExists       bool   `json:"target_exists"`
	VersionWarning     string `json:"version_warning,omitempty"`
	PassphraseRequired bool   `json:"passphrase_required"`
}

type restorePlan struct {
	pf         Preflight
	backup     store.Backup
	dest       store.Destination
	target     store.Connection
	eng        engine.Engine
	passphrase string
}

// newEngine is replaceable in tests.
var newEngine = engine.For

func (r *Runner) plan(req RestoreReq) (*restorePlan, error) {
	b, err := r.St.Backup(req.BackupID)
	if err != nil {
		return nil, errf(http.StatusNotFound, "not_found", "backup not found")
	}
	if b.Status != "ok" {
		return nil, errf(http.StatusBadRequest, "backup_not_ok", "backup is %s and cannot be restored", b.Status)
	}
	t, err := r.St.Connection(req.TargetConnectionID)
	if err != nil {
		return nil, errf(http.StatusNotFound, "not_found", "target connection not found")
	}
	if t.Engine != b.Engine {
		return nil, errf(http.StatusBadRequest, "engine_mismatch", "a %s backup cannot be restored into a %s server", b.Engine, t.Engine)
	}
	if req.TargetDatabase == "" {
		return nil, errf(http.StatusBadRequest, "invalid", "target database name required")
	}
	dest, err := r.St.Destination(b.DestinationID)
	if err != nil {
		return nil, errf(http.StatusInternalServerError, "not_found", "backup destination missing")
	}
	eng, err := newEngine(t.Engine)
	if err != nil {
		return nil, err
	}
	p := &restorePlan{backup: b, dest: dest, target: t, eng: eng}
	if exists, err := eng.Exists(r.ctx, r.conn(t, store.NewID()), req.TargetDatabase); err != nil {
		return nil, errf(http.StatusBadGateway, "target_unreachable", "cannot query target: %v", err)
	} else {
		p.pf.TargetExists = exists
	}
	if t.ServerVersion != "" && b.ServerVersion != "" && engine.Major(t.ServerVersion) < engine.Major(b.ServerVersion) {
		p.pf.VersionWarning = fmt.Sprintf("target server %s is older than the backup source %s; the restore may fail", t.ServerVersion, b.ServerVersion)
	}
	if b.Encrypted { // passphrase: request → stored by unlock → plan
		p.passphrase = req.Passphrase
		if p.passphrase == "" {
			p.passphrase = b.PassphrasePlain()
		}
		if p.passphrase == "" && b.PlanID != "" {
			if pl, err := r.St.Plan(b.PlanID); err == nil {
				p.passphrase = pl.PassphrasePlain()
			}
		}
		p.pf.PassphraseRequired = p.passphrase == ""
	}
	return p, nil
}

func (r *Runner) Preflight(req RestoreReq) (Preflight, error) {
	p, err := r.plan(req)
	if err != nil {
		return Preflight{}, err
	}
	return p.pf, nil
}

// StartRestore validates the request (FR-016–FR-021) and runs the restore in the background:
// checksum pass first, then drop/recreate (if confirmed) and load, so a corrupted artifact never
// touches the target.
func (r *Runner) StartRestore(req RestoreReq) (string, error) {
	p, err := r.plan(req)
	if err != nil {
		return "", err
	}
	if p.pf.TargetExists && req.ConfirmDatabase != req.TargetDatabase {
		e := errf(http.StatusConflict, "confirmation_required",
			"database %q exists: active connections will be terminated and it will be dropped and recreated; type its name to confirm", req.TargetDatabase)
		e.Extra = map[string]any{"target_exists": true, "warnings": nonEmpty(p.pf.VersionWarning)}
		return "", e
	}
	if p.pf.PassphraseRequired && p.backup.Locked {
		return "", errf(http.StatusBadRequest, "backup_locked", "imported encrypted backup: unlock it or enter its passphrase")
	}
	if p.pf.PassphraseRequired {
		return "", errf(http.StatusBadRequest, "passphrase_required", "backup is encrypted: passphrase required")
	}
	key := "restore:" + p.target.ID + "/" + req.TargetDatabase
	if !r.tryLock(key) {
		return "", errf(http.StatusConflict, "restore_in_progress", "a restore into this database is already running")
	}
	o := &store.Operation{Type: "restore", ConnectionID: p.backup.ConnectionID, BackupID: p.backup.ID,
		TargetConnectionID: p.target.ID, TargetDatabase: req.TargetDatabase, Database: p.backup.Database}
	if err := r.newOp(o); err != nil {
		r.unlock(key)
		return "", err
	}
	r.goRun(func() {
		defer r.unlock(key)
		r.restore(o, p, req)
	})
	return o.ID, nil
}

func nonEmpty(s ...string) []string {
	out := []string{}
	for _, v := range s {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (r *Runner) restore(o *store.Operation, p *restorePlan, req RestoreReq) {
	l, err := r.start(o)
	if err != nil {
		return
	}
	l.AddSecret(p.target.Password)
	l.AddSecret(p.passphrase)
	if missing := engine.MissingTools(p.target.Engine); len(missing) > 0 {
		r.finish(o, l, "failed", "missing client tools: "+fmt.Sprint(missing))
		return
	}
	dst, err := r.openDest(p.dest, l)
	if err != nil {
		r.finish(o, l, "failed", "destination unreachable: "+err.Error())
		return
	}
	defer dst.Close()
	path, cleanup, err := r.fetch(dst, p.dest, p.backup.Path, o.ID, l)
	defer cleanup()
	if err != nil {
		r.finish(o, l, "failed", "read artifact: "+err.Error())
		return
	}

	l.Printf("verifying checksum of %s", storage.Location(p.dest, p.backup.Path))
	f, err := os.Open(path)
	if err != nil {
		r.finish(o, l, "failed", "read artifact: "+err.Error())
		return
	}
	defer f.Close()
	sum, err := artifact.Checksum(f)
	if err != nil {
		r.finish(o, l, "failed", "read artifact: "+err.Error())
		return
	}
	if sum != p.backup.SHA256 {
		r.St.SetBackupStatus(p.backup.ID, "corrupted")
		r.finish(o, l, "failed", "checksum mismatch: backup marked corrupted, target left untouched")
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		r.finish(o, l, "failed", "read artifact: "+err.Error())
		return
	}
	src, err := artifact.Open(f, engine.Gzip(p.backup.Engine), p.passphrase)
	if err != nil {
		if errors.Is(err, artifact.ErrPassphrase) {
			err = artifact.ErrPassphrase
		}
		r.finish(o, l, "failed", "open artifact: "+err.Error())
		return
	}
	defer src.Close()
	if p.pf.TargetExists {
		l.Printf("overwrite of %s confirmed by %s at %s", req.TargetDatabase, req.User, store.Now())
	}
	if p.pf.VersionWarning != "" {
		l.Printf("warning: %s", p.pf.VersionWarning)
	}
	err = p.eng.Restore(r.ctx, r.conn(p.target, o.ID), req.TargetDatabase, p.pf.TargetExists, src,
		engine.RestoreOpts{SkipOwnership: req.SkipOwnership}, l)
	if err != nil {
		r.finish(o, l, "failed", err.Error())
		return
	}
	r.finish(o, l, "success", fmt.Sprintf("restored into %s/%s", p.target.Name, req.TargetDatabase))
}
