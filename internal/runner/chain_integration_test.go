//go:build integration

// Incremental chain round-trips per engine (Constitution VI, SC-001, SC-002): full + 3 incrementals
// with ≤ 5% of rows changed between links, restore of the last link, and the fallbacks that must
// end in a full instead of a broken chain.
package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/delvivo/dumpling/internal/engine"
	"github.com/delvivo/dumpling/internal/store"
)

func chainIT(t *testing.T, ec *engineCase, databases []string) (*Runner, *store.Store, store.Plan) {
	t.Helper()
	r, st := newIT(t, ec)
	dest := store.Destination{Name: "d", Path: filepath.Join(r.DataDir, "dest")}
	if err := st.SaveDestination(&dest); err != nil {
		t.Fatal(err)
	}
	plan := store.Plan{Name: "inc-" + ec.name, ConnectionID: ec.a.ID, Databases: databases, DestinationID: dest.ID,
		Schedule: "0 2 * * 0", IncrSchedule: "0 * * * *", MaxChain: 30, Retention: store.Retention{Last: 50}}
	if err := st.SavePlan(&plan); err != nil {
		t.Fatal(err)
	}
	return r, st, plan
}

// runChain runs the plan once and returns the operation and the backup it produced (if any).
func runChain(t *testing.T, r *Runner, st *store.Store, p store.Plan, kind string) (store.Operation, store.Backup, string) {
	t.Helper()
	ids, err := r.RunPlan(p, kind)
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()
	o, _ := st.Operation(ids[0])
	log, _ := os.ReadFile(LogPath(r.DataDir, o.ID))
	if o.Status == "failed" {
		t.Fatalf("%s run failed: %s\n%s", kind, o.Message, log)
	}
	var b store.Backup
	if o.BackupID != "" {
		b, _ = st.Backup(o.BackupID)
	}
	time.Sleep(1100 * time.Millisecond) // distinct started_at seconds
	return o, b, string(log)
}

// chainLinks takes a full and three incrementals, changing ~1.7% of rows before each, and checks
// each incremental stays ≤ 20% of the full (SC-001).
func chainLinks(t *testing.T, r *Runner, st *store.Store, p store.Plan, change func(i int)) (full store.Backup, last store.Backup) {
	t.Helper()
	start := time.Now()
	_, full, _ = runChain(t, r, st, p, "full")
	fullTook := time.Since(start)
	if full.Kind != "full" || full.ChainID != full.ID {
		t.Fatalf("full: %+v", full)
	}
	for i := 1; i <= 3; i++ {
		change(i)
		start = time.Now()
		_, b, log := runChain(t, r, st, p, "incremental")
		if b.Kind != "incr" || b.ChainID != full.ID || b.Seq != i {
			t.Fatalf("incremental %d: %+v\n%s", i, b, log)
		}
		if b.SizeBytes > full.SizeBytes/5 {
			t.Fatalf("SC-001: incremental %d is %d bytes, full %d", i, b.SizeBytes, full.SizeBytes)
		}
		t.Logf("incremental %d: %d bytes (full %d), %s (full %s)", i, b.SizeBytes, full.SizeBytes, time.Since(start).Round(time.Millisecond), fullTook.Round(time.Millisecond))
		last = b
	}
	return full, last
}

