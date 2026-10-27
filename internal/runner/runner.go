// Package runner executes backup, restore, verify and prune operations and records them.
package runner

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/delvivo/dumpling/internal/artifact"
	"github.com/delvivo/dumpling/internal/engine"
	"github.com/delvivo/dumpling/internal/store"
)

type Runner struct {
	St      *store.Store
	DataDir string
	Publish func(event string, v any) // SSE fan-out; may be nil

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

// Recover marks operations left running by a previous process as interrupted and removes their
// partial files (FR-026).
func (r *Runner) Recover() error {
	ops, err := r.St.Unfinished()
	if err != nil {
		return err
	}
	for i := range ops {
		o := &ops[i]
		for _, cid := range []string{o.ConnectionID, o.TargetConnectionID} {
			if c, err := r.St.Connection(cid); err == nil && c.Engine == "mssql" && c.SharePathLocal != "" {
				os.Remove(filepath.Join(c.SharePathLocal, "dumpling-"+o.ID+".bak"))
			}
		}
		o.Status, o.Message, o.FinishedAt = "interrupted", "process stopped while operation was running", store.Now()
		r.St.UpdateOperation(o)
	}
	dests, err := r.St.Destinations()
	if err != nil {
		return err
	}
	for _, d := range dests {
		filepath.WalkDir(d.Path, func(p string, e fs.DirEntry, err error) error {
			if err == nil && !e.IsDir() && strings.HasSuffix(p, artifact.PartialSuffix) {
				os.Remove(p)
			}
			return nil
		})
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
