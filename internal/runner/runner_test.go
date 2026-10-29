package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/delvivo/dumpling/internal/artifact"
	"github.com/delvivo/dumpling/internal/engine"
	"github.com/delvivo/dumpling/internal/secret"
	"github.com/delvivo/dumpling/internal/storage"
	"github.com/delvivo/dumpling/internal/store"
)

func init() { artifact.ScryptWorkFactor = 10 }

type fakeEngine struct {
	mu       sync.Mutex
	exists   bool
	restored []string
	block    chan struct{}
}

func (f *fakeEngine) Test(context.Context, engine.Conn) (engine.TestResult, error) {
	return engine.TestResult{Version: "16.0"}, nil
}
func (f *fakeEngine) Backup(_ context.Context, _ engine.Conn, _ string, w io.Writer, _ io.Writer) ([]string, error) {
	_, err := w.Write([]byte("dump data"))
	return nil, err
}
func (f *fakeEngine) Exists(context.Context, engine.Conn, string) (bool, error) { return f.exists, nil }
func (f *fakeEngine) Restore(_ context.Context, _ engine.Conn, db string, overwrite bool, r io.Reader, _ engine.RestoreOpts, _ io.Writer) error {
	if f.block != nil {
		<-f.block
	}
	b, _ := io.ReadAll(r)
	f.mu.Lock()
	f.restored = append(f.restored, db+":"+string(b))
	f.mu.Unlock()
	return nil
}

type env struct {
	r    *Runner
	st   *store.Store
	fake *fakeEngine
	conn store.Connection
	dest store.Destination
	dir  string
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"logs", "tmp", "dest"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	box, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	st, err := store.Open(filepath.Join(dir, "db.sqlite"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB.Close() })
	fake := &fakeEngine{}
	newEngine = func(string) (engine.Engine, error) { return fake, nil }
	t.Cleanup(func() { newEngine = engine.For })
	e := &env{r: New(context.Background(), st, dir, nil), st: st, fake: fake, dir: dir}
	e.conn = store.Connection{Name: "a", Engine: "mssql", Host: "h", Port: 1, Username: "u", Password: "pw", ServerVersion: "16.0"}
	st.SaveConnection(&e.conn)
	e.dest = store.Destination{Name: "d", Path: filepath.Join(dir, "dest")}
	st.SaveDestination(&e.dest)
	return e
}

