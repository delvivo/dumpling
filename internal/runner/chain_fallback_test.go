package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/delvivo/dumpling/internal/engine"
	"github.com/delvivo/dumpling/internal/store"
)

// fakeIncr is a fake engine with chain support; errors are returned once then cleared.
type fakeIncr struct {
	fakeEngine
	missing  []string
	fullErr  error
	incrErr  error
	incrPrev []string // markers seen by BackupIncr
}

func (f *fakeIncr) Prereqs(context.Context, engine.Conn, string) ([]string, []string, error) {
	return f.missing, nil, nil
}
func (f *fakeIncr) BackupFull(_ context.Context, _ engine.Conn, _ string, w, _, _ io.Writer) (string, []string, error) {
	if err := f.fullErr; err != nil {
		f.fullErr = nil
		w.Write([]byte("partial"))
		return "", nil, err
	}
	_, err := w.Write([]byte("full data"))
	return `{"base_guid":"g1"}`, nil, err
}
func (f *fakeIncr) BackupIncr(_ context.Context, _ engine.Conn, _ string, prev engine.Link, w, _, _ io.Writer) (string, []string, error) {
	f.incrPrev = append(f.incrPrev, prev.Marker)
	if err := f.incrErr; err != nil {
		f.incrErr = nil
		w.Write([]byte("partial"))
		return "", nil, err
	}
	_, err := w.Write([]byte("diff"))
	return prev.Marker, nil, err
}

func (e *env) useIncr(t *testing.T) *fakeIncr {
	f := &fakeIncr{}
	newEngine = func(string) (engine.Engine, error) { return f, nil }
	t.Cleanup(func() { newEngine = engine.For })
	return f
}

// run executes one plan run of kind and returns its operation and the backups created by it.
func (e *env) run(t *testing.T, p store.Plan, kind string) (store.Operation, []store.Backup) {
	t.Helper()
	before, _ := e.st.Backups(store.BackupFilter{PlanID: p.ID})
	ids, err := e.r.RunPlan(p, kind)
	if err != nil {
		t.Fatal(err)
	}
	e.r.Wait()
	o, _ := e.st.Operation(ids[0])
	after, _ := e.st.Backups(store.BackupFilter{PlanID: p.ID})
	return o, after[:len(after)-len(before)]
}

func TestChainFallbacks(t *testing.T) {
	e := setup(t)
	f := e.useIncr(t)
	p := e.incrPlan(t)

	// no base → full that starts a chain
	o, bs := e.run(t, p, "incremental")
	if o.Status != "success" || len(bs) != 1 || bs[0].Kind != "full" || bs[0].ChainID != bs[0].ID {
		t.Fatalf("first run: %s %q %+v", o.Status, o.Message, bs)
	}
	full := bs[0]

	// incremental on the full (mssql: parent = full)
	o, bs = e.run(t, p, "incremental")
	if o.Status != "success" || len(bs) != 1 || bs[0].Kind != "incr" || bs[0].ParentID != full.ID || bs[0].Seq != 1 ||
		bs[0].ChainID != full.ID || !strings.HasSuffix(bs[0].Path, "-incr.bak.gz") {
		t.Fatalf("incremental: %s %+v", o.Status, bs)
	}
	o, bs = e.run(t, p, "incremental")
	if bs[0].ParentID != full.ID || bs[0].Seq != 2 || f.incrPrev[1] != `{"base_guid":"g1"}` {
		t.Fatalf("second diff: %+v %v", bs[0], f.incrPrev)
	}

	// base replaced outside Dumpling → the incremental upload is aborted and a full taken, same op
	f.incrErr = engine.ErrChainBroken
	o, bs = e.run(t, p, "incremental")
	if o.Status != "success" || len(bs) != 1 || bs[0].Kind != "full" {
		t.Fatalf("chain broken fallback: %s %+v", o.Status, bs)
	}
	log, _ := os.ReadFile(filepath.Join(e.dir, "logs", o.ID+".log"))
	if !strings.Contains(string(log), "cannot continue the chain") {
		t.Fatalf("reason not logged:\n%s", log)
	}

	// prerequisite missing → protecting standalone full + visible warning
	f.missing = []string{"binary logging is off"}
	o, bs = e.run(t, p, "incremental")
	if o.Status != "warning" || !strings.Contains(o.Message, "prerequisite missing: binary logging is off") ||
		len(bs) != 1 || bs[0].ChainID != "" {
		t.Fatalf("prerequisite: %s %q %+v", o.Status, o.Message, bs)
	}
	f.missing = nil

	// lock unsafe → warning, no backup at all
	f.fullErr = engine.ErrLockUnsafe
	o, bs = e.run(t, p, "full")
	if o.Status != "warning" || !strings.Contains(o.Message, "full postponed") || len(bs) != 0 {
		t.Fatalf("lock unsafe: %s %q %+v", o.Status, o.Message, bs)
	}

	// no partial artifacts left behind by the aborted attempts
	filepath.Walk(filepath.Join(e.dir, "dest"), func(path string, info os.FileInfo, err error) error {
		if err == nil && strings.HasSuffix(path, ".partial") {
			t.Errorf("partial left: %s", path)
		}
		return nil
	})
}
