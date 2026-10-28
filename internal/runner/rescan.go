package runner

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/delvivo/dumpling/internal/artifact"
	"github.com/delvivo/dumpling/internal/engine"
	"github.com/delvivo/dumpling/internal/storage"
	"github.com/delvivo/dumpling/internal/store"
)

type RescanSkip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"` // known | no_artifact | size_mismatch | bad_metadata
}

type RescanResult struct {
	Imported int          `json:"imported"`
	Skipped  []RescanSkip `json:"skipped"`
}

// sidecar mirrors the metadata written by backup (artifact.Commit).
type sidecar struct {
	Engine        string `json:"engine"`
	ServerVersion string `json:"server_version"`
	Database      string `json:"database"`
	Connection    string `json:"connection"`
	Plan          string `json:"plan"`
	PlanID        string `json:"plan_id"`
	Encrypted     *bool  `json:"encrypted"`
	Gzip          *bool  `json:"gzip"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at"`
	OperationID   string `json:"operation_id"`
	SizeBytes     *int64 `json:"size_bytes"`
	SHA256        string `json:"sha256"`
	// chains (003): links reference each other by backup id
	BackupID string `json:"backup_id"`
	Kind     string `json:"kind"`
	ChainID  string `json:"chain_id"`
	ParentID string `json:"parent_id"`
	Seq      int    `json:"seq"`
	Marker   string `json:"marker"`
}

func (m sidecar) valid() bool {
	switch m.Engine {
	case "postgres", "mysql", "mssql":
	default:
		return false
	}
	return m.Database != "" && m.SHA256 != "" && m.SizeBytes != nil && m.Encrypted != nil && m.Gzip != nil &&
		m.StartedAt != "" && *m.Gzip == engine.Gzip(m.Engine, m.Engine == "postgres" && m.ChainID != "")
}

// Rescan imports backups present on a destination but unknown to this install (FR-016). It never
// writes to the destination. Sidecars of this install's own operations (also in-flight ones) are
// skipped, so it cannot race a running backup into a duplicate.
// ponytail: synchronous; move to a background operation if destinations grow to tens of thousands of backups.
func (r *Runner) Rescan(dest store.Destination) (RescanResult, error) {
	res := RescanResult{Skipped: []RescanSkip{}}
	key := "rescan:" + dest.ID
	if !r.tryLock(key) {
		return res, errf(http.StatusConflict, "rescan_in_progress", "a rescan of this destination is already running")
	}
	defer r.unlock(key)
	b, err := r.openDest(dest, nil)
	if err != nil {
		return res, errf(http.StatusBadGateway, "unreachable", "destination unreachable: %v", err)
	}
	defer b.Close()
	sidecars, err := b.List(".json")
	if err != nil {
		return res, errf(http.StatusBadGateway, "unreachable", "list destination: %v", err)
	}
	skip := func(rel, reason string) { res.Skipped = append(res.Skipped, RescanSkip{rel, reason}) }
	for _, j := range sidecars {
		rel := strings.TrimSuffix(j, ".json")
		if _, err := r.St.BackupByPath(dest.ID, rel); err == nil {
			skip(rel, "known")
			continue
		}
		m, err := readSidecar(b, j)
		if errors.Is(err, errBadMetadata) || err == nil && !m.valid() {
			skip(rel, "bad_metadata")
			continue
		}
		if err != nil {
			return res, errf(http.StatusBadGateway, "unreachable", "read %s: %v", j, err)
		}
		if m.OperationID != "" && r.St.OperationExists(m.OperationID) {
			skip(rel, "known")
			continue
		}
		size, err := b.Stat(rel)
		if errors.Is(err, fs.ErrNotExist) {
			skip(rel, "no_artifact")
			continue
		}
		if err != nil {
			return res, errf(http.StatusBadGateway, "unreachable", "stat %s: %v", rel, err)
		}
		if size != *m.SizeBytes {
			skip(rel, "size_mismatch")
			continue
		}
		bk := store.Backup{ConnectionName: m.Connection, Engine: m.Engine, ServerVersion: m.ServerVersion, Database: m.Database,
			DestinationID: dest.ID, Path: rel, SizeBytes: size, SHA256: m.SHA256, Encrypted: *m.Encrypted, Status: "ok",
			StartedAt: m.StartedAt, FinishedAt: m.FinishedAt, PlanName: m.Plan, Imported: true, OriginPlanID: m.PlanID,
			Kind: m.Kind, ChainID: m.ChainID, ParentID: m.ParentID, Seq: m.Seq, Marker: m.Marker}
		// keep the original id so chain links still find each other (FR-016); a link whose parent is
		// not on this destination shows as a broken chain
		if m.BackupID != "" {
			if _, err := r.St.Backup(m.BackupID); errors.Is(err, store.ErrNotFound) {
				bk.ID = m.BackupID
			}
		}
		if bk.FinishedAt == "" {
			bk.FinishedAt = bk.StartedAt
		}
		if err := r.St.InsertBackup(&bk); err != nil {
			return res, err
		}
		res.Imported++
	}
	return res, nil
}

