package runner

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
	TargetPath         string `json:"target_path"` // PostgreSQL physical chains: folder inside the restore root
	Passphrase         string `json:"passphrase"`
	SkipOwnership      bool   `json:"skip_ownership"`
	User               string `json:"-"`
}

// ChainLink is one link shown before a chain restore (FR-014).
type ChainLink struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Seq       int    `json:"seq"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
}

type Preflight struct {
	TargetExists       bool        `json:"target_exists"`
	VersionWarning     string      `json:"version_warning,omitempty"`
	PassphraseRequired bool        `json:"passphrase_required"`
	PassphraseLink     string      `json:"passphrase_link,omitempty"` // first link without a passphrase
	Chain              []ChainLink `json:"chain,omitempty"`
	ChainError         string      `json:"chain_error,omitempty"`
	Physical           bool        `json:"physical,omitempty"`       // restores to a directory (target_path)
	TargetPathOK       bool        `json:"target_path_ok,omitempty"` // physical only
}

type restorePlan struct {
	pf          Preflight
	backup      store.Backup
	links       []store.Backup // oldest first, ending with backup (one entry for standalone backups)
	chainErr    error
	passErr     error
	passphrases []string // per link
	dest        store.Destination
	target      store.Connection
	targetPath  string // physical: absolute output directory
	pathErr     error
	eng         engine.Engine
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
	dest, err := r.St.Destination(b.DestinationID)
	if err != nil {
		return nil, errf(http.StatusInternalServerError, "not_found", "backup destination missing")
	}
	p := &restorePlan{backup: b, links: []store.Backup{b}, dest: dest}

	if b.ParentID != "" { // resolve the links it depends on (FR-010, FR-011)
		anc, err := r.St.Ancestors(b)
		var ce *store.ChainError
		switch {
		case errors.As(err, &ce):
			p.chainErr = ce
			p.pf.ChainError = ce.Error()
		case err != nil:
			return nil, err
		default:
			p.links = append(anc, b)
		}
	}
	for _, l := range p.links {
		p.pf.Chain = append(p.pf.Chain, ChainLink{ID: l.ID, Kind: l.Kind, Seq: l.Seq, Status: l.Status, StartedAt: l.StartedAt})
	}
	for _, l := range p.links { // passphrase per link, before anything is downloaded
		pass, err := r.linkPassphrase(l, req.Passphrase)
		if err != nil && p.passErr == nil {
			p.passErr = err
			p.pf.PassphraseRequired, p.pf.PassphraseLink = true, l.ID
		}
		p.passphrases = append(p.passphrases, pass)
	}

	if b.Physical() {
		p.pf.Physical = true
		if p.eng, err = newEngine(b.Engine); err != nil {
			return nil, err
		}
		p.targetPath, p.pathErr = r.resolveTargetPath(req.TargetPath)
		p.pf.TargetPathOK = p.pathErr == nil
		return p, nil
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
	eng, err := newEngine(t.Engine)
	if err != nil {
		return nil, err
	}
	p.target, p.eng = t, eng
	if exists, err := eng.Exists(r.ctx, r.conn(t, store.NewID()), req.TargetDatabase); err != nil {
		return nil, errf(http.StatusBadGateway, "target_unreachable", "cannot query target: %v", err)
	} else {
		p.pf.TargetExists = exists
	}
	if t.ServerVersion != "" && b.ServerVersion != "" && engine.Major(t.ServerVersion) < engine.Major(b.ServerVersion) {
		p.pf.VersionWarning = fmt.Sprintf("target server %s is older than the backup source %s; the restore may fail", t.ServerVersion, b.ServerVersion)
	}
	return p, nil
}

func (r *Runner) restoreRoot() string {
	if r.RestoreDir != "" {
		return r.RestoreDir
	}
	return filepath.Join(r.DataDir, "restores")
}

// resolveTargetPath confines a physical restore to an empty or new folder inside the restore root;
// symlinks are resolved so a link inside the root cannot point the output elsewhere.
func (r *Runner) resolveTargetPath(rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", errf(http.StatusBadRequest, "target_path_required", "a folder name inside the restore folder is required")
	}
	outside := errf(http.StatusBadRequest, "target_path_outside_root", "the target folder must be inside the restore folder %s", r.restoreRoot())
	root, err := filepath.Abs(r.restoreRoot())
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", outside
	}
	abs := filepath.Join(root, rel)
	if within(root, abs) != nil || abs == root {
		return "", outside
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("restore folder: %w", err)
	}
	// the deepest existing ancestor, symlinks resolved, must still be inside the root
	for dir := abs; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(dir); err == nil {
			real, err := filepath.EvalSymlinks(dir)
			if err != nil || within(realRoot, real) != nil {
				return "", outside
			}
			break
		}
		if dir == root {
			break
		}
	}
	if fi, err := os.Lstat(abs); err == nil {
		entries, rerr := os.ReadDir(abs)
		if !fi.IsDir() || rerr != nil || len(entries) > 0 {
			return "", errf(http.StatusBadRequest, "target_path_not_empty", "%s exists and is not an empty folder", abs)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return abs, nil
}

func within(root, p string) error {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("outside")
	}
	return nil
}

func (r *Runner) Preflight(req RestoreReq) (Preflight, error) {
	p, err := r.plan(req)
	if err != nil {
		return Preflight{}, err
	}
	return p.pf, nil
}

// StartRestore validates the request (FR-016–FR-021) and runs the restore in the background:
// checksum pass over every link first, then drop/recreate (if confirmed) and load, so a corrupted
// artifact never touches the target.
func (r *Runner) StartRestore(req RestoreReq) (string, error) {
	p, err := r.plan(req)
	if err != nil {
		return "", err
	}
	var ce *store.ChainError
	if errors.As(p.chainErr, &ce) {
		e := errf(http.StatusBadRequest, "chain_broken", "cannot restore: %s", ce.Error())
		e.Extra = map[string]any{"link": ce.Link}
		return "", e
	}
	if p.passErr != nil {
		return "", p.passErr
	}
	if p.pathErr != nil {
		return "", p.pathErr
	}
	if !p.pf.Physical && p.pf.TargetExists && req.ConfirmDatabase != req.TargetDatabase {
		e := errf(http.StatusConflict, "confirmation_required",
			"database %q exists: active connections will be terminated and it will be dropped and recreated; type its name to confirm", req.TargetDatabase)
		e.Extra = map[string]any{"target_exists": true, "warnings": nonEmpty(p.pf.VersionWarning)}
		return "", e
	}
	key := "restore:" + p.target.ID + "/" + req.TargetDatabase
	o := &store.Operation{Type: "restore", ConnectionID: p.backup.ConnectionID, BackupID: p.backup.ID,
		TargetConnectionID: p.target.ID, TargetDatabase: req.TargetDatabase, Database: p.backup.Database}
	if p.pf.Physical {
		key = "restore:path:" + p.targetPath
		o.TargetConnectionID, o.TargetDatabase = "", p.targetPath
	}
	if !r.tryLock(key) {
		return "", errf(http.StatusConflict, "restore_in_progress", "a restore into this target is already running")
	}
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
	for _, s := range p.passphrases {
		l.AddSecret(s)
	}
	tools := engine.MissingTools(p.backup.Engine)
	if len(p.links) > 1 || p.pf.Physical {
		tools = engine.MissingIncrTools(p.backup.Engine)
	}
	if len(tools) > 0 {
		r.finish(o, l, "failed", "missing client tools: "+fmt.Sprint(tools))
		return
	}
	dst, err := r.openDest(p.dest, l)
	if err != nil {
		r.finish(o, l, "failed", "destination unreachable: "+err.Error())
		return
	}
	defer dst.Close()

	// 1. fetch and checksum every link before touching the target
	files := make([]*os.File, len(p.links))
	defer func() {
		for _, f := range files {
			if f != nil {
				f.Close()
			}
		}
	}()
	for i, b := range p.links {
		path, cleanup, err := r.fetch(dst, p.dest, b.Path, fmt.Sprintf("%s-%d", o.ID, i), l)
		defer cleanup()
		if err != nil {
			r.finish(o, l, "failed", fmt.Sprintf("read artifact %s: %v", b.ID, err))
			return
		}
		l.Printf("verifying checksum of %s", storage.Location(p.dest, b.Path))
		if files[i], err = os.Open(path); err != nil {
			r.finish(o, l, "failed", "read artifact: "+err.Error())
			return
		}
		sum, err := artifact.Checksum(files[i])
		if err != nil {
			r.finish(o, l, "failed", "read artifact: "+err.Error())
			return
		}
		if sum != b.SHA256 {
			r.St.SetBackupStatus(b.ID, "corrupted")
			r.finish(o, l, "failed", fmt.Sprintf("checksum mismatch on %s: backup marked corrupted, target left untouched", b.ID))
			return
		}
		if _, err := files[i].Seek(0, io.SeekStart); err != nil {
			r.finish(o, l, "failed", "read artifact: "+err.Error())
			return
		}
	}
	open := func(i int) (io.ReadCloser, error) {
		src, err := artifact.Open(files[i], engine.Gzip(p.links[i].Engine, p.links[i].Physical()), p.passphrases[i])
		files[i] = nil // closed through src
		if errors.Is(err, artifact.ErrPassphrase) {
			err = artifact.ErrPassphrase
		}
		return src, err
	}
	if p.pf.TargetExists {
		l.Printf("overwrite of %s confirmed by %s at %s", req.TargetDatabase, req.User, store.Now())
	}
	if p.pf.VersionWarning != "" {
		l.Printf("warning: %s", p.pf.VersionWarning)
	}

	// 2a. standalone backup: stream into the engine as before
	if len(p.links) == 1 && !p.pf.Physical {
		src, err := open(0)
		if err != nil {
			r.finish(o, l, "failed", "open artifact: "+err.Error())
			return
		}
		defer src.Close()
		err = p.eng.Restore(r.ctx, r.conn(p.target, o.ID), req.TargetDatabase, p.pf.TargetExists, src,
			engine.RestoreOpts{SkipOwnership: req.SkipOwnership}, l)
		if err != nil {
			r.finish(o, l, "failed", err.Error())
			return
		}
		r.finish(o, l, "success", fmt.Sprintf("restored into %s/%s", p.target.Name, req.TargetDatabase))
		return
	}

	// 2b. chain: decode every link to a local payload file, then let the engine apply them
	cr, ok := p.eng.(engine.ChainRestorer)
	if !ok {
		r.finish(o, l, "failed", p.backup.Engine+" cannot restore backup chains")
		return
	}
	// ponytail: decoded copies cost disk ≈ sum of the chain; stream per link if that becomes a problem
	links := make([]engine.Link, len(p.links))
	for i, b := range p.links {
		src, err := open(i)
		if err != nil {
			r.finish(o, l, "failed", "open artifact: "+err.Error())
			return
		}
		payload := filepath.Join(r.DataDir, "tmp", fmt.Sprintf("%s-%d.payload", o.ID, i))
		err = copyFile(src, payload)
		src.Close()
		defer os.Remove(payload)
		if err != nil {
			r.finish(o, l, "failed", "decode artifact: "+err.Error())
			return
		}
		links[i] = engine.Link{Marker: b.Marker, Path: payload}
	}
	l.Printf("applying %d link(s): full %s + %d incremental(s)", len(links), p.links[0].ID, len(links)-1)
	target := req.TargetDatabase
	if p.pf.Physical {
		target = p.targetPath
	}
	err = cr.RestoreChain(r.ctx, r.conn(p.target, o.ID), target, p.pf.TargetExists, links,
		engine.RestoreOpts{SkipOwnership: req.SkipOwnership}, l)
	if err != nil {
		r.finish(o, l, "failed", err.Error())
		return
	}
	if p.pf.Physical {
		r.finish(o, l, "success", fmt.Sprintf("data directory rebuilt in %s: chown it to the PostgreSQL server user (mode 0700) and start PostgreSQL %d on it",
			p.targetPath, engine.Major(p.backup.ServerVersion)))
		return
	}
	r.finish(o, l, "success", fmt.Sprintf("restored %d link(s) into %s/%s", len(links), p.target.Name, req.TargetDatabase))
}