// backup writes a real artifact and backup row directly.
func (e *env) backup(t *testing.T, pass string) store.Backup {
	t.Helper()
	rel := "adhoc/a/shop/" + store.NewID() + ".bak.gz"
	dst, _ := storage.Open(context.Background(), e.dest)
	up, err := dst.Create(rel)
	if err != nil {
		t.Fatal(err)
	}
	w, err := artifact.Create(up, true, pass)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("payload"))
	size, sum, _ := w.Commit(dst, rel, nil)
	b := store.Backup{ConnectionID: e.conn.ID, ConnectionName: "a", Engine: "mssql", ServerVersion: "16.0", Database: "shop",
		DestinationID: e.dest.ID, Path: rel, SizeBytes: size, SHA256: sum, Encrypted: pass != "", Status: "ok",
		StartedAt: store.Now(), FinishedAt: store.Now()}
	if err := e.st.InsertBackup(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestRestoreGuards(t *testing.T) {
	e := setup(t)
	b := e.backup(t, "")
	req := RestoreReq{BackupID: b.ID, TargetConnectionID: e.conn.ID, TargetDatabase: "shop_copy"}

	other := store.Connection{Name: "pg", Engine: "postgres", Host: "h", Port: 1, Username: "u"}
	e.st.SaveConnection(&other)
	if _, err := e.r.StartRestore(RestoreReq{BackupID: b.ID, TargetConnectionID: other.ID, TargetDatabase: "x"}); code(err) != "engine_mismatch" {
		t.Fatalf("engine mismatch: %v", err)
	}

	e.fake.exists = true
	if _, err := e.r.StartRestore(req); code(err) != "confirmation_required" {
		t.Fatalf("no confirm: %v", err)
	}
	wrong := req
	wrong.ConfirmDatabase = "shop"
	if _, err := e.r.StartRestore(wrong); code(err) != "confirmation_required" {
		t.Fatalf("wrong confirm: %v", err)
	}

	// Concurrent restore into same target is refused.
	e.fake.block = make(chan struct{})
	ok := req
	ok.ConfirmDatabase = "shop_copy"
	if _, err := e.r.StartRestore(ok); err != nil {
		t.Fatal(err)
	}
	if _, err := e.r.StartRestore(ok); code(err) != "restore_in_progress" {
		t.Fatalf("concurrent: %v", err)
	}
	close(e.fake.block)
	e.r.Wait()
	if len(e.fake.restored) != 1 || e.fake.restored[0] != "shop_copy:payload" {
		t.Fatalf("restored %q", e.fake.restored)
	}
	e.fake.block = nil

	enc := e.backup(t, "secret-pass")
	if _, err := e.r.StartRestore(RestoreReq{BackupID: enc.ID, TargetConnectionID: e.conn.ID, TargetDatabase: "n", ConfirmDatabase: "n"}); code(err) != "passphrase_required" {
		t.Fatalf("passphrase: %v", err)
	}

	e.st.SetBackupStatus(b.ID, "corrupted")
	if _, err := e.r.StartRestore(ok); code(err) != "backup_not_ok" {
		t.Fatalf("not ok: %v", err)
	}
}

func TestRestoreChecksumMismatchLeavesTargetUntouched(t *testing.T) {
	e := setup(t)
	b := e.backup(t, "")
	p := filepath.Join(e.dest.Path, b.Path)
	data, _ := os.ReadFile(p)
	data[len(data)/2] ^= 0xff
	os.WriteFile(p, data, 0o640)

	id, err := e.r.StartRestore(RestoreReq{BackupID: b.ID, TargetConnectionID: e.conn.ID, TargetDatabase: "t"})
	if err != nil {
		t.Fatal(err)
	}
	e.r.Wait()
	if len(e.fake.restored) != 0 {
		t.Fatal("engine.Restore must not run on corrupted artifact")
	}
	if o, _ := e.st.Operation(id); o.Status != "failed" {
		t.Fatalf("op %s", o.Status)
	}
	if got, _ := e.st.Backup(b.ID); got.Status != "corrupted" {
		t.Fatalf("backup %s", got.Status)
	}
}

func TestEncryptedRestoreWithPassphrase(t *testing.T) {
	e := setup(t)
	b := e.backup(t, "pp")
	if _, err := e.r.StartRestore(RestoreReq{BackupID: b.ID, TargetConnectionID: e.conn.ID, TargetDatabase: "t", Passphrase: "pp"}); err != nil {
		t.Fatal(err)
	}
	e.r.Wait()
	if len(e.fake.restored) != 1 || e.fake.restored[0] != "t:payload" {
		t.Fatalf("%q", e.fake.restored)
	}
}

func TestRecover(t *testing.T) {
	e := setup(t)
	o := &store.Operation{Type: "backup", ConnectionID: e.conn.ID}
	e.r.newOp(o)
	o.Status = "running"
	e.st.UpdateOperation(o)
	partial := filepath.Join(e.dest.Path, "p", "x", "db", "a.bak.gz"+storage.PartialSuffix)
	os.MkdirAll(filepath.Dir(partial), 0o755)
	os.WriteFile(partial, []byte("half"), 0o640)

	tmp := filepath.Join(e.dir, "tmp", "OLD"+tmpSuffix)
	os.WriteFile(tmp, []byte("stale download"), 0o600)
	if err := e.r.Recover(); err != nil {
		t.Fatal(err)
	}
	e.r.Wait()
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("stale temp download not removed")
	}
	if got, _ := e.st.Operation(o.ID); got.Status != "interrupted" {
		t.Fatalf("status %s", got.Status)
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatal("partial not removed")
	}
}

func TestBackupAndPlanOverlap(t *testing.T) {
	e := setup(t)
	plan := store.Plan{Name: "nightly", ConnectionID: e.conn.ID, Databases: []string{"shop"}, DestinationID: e.dest.ID,
		Schedule: "0 2 * * *", Retention: store.Retention{Last: 1}}
	if err := e.st.SavePlan(&plan); err != nil {
		t.Fatal(err)
	}
	e.r.tryLock("plan:" + plan.ID) // simulate running plan
	ids, _ := e.r.StartBackup(BackupReq{Conn: e.conn, Databases: plan.Databases, Dest: e.dest, Plan: &plan})
	if o, _ := e.st.Operation(ids[0]); o.Status != "skipped" {
		t.Fatalf("overlap: %s", o.Status)
	}
	e.r.unlock("plan:" + plan.ID)

	for i := 0; i < 2; i++ {
		ids, _ = e.r.StartBackup(BackupReq{Conn: e.conn, Databases: plan.Databases, Dest: e.dest, Plan: &plan})
		e.r.Wait()
	}
	o, _ := e.st.Operation(ids[0])
	if o.Status != "success" || o.BackupID == "" {
		t.Fatalf("backup op %+v", o)
	}
	ok, _ := e.st.Backups(store.BackupFilter{PlanID: plan.ID, Status: "ok"})
	if len(ok) != 1 || ok[0].ID != o.BackupID {
		t.Fatalf("retention last=1 kept %d", len(ok))
	}
}

