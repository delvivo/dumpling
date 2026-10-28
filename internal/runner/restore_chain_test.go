package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/delvivo/dumpling/internal/artifact"
	"github.com/delvivo/dumpling/internal/engine"
	"github.com/delvivo/dumpling/internal/storage"
	"github.com/delvivo/dumpling/internal/store"
)

// fakeChain records the decoded payloads a chain restore receives.
type fakeChain struct {
	fakeEngine
	got    []string
	target string
}

func (f *fakeChain) RestoreChain(_ context.Context, _ engine.Conn, db string, _ bool, links []engine.Link, _ engine.RestoreOpts, _ io.Writer) error {
	f.target = db
	for _, l := range links {
		b, err := os.ReadFile(l.Path)
		if err != nil {
			return err
		}
		f.got = append(f.got, string(b))
	}
	return nil
}

func (e *env) useChain(t *testing.T) *fakeChain {
	f := &fakeChain{}
	newEngine = func(string) (engine.Engine, error) { return f, nil }
	t.Cleanup(func() { newEngine = engine.For })
	return f
}

// artLink writes a real artifact for a chain link on top of parent (nil = full).
func (e *env) artLink(t *testing.T, p store.Plan, parent *store.Backup, payload, pass string) store.Backup {
	t.Helper()
	rel := fmt.Sprintf("inc/a/shop/%s.bak.gz", store.NewID())
	dst, _ := storage.Open(context.Background(), e.dest)
	up, err := dst.Create(rel)
	if err != nil {
		t.Fatal(err)
	}
	w, _ := artifact.Create(up, true, pass)
	w.Write([]byte(payload))
	size, sum, _ := w.CommitBare()
	b := store.Backup{ID: store.NewID(), PlanID: p.ID, ConnectionID: e.conn.ID, ConnectionName: "a", Engine: "mssql",
		ServerVersion: "16.0", Database: "shop", DestinationID: e.dest.ID, Path: rel, SizeBytes: size, SHA256: sum,
		Encrypted: pass != "", Status: "ok", StartedAt: store.Now(), FinishedAt: store.Now()}
	if parent == nil {
		b.ChainID = b.ID
	} else {
		b.Kind, b.ChainID, b.ParentID, b.Seq = "incr", parent.ChainID, parent.ID, parent.Seq+1
	}
	if err := e.st.InsertBackup(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRestoreChain(t *testing.T) {
	e := setup(t)
	f := e.useChain(t)
	p := e.incrPlan(t)
	full := e.artLink(t, p, nil, "F", "")
	i1 := e.artLink(t, p, &full, "I1", "")
	i2 := e.artLink(t, p, &i1, "I2", "")
	req := RestoreReq{BackupID: i2.ID, TargetConnectionID: e.conn.ID, TargetDatabase: "shop_copy"}

	pf, err := e.r.Preflight(req)
	if err != nil || len(pf.Chain) != 3 || pf.Chain[0].ID != full.ID || pf.ChainError != "" {
		t.Fatalf("preflight: %+v %v", pf, err)
	}
	id, err := e.r.StartRestore(req)
	if err != nil {
		t.Fatal(err)
	}
	e.r.Wait()
	if o, _ := e.st.Operation(id); o.Status != "success" || strings.Join(f.got, ",") != "F,I1,I2" || f.target != "shop_copy" {
		t.Fatalf("restore: %s %q got=%v", o.Status, o.Message, f.got)
	}
	// decoded payloads are removed afterwards
	if left, _ := filepath.Glob(filepath.Join(e.dir, "tmp", "*.payload")); len(left) != 0 {
		t.Fatalf("payloads left: %v", left)
	}
}

func TestRestoreChainGuards(t *testing.T) {
	e := setup(t)
	f := e.useChain(t)
	p := e.incrPlan(t)
	full := e.artLink(t, p, nil, "F", "")
	i1 := e.artLink(t, p, &full, "I1", "")
	i2 := e.artLink(t, p, &i1, "I2", "")
	req := RestoreReq{BackupID: i2.ID, TargetConnectionID: e.conn.ID, TargetDatabase: "shop_copy"}

	// broken intermediate link: refused up front, naming it, engine never called
	e.st.SetBackupStatus(i1.ID, "corrupted")
	if pf, _ := e.r.Preflight(req); !strings.Contains(pf.ChainError, i1.ID) {
		t.Fatalf("preflight chain_error: %+v", pf)
	}
	_, err := e.r.StartRestore(req)
	var re *Error
	if code(err) != "chain_broken" || !errorsAsErr(err, &re) || re.Extra["link"] != i1.ID {
		t.Fatalf("broken chain: %v", err)
	}
	e.st.SetBackupStatus(i1.ID, "ok")

	// checksum mismatch on an ancestor: operation fails before the target, link marked corrupted
	os.WriteFile(artifactPath(e.dest, full.Path), []byte("tampered"), 0o600)
	id, err := e.r.StartRestore(req)
	if err != nil {
		t.Fatal(err)
	}
	e.r.Wait()
	o, _ := e.st.Operation(id)
	if b, _ := e.st.Backup(full.ID); o.Status != "failed" || b.Status != "corrupted" || len(f.got) != 0 {
		t.Fatalf("tampered: %s %q full=%s got=%v", o.Status, o.Message, b.Status, f.got)
	}
}

func TestRestoreChainLockedLink(t *testing.T) {
	e := setup(t)
	e.useChain(t)
	p := e.incrPlan(t)
	full := e.artLink(t, p, nil, "F", "")
	i1 := e.artLink(t, p, &full, "I1", "sesame")
	i2 := e.artLink(t, p, &i1, "I2", "")
	// i1 imported, encrypted, passphrase unknown to this install
	e.st.DB.Exec(`UPDATE backups SET imported=1, plan_id=NULL WHERE id=?`, i1.ID)

	_, err := e.r.StartRestore(RestoreReq{BackupID: i2.ID, TargetConnectionID: e.conn.ID, TargetDatabase: "x"})
	var re *Error
	if code(err) != "backup_locked" || !errorsAsErr(err, &re) || re.Extra["link"] != i1.ID {
		t.Fatalf("locked link: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, "tmp", "*")); len(left) != 0 {
		t.Fatalf("downloaded before refusing: %v", left)
	}
}

func errorsAsErr(err error, re **Error) bool { return errors.As(err, re) }

func TestResolveTargetPath(t *testing.T) {
	e := setup(t)
	e.r.RestoreDir = filepath.Join(e.dir, "restores")
	os.MkdirAll(e.r.RestoreDir, 0o700)
	if p, err := e.r.resolveTargetPath("pgr"); err != nil || p != filepath.Join(e.r.RestoreDir, "pgr") {
		t.Fatalf("plain name: %q %v", p, err)
	}
	if _, err := e.r.resolveTargetPath("nested/dir"); err != nil {
		t.Fatalf("nested: %v", err)
	}
	for _, bad := range []string{"../x", "a/../../x", filepath.Join(e.dir, "abs"), "."} {
		if _, err := e.r.resolveTargetPath(bad); code(err) != "target_path_outside_root" {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := e.r.resolveTargetPath(" "); code(err) != "target_path_required" {
		t.Errorf("empty: %v", err)
	}
	full := filepath.Join(e.r.RestoreDir, "full")
	os.MkdirAll(full, 0o700)
	os.WriteFile(filepath.Join(full, "PG_VERSION"), []byte("17"), 0o600)
	if _, err := e.r.resolveTargetPath("full"); code(err) != "target_path_not_empty" {
		t.Errorf("non-empty: %v", err)
	}
	// a symlink inside the root pointing outside it
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(e.r.RestoreDir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := e.r.resolveTargetPath("link/pgr"); code(err) != "target_path_outside_root" {
		t.Errorf("symlinked parent: %v", err)
	}
}