var errBadMetadata = errors.New("bad metadata")

func readSidecar(b storage.Backend, rel string) (sidecar, error) {
	var m sidecar
	rc, err := b.Open(rel)
	if err != nil {
		return m, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 1<<20))
	if err != nil {
		return m, err
	}
	if json.Unmarshal(data, &m) != nil {
		return m, errBadMetadata
	}
	return m, nil
}

// Unlock validates a passphrase for a locked imported backup by decrypting its header and stores it
// sealed (FR-018). With applyToOrigin it is tried on every other locked backup of the same origin
// (same original plan, or same engine+database when the sidecar had no plan id).
func (r *Runner) Unlock(id, passphrase string, applyToOrigin bool) ([]string, error) {
	b, err := r.St.Backup(id)
	if err != nil || b.Status == "deleted" {
		return nil, errf(http.StatusNotFound, "not_found", "backup not found")
	}
	if !b.Locked {
		return nil, errf(http.StatusBadRequest, "not_locked", "backup is not locked")
	}
	if passphrase == "" {
		return nil, errf(http.StatusBadRequest, "invalid", "passphrase required")
	}
	backends := map[string]storage.Backend{}
	defer func() {
		for _, bk := range backends {
			bk.Close()
		}
	}()
	check := func(bk store.Backup) error {
		dst, ok := backends[bk.DestinationID]
		if !ok {
			dest, err := r.St.Destination(bk.DestinationID)
			if err != nil {
				return err
			}
			if dst, err = r.openDest(dest, nil); err != nil {
				return err
			}
			backends[bk.DestinationID] = dst
		}
		rc, err := dst.Open(bk.Path)
		if err != nil {
			return err
		}
		defer rc.Close()
		return artifact.CheckPassphrase(rc, passphrase)
	}
	if err := check(b); errors.Is(err, artifact.ErrPassphrase) {
		return nil, errf(http.StatusBadRequest, "wrong_passphrase", "wrong passphrase")
	} else if err != nil {
		return nil, errf(http.StatusBadGateway, "unreachable", "read artifact: %v", err)
	}
	if err := r.St.SetBackupPassphrase(b.ID, passphrase); err != nil {
		return nil, err
	}
	unlocked := []string{b.ID}
	if !applyToOrigin {
		return unlocked, nil
	}
	all, err := r.St.Backups(store.BackupFilter{Status: "ok"})
	if err != nil {
		return unlocked, err
	}
	for _, c := range all {
		same := c.OriginPlanID == b.OriginPlanID && (b.OriginPlanID != "" || c.Engine == b.Engine && c.Database == b.Database)
		if c.ID == b.ID || !c.Locked || !same || check(c) != nil {
			continue
		}
		if r.St.SetBackupPassphrase(c.ID, passphrase) == nil {
			unlocked = append(unlocked, c.ID)
		}
	}
	return unlocked, nil
}
