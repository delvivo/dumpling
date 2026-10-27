package runner

import (
	"net/http"
	"os"

	"github.com/delvivo/dumpling/internal/artifact"
	"github.com/delvivo/dumpling/internal/schedule"
	"github.com/delvivo/dumpling/internal/store"
)

// StartVerify re-computes an artifact's checksum; mismatch marks the backup corrupted.
func (r *Runner) StartVerify(backupID string) (string, error) {
	b, err := r.St.Backup(backupID)
	if err != nil || b.Status == "deleted" {
		return "", errf(http.StatusNotFound, "not_found", "backup not found")
	}
	dest, err := r.St.Destination(b.DestinationID)
	if err != nil {
		return "", err
	}
	o := &store.Operation{Type: "verify", PlanID: b.PlanID, ConnectionID: b.ConnectionID, BackupID: b.ID, Database: b.Database}
	if err := r.newOp(o); err != nil {
		return "", err
	}
	r.goRun(func() {
		l, err := r.start(o)
		if err != nil {
			return
		}
		sum, err := artifact.Checksum(artifactPath(dest, b.Path))
		switch {
		case err != nil:
			r.St.SetBackupStatus(b.ID, "corrupted")
			r.finish(o, l, "failed", "artifact unreadable: "+err.Error())
		case sum != b.SHA256:
			r.St.SetBackupStatus(b.ID, "corrupted")
			r.finish(o, l, "failed", "checksum mismatch: backup marked corrupted")
		default:
			if b.Status == "corrupted" { // file was repaired/restored from elsewhere
				r.St.SetBackupStatus(b.ID, "ok")
			}
			r.finish(o, l, "success", "checksum ok "+sum)
		}
	})
	return o.ID, nil
}

// deleteFiles removes an artifact and its sidecar and marks the backup deleted.
func (r *Runner) deleteFiles(b store.Backup) error {
	dest, err := r.St.Destination(b.DestinationID)
	if err != nil {
		return err
	}
	p := artifactPath(dest, b.Path)
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	os.Remove(p + ".json")
	return r.St.SetBackupStatus(b.ID, "deleted")
}

// DeleteBackup deletes a backup after the caller confirmed by typing its database name.
func (r *Runner) DeleteBackup(id, confirm string) error {
	b, err := r.St.Backup(id)
	if err != nil || b.Status == "deleted" {
		return errf(http.StatusNotFound, "not_found", "backup not found")
	}
	if confirm != b.Database {
		return errf(http.StatusConflict, "confirmation_required", "type the database name %q to confirm deletion", b.Database)
	}
	return r.deleteFiles(b)
}

// prune applies plan retention after a successful backup (runs as its own operation).
func (r *Runner) prune(p store.Plan, newest store.Backup) {
	all, err := r.St.Backups(store.BackupFilter{PlanID: p.ID, Status: "ok"})
	if err != nil {
		return
	}
	keep := schedule.Keep(all, p.Retention)
	var drop []store.Backup
	for _, b := range all {
		if !keep[b.ID] && b.ID != newest.ID {
			drop = append(drop, b)
		}
	}
	if len(drop) == 0 {
		return
	}
	o := &store.Operation{Type: "prune", PlanID: p.ID, ConnectionID: p.ConnectionID}
	if r.newOp(o) != nil {
		return
	}
	l, err := r.start(o)
	if err != nil {
		return
	}
	failed := 0
	for _, b := range drop {
		if err := r.deleteFiles(b); err != nil {
			failed++
			l.Printf("delete %s (%s): %v", b.Path, b.ID, err)
		} else {
			l.Printf("deleted %s (%s)", b.Path, b.ID)
		}
	}
	if failed > 0 {
		r.finish(o, l, "failed", "some backups could not be deleted")
	} else {
		r.finish(o, l, "success", "")
	}
}
