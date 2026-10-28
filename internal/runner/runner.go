// Package runner executes backup, restore, verify and prune operations and records them.
package runner

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/delvivo/dumpling/internal/engine"
	"github.com/delvivo/dumpling/internal/storage"
	"github.com/delvivo/dumpling/internal/store"
)

type Runner struct {
	St      *store.Store
	DataDir string
	Publish func(event string, v any) // SSE fan-out; may be nil
	// RestoreDir confines PostgreSQL physical restores ("" = <DataDir>/restores).
	RestoreDir string

	ctx   context.Context
	wg    sync.WaitGroup
	mu    sync.Mutex
	locks map[string]bool
}

func New(ctx context.Context, st *store.Store, dataDir string, publish func(string, any)) *Runner {
	if publish == nil {
		publish = func(string, any) {}
	}
	return &Runner{St: st, DataDir: dataDir, Publish: publish, ctx: ctx, locks: map[string]bool{}}
}

// Wait blocks until all running operations return (after ctx cancel on shutdown).
func (r *Runner) Wait() { r.wg.Wait() }

// ponytail: in-memory locks; fine for a single process, needs DB/file locks if ever multi-instance.
func (r *Runner) tryLock(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locks[key] {
		return false
	}
	r.locks[key] = true
	return true
}

func (r *Runner) unlock(key string) {
	r.mu.Lock()
	delete(r.locks, key)
	r.mu.Unlock()
}

func (r *Runner) goRun(fn func()) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		fn()
	}()
}

// newOp inserts a pending operation.
func (r *Runner) newOp(o *store.Operation) error {
	o.Status = "pending"
	if err := r.St.InsertOperation(o); err != nil {
		return err
	}
	r.Publish("operation", *o)
	return nil
}

// start marks an operation running and opens its log.
func (r *Runner) start(o *store.Operation) (*OpLog, error) {
	l, err := CreateLog(r.DataDir, o.ID, func(line string) {
		r.Publish("log", map[string]string{"id": o.ID, "line": line})
	})
	if err != nil {
		r.finish(o, nil, "failed", "cannot open log: "+err.Error())
		return nil, err
	}
	o.Status, o.StartedAt = "running", store.Now()
	slog.Info("operation started", "id", o.ID, "type", o.Type, "database", o.Database)
	r.St.UpdateOperation(o)
	r.Publish("operation", *o)
	return l, nil
}

func (r *Runner) finish(o *store.Operation, l *OpLog, status, msg string) {
	if l != nil {
		msg = l.Redact(msg)
		if msg != "" {
			l.Printf("%s: %s", status, msg)
		} else {
			l.Printf("%s", status)
		}
		l.Close()
	}
	o.Status, o.Message, o.FinishedAt = status, msg, store.Now()
	lvl := slog.LevelInfo
	switch status {
	case "failed":
		lvl = slog.LevelError
	case "warning":
		lvl = slog.LevelWarn
	}
	slog.Log(context.Background(), lvl, "operation finished", "id", o.ID, "type", o.Type, "database", o.Database, "status", status, "msg", msg)
	if err := r.St.UpdateOperation(o); err != nil {
		slog.Error("update operation", "id", o.ID, "err", err)
	}
	r.Publish("operation", *o)
}

func (r *Runner) conn(c store.Connection, opID string) engine.Conn {
	return engine.Conn{Engine: c.Engine, Host: c.Host, Port: c.Port, Username: c.Username, Password: c.Password,
		TLSMode: c.TLSMode, SharePathServer: c.SharePathServer, SharePathLocal: c.SharePathLocal,
		TmpDir: filepath.Join(r.DataDir, "tmp"), OpID: opID}
}

// EngineConn exposes connection resolution for connection tests.
func (r *Runner) EngineConn(c store.Connection) engine.Conn { return r.conn(c, store.NewID()) }

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// safeName makes a user-provided name usable as one path segment (no separators, no "..").
func safeName(s string) string {
	s = strings.Trim(unsafeChars.ReplaceAllString(s, "_"), ".")
	if s == "" {
		return "_"
	}
	return s
}

func artifactPath(dest store.Destination, rel string) string {
	return filepath.Join(dest.Path, filepath.FromSlash(rel))
}

// openDest connects to a destination and registers its secrets for log redaction.
func (r *Runner) openDest(d store.Destination, l *OpLog) (storage.Backend, error) {
	if l != nil {
		for _, s := range d.Secrets() {
			l.AddSecret(s)
		}
	}
	return openStorage(r.ctx, d)
}

// openStorage is replaceable in tests.
var openStorage = storage.Open

// fetch returns a local file holding rel: the artifact itself for local destinations, otherwise a
// one-time download to <data-dir>/tmp/<op-id>.artifact removed by the returned cleanup (FR-009).
func (r *Runner) fetch(b storage.Backend, d store.Destination, rel, opID string, l *OpLog) (string, func(), error) {
	if d.Type == "" || d.Type == "local" {
		return artifactPath(d, rel), func() {}, nil
	}
	l.Printf("downloading %s", storage.Location(d, rel))
	src, err := b.Open(rel)
	if err != nil {
		return "", func() {}, err
	}
	defer src.Close()
	tmp := filepath.Join(r.DataDir, "tmp", opID+tmpSuffix)
	cleanup := func() { os.Remove(tmp) }
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", func() {}, err
	}
	_, err = io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	return tmp, cleanup, nil
}

const tmpSuffix = ".artifact"

// Recover marks operations left running by a previous process as interrupted and removes their
// partial files and temp downloads (FR-026, FR-014). Remote destinations are cleaned in the
// background so startup never waits on the network.
func (r *Runner) Recover() error {
	ops, err := r.St.Unfinished()
	if err != nil {
		return err
	}
	for i := range ops {
		o := &ops[i]
		for _, cid := range []string{o.ConnectionID, o.TargetConnectionID} {
			if c, err := r.St.Connection(cid); err == nil && c.Engine == "mssql" && c.SharePathLocal != "" {
				leftovers, _ := filepath.Glob(filepath.Join(c.SharePathLocal, "dumpling-"+o.ID+"*.bak")) // chain restores: -<i>.bak
				for _, f := range leftovers {
					os.Remove(f)
				}
			}
		}
		o.Status, o.Message, o.FinishedAt = "interrupted", "process stopped while operation was running", store.Now()
		r.St.UpdateOperation(o)
	}
	// nothing runs yet: every temp entry (downloads, decoded payloads, manifests, extracted
	// chain directories, credential files) is a leftover
	if tmps, err := filepath.Glob(filepath.Join(r.DataDir, "tmp", "*")); err == nil {
		for _, p := range tmps {
			os.RemoveAll(p)
		}
	}
	dests, err := r.St.Destinations()
	if err != nil {
		return err
	}
	cleanup := func(d store.Destination) {
		ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
		defer cancel()
		b, err := openStorage(ctx, d)
		if err == nil {
			err = b.Cleanup()
			b.Close()
		}
		if err != nil {
			slog.Warn("destination cleanup", "destination", d.Name, "err", err)
		}
	}
	for _, d := range dests {
		if d.Type == "local" {
			cleanup(d)
		} else {
			r.goRun(func() { cleanup(d) })
		}
	}
	if len(ops) > 0 {
		slog.Warn("marked interrupted operations", "count", len(ops))
	}
	return nil
}

// Error is a request-level failure with a machine code for the API.
type Error struct {
	Status int
	Code   string
	Msg    string
	Extra  map[string]any
}

func (e *Error) Error() string { return e.Msg }

func errf(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Msg: fmt.Sprintf(format, args...)}
}
