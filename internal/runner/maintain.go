package runner

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/delvivo/dumpling/internal/artifact"
	"github.com/delvivo/dumpling/internal/schedule"
	"github.com/delvivo/dumpling/internal/storage"
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
		sum, err := r.checksum(dest, b, o.ID, l)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			r.St.SetBackupStatus(b.ID, "corrupted")
			r.finish(o, l, "failed", "artifact missing: backup marked corrupted")
		case err != nil: // network/auth/IO: the artifact may be fine, keep its status (FR-010)
			r.finish(o, l, "failed", "destination unreachable: "+err.Error())
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

// checksum fetches a backup's artifact (temp download for remote destinations) and hashes it.
func (r *Runner) checksum(dest store.Destination, b store.Backup, opID string, l *OpLog) (string, error) {
	dst, err := r.openDest(dest, l)
	if err != nil {
		return "", err
	}
	defer dst.Close()
	path, cleanup, err := r.fetch(dst, dest, b.Path, opID, l)
	defer cleanup()
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return artifact.Checksum(f)
}

// deleteFiles removes an artifact and its sidecar and marks the backup deleted.
func (r *Runner) deleteFiles(dst storage.Backend, b store.Backup) error {
	if err := dst.Remove(b.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	dst.Remove(b.Path + ".json")
	return r.St.SetBackupStatus(b.ID, "deleted")
}

// deleteBackup opens the backup's destination and deletes it.
func (r *Runner) deleteBackup(b store.Backup) error {
	dest, err := r.St.Destination(b.DestinationID)
	if err != nil {
		return err
	}
	dst, err := r.openDest(dest, nil)
	if err != nil {
		return err
	}
	defer dst.Close()
	return r.deleteFiles(dst, b)
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
	return r.deleteBackup(b)
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
	// backups of a plan may live on an older destination if the plan was edited: one backend each
	backends := map[string]storage.Backend{}
	defer func() {
		for _, b := range backends {
			b.Close()
		}
	}()
	var failed []string
	for _, b := range drop {
		dst, ok := backends[b.DestinationID]
		if !ok {
			dest, err := r.St.Destination(b.DestinationID)
			if err == nil {
				dst, err = r.openDest(dest, l)
			}
			if err != nil {
				failed = append(failed, b.Path)
				l.Printf("delete %s (%s): destination unreachable: %v", b.Path, b.ID, err)
				continue
			}
			backends[b.DestinationID] = dst
		}
		if err := r.deleteFiles(dst, b); err != nil {
			failed = append(failed, b.Path)
			l.Printf("delete %s (%s): %v", b.Path, b.ID, err)
		} else {
			l.Printf("deleted %s (%s)", b.Path, b.ID)
		}
	}
	if len(failed) > 0 { // FR-011: kept in history, reported as warning
		r.finish(o, l, "warning", "not deleted: "+strings.Join(failed, ", "))
	} else {
		r.finish(o, l, "success", "")
	}
}
