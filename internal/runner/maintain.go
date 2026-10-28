package runner

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"sort"
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
		// a chain link is only as good as the links it depends on: verify all of them (FR-012)
		links, missing := []store.Backup{b}, ""
		for id := b.ParentID; id != ""; {
			p, err := r.St.Backup(id)
			if err != nil || p.Status == "deleted" {
				missing = id
				break
			}
			links = append([]store.Backup{p}, links...)
			id = p.ParentID
		}
		if len(links) == 1 && missing == "" {
			status, msg := r.verifyOne(dest, b, o.ID, l)
			r.finish(o, l, status, msg)
			return
		}
		var report []string
		failed := missing != ""
		if missing != "" {
			report = append(report, "link "+missing+": missing")
		}
		for i, lk := range links {
			status, msg := r.verifyOne(dest, lk, fmt.Sprintf("%s-%d", o.ID, i), l)
			failed = failed || status != "success"
			report = append(report, fmt.Sprintf("link %s (%s #%d): %s", lk.ID, lk.Kind, lk.Seq, msg))
			l.Printf("%s", report[len(report)-1])
		}
		if failed {
			r.finish(o, l, "failed", "chain not restorable: "+strings.Join(report, "; "))
		} else {
			r.finish(o, l, "success", fmt.Sprintf("all %d links ok", len(links)))
		}
	})
	return o.ID, nil
}

// verifyOne re-checks one artifact's checksum and updates its status; returns op status + message.
func (r *Runner) verifyOne(dest store.Destination, b store.Backup, opID string, l *OpLog) (string, string) {
	if dest.ID != b.DestinationID {
		if d, err := r.St.Destination(b.DestinationID); err == nil {
			dest = d
		}
	}
	sum, err := r.checksum(dest, b, opID, l)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.St.SetBackupStatus(b.ID, "corrupted")
		return "failed", "artifact missing: backup marked corrupted"
	case err != nil: // network/auth/IO: the artifact may be fine, keep its status (FR-010)
		return "failed", "destination unreachable: " + err.Error()
	case sum != b.SHA256:
		r.St.SetBackupStatus(b.ID, "corrupted")
		return "failed", "checksum mismatch: backup marked corrupted"
	}
	if b.Status == "corrupted" { // file was repaired/restored from elsewhere
		r.St.SetBackupStatus(b.ID, "ok")
	}
	return "success", "checksum ok " + sum
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
	r.removeManifest(dst, b)
	return r.deleteFiles(dst, b)
}

// DeleteBackup deletes a backup after the caller confirmed by typing its database name. Backups
// that depend on it are deleted too, but only when cascade confirms it (FR-013).
func (r *Runner) DeleteBackup(id, confirm string, cascade bool) error {
	b, err := r.St.Backup(id)
	if err != nil || b.Status == "deleted" {
		return errf(http.StatusNotFound, "not_found", "backup not found")
	}
	if confirm != b.Database {
		return errf(http.StatusConflict, "confirmation_required", "type the database name %q to confirm deletion", b.Database)
	}
	deps, err := r.St.Dependents(b.ID)
	if err != nil {
		return err
	}
	if len(deps) > 0 && !cascade {
		e := errf(http.StatusConflict, "has_dependents", "%d incremental backup(s) depend on this backup and would be deleted with it", len(deps))
		e.Extra = map[string]any{"dependents": len(deps)}
		return e
	}
	// newest links first: an interrupted delete never leaves an incremental without its base
	sort.Slice(deps, func(i, j int) bool { return deps[i].Seq > deps[j].Seq })
	for _, d := range deps {
		if err := r.deleteBackup(d); err != nil {
			return err
		}
	}
	return r.deleteBackup(b)
}

// prune applies plan retention after a successful backup (runs as its own operation).
func (r *Runner) prune(p store.Plan, newest store.Backup) {
	all, err := r.St.Backups(store.BackupFilter{PlanID: p.ID, Status: "ok"})
	if err != nil {
		return
	}
	var drop []store.Backup
	if p.Incremental() { // chains are per database: keep every link a kept backup needs (FR-013)
		byDB := map[string][]store.Backup{}
		for _, b := range all {
			byDB[b.Database] = append(byDB[b.Database], b)
		}
		for db, group := range byDB {
			kept := schedule.Keep(group, p.Retention)
			if db == newest.Database {
				kept[newest.ID] = true
			}
			keep := schedule.Closure(kept, group)
			for _, b := range group {
				if !keep[b.ID] {
					drop = append(drop, b)
				}
			}
		}
		// dependents first, so an interrupted prune never leaves an incremental without its base
		sort.Slice(drop, func(i, j int) bool { return drop[i].Seq > drop[j].Seq })
	} else {
		keep := schedule.Keep(all, p.Retention)
		for _, b := range all {
			if !keep[b.ID] && b.ID != newest.ID {
				drop = append(drop, b)
			}
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
		r.removeManifest(dst, b)
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