func TestChainMSSQL(t *testing.T) {
	ec := cases(t)[2]
	r, st, p := chainIT(t, &ec, []string{"shop"})
	execSQL(t, ec.a, ec.admin, ec.drop("shop"))
	execSQL(t, ec.a, ec.admin, "CREATE DATABASE shop;")
	execSQL(t, ec.a, "shop", ec.seed)
	execSQL(t, ec.b, ec.admin, ec.drop("shop_inc"))
	// localized changes (SC-001: differentials track changed 64 KiB extents, so updates spread over
	// every page would make each differential almost as large as the full)
	change := func(i int) {
		execSQL(t, ec.a, "shop", "INSERT INTO orders(note) SELECT TOP 5000 'inc' FROM sys.all_objects a CROSS JOIN sys.all_objects b; UPDATE items SET note='u' WHERE id BETWEEN "+itoa(i*10000)+" AND "+itoa(i*10000+2999)+";")
	}
	full, last := chainLinks(t, r, st, p, change)
	if last.ParentID != full.ID {
		t.Fatalf("mssql differential must apply on the full: parent %s", last.ParentID)
	}
	want := execSQL(t, ec.a, "shop", "SELECT COUNT(*) FROM orders;")
	restore(t, r, st, last, ec.b, "shop_inc", "", "")
	if got := execSQL(t, ec.b, "shop_inc", "SELECT COUNT(*) FROM orders;"); got != want {
		t.Fatalf("orders=%s want %s", got, want)
	}
	if got := execSQL(t, ec.b, "shop_inc", "SELECT COUNT(*) FROM items WHERE note='u';"); got != "9000" {
		t.Fatalf("updated items=%s want 9000", got)
	}

	// a full taken outside Dumpling replaces the differential base → next run is a full
	execSQL(t, ec.a, ec.admin, "BACKUP DATABASE shop TO DISK = '/var/opt/mssql/share/external.bak' WITH INIT;")
	defer os.Remove(filepath.Join(ec.a.SharePathLocal, "external.bak"))
	_, b, log := runChain(t, r, st, p, "incremental")
	if b.Kind != "full" || !strings.Contains(log, "differential base changed outside Dumpling") {
		t.Fatalf("external full: %+v\n%s", b, log)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestChainMySQL(t *testing.T) {
	ec := cases(t)[1]
	r, st, p := chainIT(t, &ec, []string{"shop"})
	execSQL(t, ec.a, "", ec.drop("shop")+ec.drop("shop_inc2"))
	execSQL(t, ec.a, "", "CREATE DATABASE shop;")
	execSQL(t, ec.a, "shop", ec.seed)
	execSQL(t, ec.b, "", ec.drop("shop_inc"))
	change := func(i int) {
		lo := i * 10000
		execSQL(t, ec.a, "shop", "SET SESSION cte_max_recursion_depth = 10000;"+
			"INSERT INTO orders(note) WITH RECURSIVE g(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM g WHERE n < 5000) SELECT 'inc' FROM g;"+
			"UPDATE items SET note='u' WHERE id BETWEEN "+itoa(lo)+" AND "+itoa(lo+2999)+";"+
			"DELETE FROM items WHERE id BETWEEN "+itoa(lo+5000)+" AND "+itoa(lo+5099)+";")
	}
	_, last := chainLinks(t, r, st, p, change)
	want := execSQL(t, ec.a, "shop", "SELECT COUNT(*), SUM(note='u') FROM items; SELECT COUNT(*) FROM orders;")
	if !strings.HasPrefix(want, "299700\t9000") {
		t.Fatalf("source state %q", want)
	}
	// another server, and the source server itself (GTIDs already executed there must not be skipped)
	for _, tc := range []struct {
		target store.Connection
		db     string
	}{{ec.b, "shop_inc"}, {ec.a, "shop_inc2"}} {
		restore(t, r, st, last, tc.target, tc.db, "", "")
		if got := execSQL(t, tc.target, tc.db, "SELECT COUNT(*), SUM(note='u') FROM items; SELECT COUNT(*) FROM orders;"); got != want {
			t.Fatalf("%s/%s: got %q want %q", tc.target.Name, tc.db, got, want)
		}
	}

	// binary logs purged since the last link → full
	execSQL(t, ec.a, "", "FLUSH BINARY LOGS;")
	cur := strings.Fields(execSQL(t, ec.a, "", "SHOW BINARY LOG STATUS;"))[0]
	execSQL(t, ec.a, "", "PURGE BINARY LOGS TO '"+cur+"';")
	if _, b, log := runChain(t, r, st, p, "incremental"); b.Kind != "full" || !strings.Contains(log, "purged") {
		t.Fatalf("purged logs: %+v\n%s", b, log)
	}

	// long-running write before the full → postponed, no backup
	go runSQL(ec.a, "shop", "UPDATE items SET note=SLEEP(14) WHERE id=1;")
	time.Sleep(11500 * time.Millisecond)
	if o, b, _ := runChain(t, r, st, p, "full"); o.Status != "warning" || !strings.Contains(o.Message, "full postponed") || b.ID != "" {
		t.Fatalf("long write: %s %q %+v", o.Status, o.Message, b)
	}
	time.Sleep(4 * time.Second)

	// lock held after the pre-check → the dump is stopped within ~5 s, other writes go on
	go runSQL(ec.a, "shop", "LOCK TABLES items WRITE; SELECT SLEEP(20); UNLOCK TABLES;")
	time.Sleep(500 * time.Millisecond)
	start := time.Now()
	o, b, _ := runChain(t, r, st, p, "full")
	took := time.Since(start) - 1100*time.Millisecond
	if o.Status != "warning" || !strings.Contains(o.Message, "not acquired within") || b.ID != "" || took > 6*time.Second+time.Second {
		t.Fatalf("lock wait: %s %q took %s", o.Status, o.Message, took)
	}
	execSQL(t, ec.a, "shop", "INSERT INTO orders(note) VALUES ('after');") // writes resume
	time.Sleep(15 * time.Second)

	// connection now reaches another server → server identity changed → full
	execSQL(t, ec.b, "", ec.drop("shop")+"CREATE DATABASE shop;")
	execSQL(t, ec.b, "shop", "CREATE TABLE items (id int PRIMARY KEY, note varchar(50));")
	runChain(t, r, st, p, "full")
	moved := ec.a
	moved.Host, moved.Port = ec.b.Host, ec.b.Port
	if err := st.SaveConnection(&moved); err != nil {
		t.Fatal(err)
	}
	if _, b, log := runChain(t, r, st, p, "incremental"); b.Kind != "full" || !strings.Contains(log, "server identity changed") {
		t.Fatalf("moved server: %+v\n%s", b, log)
	}
}

func TestChainPostgres(t *testing.T) {
	ec := cases(t)[0]
	r, st, p := chainIT(t, &ec, []string{store.ClusterDB})
	r.RestoreDir = envOr("IT_PG_RESTORE_DIR", filepath.Join(r.DataDir, "restores"))
	os.MkdirAll(r.RestoreDir, 0o755)
	for _, db := range []string{"shop", "shop2"} {
		execSQL(t, ec.a, ec.admin, ec.drop(db))
		execSQL(t, ec.a, ec.admin, "CREATE DATABASE "+db+";")
	}
	execSQL(t, ec.a, "shop", ec.seed)
	execSQL(t, ec.a, "shop2", "CREATE TABLE notes (id serial PRIMARY KEY, body text);")
	// let the bulk load's maintenance (autovacuum freezing every page) land before the chain starts:
	// otherwise it shows up, legitimately, as changed blocks in whichever incremental follows it
	execSQL(t, ec.a, "shop", "VACUUM (FREEZE, ANALYZE);")

	pg, _ := engine.For("postgres")
	if missing, _, err := pg.(engine.Incremental).Prereqs(r.ctx, r.EngineConn(ec.b), store.ClusterDB); err != nil || len(missing) == 0 || !strings.Contains(missing[0], "PostgreSQL 17") {
		t.Fatalf("pg-b (16) prerequisites: %v %v", missing, err)
	}

	change := func(i int) {
		execSQL(t, ec.a, "shop", "INSERT INTO orders(note) SELECT 'inc' FROM generate_series(1,5000); UPDATE items SET note='u' WHERE id BETWEEN "+
			itoa(i*10000)+" AND "+itoa(i*10000+2999)+";")
		execSQL(t, ec.a, "shop2", "INSERT INTO notes(body) VALUES ('link "+itoa(i)+"');")
	}
	_, last := chainLinks(t, r, st, p, change)
	if last.Database != store.ClusterDB || !last.Physical() {
		t.Fatalf("not a cluster backup: %+v", last)
	}

	name := "pgr-" + strings.ToLower(last.ID[len(last.ID)-6:])
	if _, err := r.StartRestore(RestoreReq{BackupID: last.ID, TargetPath: "../escape"}); code(err) != "target_path_outside_root" {
		t.Fatalf("escape: %v", err)
	}
	id, err := r.StartRestore(RestoreReq{BackupID: last.ID, TargetPath: name, User: "it"})
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()
	o, _ := st.Operation(id)
	log, _ := os.ReadFile(LogPath(r.DataDir, o.ID))
	if o.Status != "success" {
		t.Fatalf("restore %s: %s\n%s", o.Status, o.Message, log)
	}
	out := filepath.Join(r.RestoreDir, name)
	// the rebuilt directory matches its own manifest (every file checksummed)
	verify := "/usr/lib/postgresql/17/bin/pg_verifybackup"
	if b, err := exec.Command(verify, "-n", out).CombinedOutput(); err != nil {
		t.Fatalf("pg_verifybackup: %v\n%s", err, b)
	}
	t.Logf("restored cluster in %s (expect shop: %s orders, shop2: 3 notes)", out, execSQL(t, ec.a, "shop", "SELECT count(*) FROM orders;"))
}
