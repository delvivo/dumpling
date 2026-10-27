package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/delvivo/dumpling/internal/store"
)

// produce runs real backups on e (sidecars written by the normal path) and returns them.
func produce(t *testing.T, e *env, plan *store.Plan, pass string, n int) []store.Backup {
	t.Helper()
	for i := 0; i < n; i++ {
		e.r.StartBackup(BackupReq{Conn: e.conn, Databases: []string{"shop"}, Dest: e.dest, Plan: plan, Passphrase: pass})
		e.r.Wait()
	}
	bs, _ := e.st.Backups(store.BackupFilter{})
	return bs
}

// fresh returns a second install whose destination points at e's backup directory.
func fresh(t *testing.T, e *env) *env {
	f := setup(t)
	d := f.dest
	d.Path = e.dest.Path
	if err := f.st.SaveDestination(&d); err != nil {
		t.Fatal(err)
	}
	f.dest = d
	return f
}

func reasons(res RescanResult) map[string]int {
	m := map[string]int{}
	for _, s := range res.Skipped {
		m[s.Reason]++
	}
	return m
}

func TestRescanImport(t *testing.T) {
	e := setup(t)
	e.fake.exists = false
	produce(t, e, nil, "", 3)

	// own sidecars are skipped (known), nothing imported
	if res, err := e.r.Rescan(e.dest); err != nil || res.Imported != 0 || reasons(res)["known"] != 3 {
		t.Fatalf("own rescan %+v %v", res, err)
	}

	f := fresh(t, e)
	bs, _ := e.st.Backups(store.BackupFilter{})
	// break three ways: missing artifact, truncated artifact, garbage sidecar
	os.Remove(filepath.Join(e.dest.Path, bs[0].Path))
	os.WriteFile(filepath.Join(e.dest.Path, bs[1].Path), []byte("x"), 0o640)
	os.WriteFile(filepath.Join(e.dest.Path, "junk.dump.json"), []byte("{not json"), 0o640)

	res, err := f.r.Rescan(f.dest)
	if err != nil {
		t.Fatal(err)
	}
	r := reasons(res)
	if res.Imported != 1 || r["no_artifact"] != 1 || r["size_mismatch"] != 1 || r["bad_metadata"] != 1 {
		t.Fatalf("rescan %+v", res)
	}
	imp, _ := f.st.Backups(store.BackupFilter{})
	if len(imp) != 1 || !imp[0].Imported || imp[0].PlanID != "" || imp[0].Locked || imp[0].Path != bs[2].Path {
		t.Fatalf("imported %+v", imp)
	}

	// idempotent
	if res, _ := f.r.Rescan(f.dest); res.Imported != 0 || reasons(res)["known"] != 1 {
		t.Fatalf("second rescan %+v", res)
	}

	// imported backup restores like a native one
	if _, err := f.r.StartRestore(RestoreReq{BackupID: imp[0].ID, TargetConnectionID: f.conn.ID, TargetDatabase: "t"}); err != nil {
		t.Fatal(err)
	}
	f.r.Wait()
	if len(f.fake.restored) != 1 || f.fake.restored[0] != "t:dump data" {
		t.Fatalf("restored %q", f.fake.restored)
	}
}

func TestUnlockAndRetention(t *testing.T) {
	e := setup(t)
	plan := store.Plan{Name: "enc", ConnectionID: e.conn.ID, Databases: []string{"shop"}, DestinationID: e.dest.ID,
		Schedule: "0 2 * * *", Retention: store.Retention{Last: 10}}
	e.st.SavePlan(&plan)
	produce(t, e, &plan, "pp", 2)
	other := store.Plan{Name: "other", ConnectionID: e.conn.ID, Databases: []string{"shop"}, DestinationID: e.dest.ID,
		Schedule: "0 2 * * *", Retention: store.Retention{Last: 10}}
	e.st.SavePlan(&other)
	produce(t, e, &other, "zz", 1)

	f := fresh(t, e)
	if res, err := f.r.Rescan(f.dest); err != nil || res.Imported != 3 {
		t.Fatalf("rescan %+v %v", res, err)
	}
	imp, _ := f.st.Backups(store.BackupFilter{})
	var first store.Backup
	for _, b := range imp {
		if !b.Locked {
			t.Fatalf("encrypted import must be locked: %+v", b)
		}
		if b.OriginPlanID == plan.ID {
			first = b
		}
	}
	if _, err := f.r.StartRestore(RestoreReq{BackupID: first.ID, TargetConnectionID: f.conn.ID, TargetDatabase: "t"}); code(err) != "backup_locked" {
		t.Fatalf("locked restore: %v", err)
	}
	if _, err := f.r.Unlock(first.ID, "wrong", true); code(err) != "wrong_passphrase" {
		t.Fatalf("wrong: %v", err)
	}
	if b, _ := f.st.Backup(first.ID); !b.Locked {
		t.Fatal("wrong passphrase must keep it locked")
	}
	ids, err := f.r.Unlock(first.ID, "pp", true)
	if err != nil || len(ids) != 2 {
		t.Fatalf("unlock same origin %v %v", ids, err)
	}
	for _, b := range imp {
		got, _ := f.st.Backup(b.ID)
		if got.Locked != (b.OriginPlanID == other.ID) {
			t.Fatalf("lock state of %s (%s): %v", b.ID, b.OriginPlanID, got.Locked)
		}
	}
	if _, err := f.r.StartRestore(RestoreReq{BackupID: first.ID, TargetConnectionID: f.conn.ID, TargetDatabase: "t"}); err != nil {
		t.Fatal(err)
	}
	f.r.Wait()
	if len(f.fake.restored) != 1 {
		t.Fatalf("restore after unlock: %q", f.fake.restored)
	}

	// plan retention on the new install never prunes imported backups
	p2 := store.Plan{Name: "keep1", ConnectionID: f.conn.ID, Databases: []string{"shop"}, DestinationID: f.dest.ID,
		Schedule: "0 2 * * *", Retention: store.Retention{Last: 1}}
	f.st.SavePlan(&p2)
	produce(t, f, &p2, "", 2)
	for _, b := range imp {
		if got, _ := f.st.Backup(b.ID); got.Status != "ok" {
			t.Fatalf("imported backup pruned: %+v", got)
		}
	}
}