func verifyOutcome(t *testing.T, e *env, b store.Backup) (store.Operation, store.Backup) {
	t.Helper()
	id, err := e.r.StartVerify(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.r.Wait()
	o, _ := e.st.Operation(id)
	got, _ := e.st.Backup(b.ID)
	return o, got
}

// FR-010: verify tells missing, unreachable and checksum mismatch apart.
func TestVerifyOutcomes(t *testing.T) {
	e := setup(t)
	b := e.backup(t, "")
	if o, _ := verifyOutcome(t, e, b); o.Status != "success" {
		t.Fatalf("ok: %+v", o)
	}

	openStorage = func(context.Context, store.Destination) (storage.Backend, error) {
		return nil, errors.New("dial tcp: connection refused")
	}
	o, got := verifyOutcome(t, e, b)
	openStorage = storage.Open
	if o.Status != "failed" || !strings.Contains(o.Message, "destination unreachable") || got.Status != "ok" {
		t.Fatalf("unreachable: %+v %s", o, got.Status)
	}

	p := filepath.Join(e.dest.Path, b.Path)
	data, _ := os.ReadFile(p)
	data[len(data)-1] ^= 0xff
	os.WriteFile(p, data, 0o640)
	if o, got := verifyOutcome(t, e, b); !strings.Contains(o.Message, "checksum mismatch") || got.Status != "corrupted" {
		t.Fatalf("mismatch: %+v %s", o, got.Status)
	}

	os.Remove(p)
	if o, got := verifyOutcome(t, e, b); !strings.Contains(o.Message, "artifact missing") || got.Status != "corrupted" {
		t.Fatalf("missing: %+v %s", o, got.Status)
	}
}

// failRemove fails Remove for one path.
type failRemove struct {
	storage.Backend
	fail string
}

func (f failRemove) Remove(rel string) error {
	if rel == f.fail {
		return errors.New("permission denied")
	}
	return f.Backend.Remove(rel)
}

// FR-011: a failed remote delete keeps the backup and ends the prune with a warning.
func TestPruneWarningKeepsBackup(t *testing.T) {
	e := setup(t)
	plan := store.Plan{Name: "p", ConnectionID: e.conn.ID, Databases: []string{"shop"}, DestinationID: e.dest.ID,
		Schedule: "0 2 * * *", Retention: store.Retention{Last: 1}}
	e.st.SavePlan(&plan)
	old := e.backup(t, "")
	e.st.DB.Exec(`UPDATE backups SET plan_id=?, started_at='2020-01-01T00:00:00Z' WHERE id=?`, plan.ID, old.ID)
	openStorage = func(ctx context.Context, d store.Destination) (storage.Backend, error) {
		b, err := storage.Open(ctx, d)
		return failRemove{b, old.Path}, err
	}
	t.Cleanup(func() { openStorage = storage.Open })

	e.r.StartBackup(BackupReq{Conn: e.conn, Databases: plan.Databases, Dest: e.dest, Plan: &plan})
	e.r.Wait()
	if got, _ := e.st.Backup(old.ID); got.Status != "ok" {
		t.Fatalf("failed delete must keep backup, got %s", got.Status)
	}
	ops, _ := e.st.Operations(store.OpFilter{Type: "prune"})
	if len(ops) != 1 || ops[0].Status != "warning" || !strings.Contains(ops[0].Message, old.Path) {
		t.Fatalf("prune op %+v", ops)
	}
}

// Remote restores download to a temp file that is removed afterwards, success or failure.
func TestFetchTempRemoved(t *testing.T) {
	e := setup(t)
	b := e.backup(t, "")
	d := e.dest
	d.Type, d.S3 = "s3", &store.S3Config{Bucket: "b"} // force the download path; backend stays local
	dst, _ := storage.Open(context.Background(), e.dest)
	l, _ := CreateLog(e.dir, "op1", nil)
	defer l.Close()
	p, cleanup, err := e.r.fetch(dst, d, b.Path, "op1", l)
	if err != nil || filepath.Dir(p) != filepath.Join(e.dir, "tmp") {
		t.Fatalf("fetch %s %v", p, err)
	}
	cleanup()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("temp download not removed")
	}
	if _, _, err := e.r.fetch(dst, d, "missing.bak", "op2", l); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if m, _ := filepath.Glob(filepath.Join(e.dir, "tmp", "*"+tmpSuffix)); len(m) != 0 {
		t.Fatalf("leftovers %v", m)
	}
}
