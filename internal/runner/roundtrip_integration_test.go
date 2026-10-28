//go:build integration

// Round-trip test per engine (Constitution VI, SC-003, SC-004): seed → backup under write load →
// restore into another instance under a new name → compare → restore again with overwrite.
// Needs testdata/compose.yaml services and client tools; run via `docker compose ... run --rm it`.
package runner

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/delvivo/dumpling/internal/secret"
	"github.com/delvivo/dumpling/internal/storage"
	"github.com/delvivo/dumpling/internal/store"
)

const itPassword = "Dumpling-Test-1"

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func hostPort(t *testing.T, addr string) (string, int) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(p)
	return h, n
}

// execSQL runs SQL and fails the test on error.
func execSQL(t *testing.T, c store.Connection, db, q string) string {
	t.Helper()
	out, err := runSQL(c, db, q)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// runSQL runs SQL against a database with the engine's own client (or driver for mssql).
func runSQL(c store.Connection, db, q string) (string, error) {
	var out bytes.Buffer
	var errb bytes.Buffer
	switch c.Engine {
	case "postgres":
		cmd := exec.Command("psql", "-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-d", db)
		cmd.Env = append(os.Environ(), "PGHOST="+c.Host, "PGPORT="+strconv.Itoa(c.Port), "PGUSER="+c.Username, "PGPASSWORD="+c.Password)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(q), &out, &errb
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("psql: %v %s", err, errb.String())
		}
	case "mysql":
		args := []string{"-h", c.Host, "-P", strconv.Itoa(c.Port), "-u", c.Username, "-N", "-B"}
		if db != "" {
			args = append(args, db)
		}
		cmd := exec.Command("mysql", args...)
		cmd.Env = append(os.Environ(), "MYSQL_PWD="+c.Password) // test only
		cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(q), &out, &errb
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("mysql: %v %s", err, errb.String())
		}
	case "mssql":
		u := url.URL{Scheme: "sqlserver", User: url.UserPassword(c.Username, c.Password), Host: fmt.Sprintf("%s:%d", c.Host, c.Port),
			RawQuery: url.Values{"database": {db}, "encrypt": {"true"}, "TrustServerCertificate": {"true"}}.Encode()}
		d, err := sql.Open("sqlserver", u.String())
		if err != nil {
			return "", err
		}
		defer d.Close()
		rows, err := d.Query(q)
		if err != nil {
			return "", fmt.Errorf("mssql: %v (%s)", err, q)
		}
		defer rows.Close()
		for {
			for rows.Next() {
				var v any
				rows.Scan(&v)
				fmt.Fprintln(&out, v)
			}
			if !rows.NextResultSet() {
				break
			}
		}
	}
	return strings.TrimSpace(out.String()), nil
}

type engineCase struct {
	name     string
	a, b     store.Connection
	admin    string // maintenance db
	seed     string
	drop     func(db string) string
	insert   string
	countSQL func(table string) string
}

func cases(t *testing.T) []engineCase {
	conn := func(engine, env, def, user string) store.Connection {
		h, p := hostPort(t, envOr(env, def))
		c := store.Connection{Name: env, Engine: engine, Host: h, Port: p, Username: user, Password: itPassword}
		if engine == "mssql" {
			c.TLSMode = "trust"
			c.SharePathServer = "/var/opt/mssql/share"
			share, _ := filepath.Abs(envOr("IT_MSSQL_SHARE_LOCAL", "../../testdata/mssql-share"))
			c.SharePathLocal = share
		}
		if engine == "mysql" {
			c.TLSMode = "PREFERRED"
		}
		return c
	}
	count := func(tbl string) string { return "SELECT COUNT(*) FROM " + tbl + ";" }
	return []engineCase{
		{
			name: "postgres", a: conn("postgres", "IT_PG_A", "localhost:15432", "postgres"), b: conn("postgres", "IT_PG_B", "localhost:15433", "postgres"),
			admin: "postgres",
			seed: `CREATE TABLE items (id int PRIMARY KEY, note varchar(50));
INSERT INTO items SELECT g, md5(g::text) FROM generate_series(1,300000) g;
CREATE TABLE orders (id serial PRIMARY KEY, note varchar(50));
CREATE VIEW item_view AS SELECT id FROM items;`,
			drop:     func(db string) string { return `DROP DATABASE IF EXISTS "` + db + `" WITH (FORCE);` },
			insert:   "INSERT INTO orders(note) VALUES ('load');",
			countSQL: count,
		},
		{
			name: "mysql", a: conn("mysql", "IT_MY_A", "localhost:13306", "root"), b: conn("mysql", "IT_MY_B", "localhost:13307", "root"),
			seed: `SET SESSION cte_max_recursion_depth = 400000;
CREATE TABLE items (id int PRIMARY KEY, note varchar(50)) ENGINE=InnoDB;
INSERT INTO items WITH RECURSIVE g(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM g WHERE n < 300000) SELECT n, md5(n) FROM g;
CREATE TABLE orders (id int AUTO_INCREMENT PRIMARY KEY, note varchar(50)) ENGINE=InnoDB;
CREATE VIEW item_view AS SELECT id FROM items;`,
			drop:     func(db string) string { return "DROP DATABASE IF EXISTS `" + db + "`;" },
			insert:   "INSERT INTO orders(note) VALUES ('load');",
			countSQL: count,
		},
		{
			name: "mssql", a: conn("mssql", "IT_MS_A", "localhost:11433", "sa"), b: conn("mssql", "IT_MS_B", "localhost:11434", "sa"),
			admin: "master",
			seed: `CREATE TABLE items (id int PRIMARY KEY, note varchar(50));
INSERT INTO items SELECT TOP 300000 ROW_NUMBER() OVER (ORDER BY (SELECT NULL)), 'x' FROM sys.all_objects a CROSS JOIN sys.all_objects b;
CREATE TABLE orders (id int IDENTITY PRIMARY KEY, note varchar(50));`,
			drop: func(db string) string {
				return fmt.Sprintf("IF DB_ID('%s') IS NOT NULL BEGIN ALTER DATABASE [%s] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE [%s]; END", db, db, db)
			},
			insert:   "INSERT INTO orders(note) VALUES ('load');",
			countSQL: count,
		},
	}
}

func TestRoundTrip(t *testing.T) {
	for _, ec := range cases(t) {
		t.Run(ec.name, func(t *testing.T) {
			t.Run("local", func(t *testing.T) { roundTrip(t, ec) })
			for name, d := range remoteDests(t) {
				t.Run(name, func(t *testing.T) { remoteRoundTrip(t, ec, d) })
			}
		})
	}
}

// remoteDests are the S3/SFTP test services from testdata/compose.yaml (env set by the `it` service).
func remoteDests(t *testing.T) map[string]store.Destination {
	out := map[string]store.Destination{}
	if ep := os.Getenv("DUMPLING_TEST_S3_ENDPOINT"); ep != "" {
		out["s3"] = store.Destination{Name: "s3", Type: "s3", S3: &store.S3Config{Endpoint: ep, Region: "us-east-1",
			Bucket: os.Getenv("DUMPLING_TEST_S3_BUCKET"), Prefix: "rt-" + store.NewID(), PathStyle: true,
			AccessKeyID: os.Getenv("DUMPLING_TEST_S3_KEY"), SecretAccessKey: os.Getenv("DUMPLING_TEST_S3_SECRET")}}
	}
	if addr := os.Getenv("DUMPLING_TEST_SFTP_ADDR"); addr != "" {
		h, p := hostPort(t, addr)
		fp, err := storage.ProbeHostKey(context.Background(), h, p)
		if err != nil {
			t.Fatal(err)
		}
		out["sftp"] = store.Destination{Name: "sftp", Type: "sftp", SFTP: &store.SFTPConfig{Host: h, Port: p, Auth: "password",
			Username: os.Getenv("DUMPLING_TEST_SFTP_USER"), Password: os.Getenv("DUMPLING_TEST_SFTP_PASSWORD"),
			RemoteDir: os.Getenv("DUMPLING_TEST_SFTP_DIR") + "/rt-" + store.NewID(), HostKeyFingerprint: fp}}
	}
	return out
}

func newIT(t *testing.T, ec *engineCase) (*Runner, *store.Store) {
	dir := t.TempDir()
	for _, d := range []string{"logs", "tmp", "dest"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	box, _ := secret.New(bytes.Repeat([]byte{1}, 32))
	st, err := store.Open(filepath.Join(dir, "db.sqlite"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB.Close() })
	for _, c := range []*store.Connection{&ec.a, &ec.b} {
		c.ID = ""
		if err := st.SaveConnection(c); err != nil {
			t.Fatal(err)
		}
	}
	return New(context.Background(), st, dir, nil), st
}

// remoteRoundTrip: backup to S3/SFTP, restore (plain and encrypted), verify outcomes, retention,
// and rescan into a fresh install (SC-002, FR-009–FR-011, FR-016).
func remoteRoundTrip(t *testing.T, ec engineCase, dest store.Destination) {
	r, st := newIT(t, &ec)
	if err := st.SaveDestination(&dest); err != nil {
		t.Fatal(err)
	}
	execSQL(t, ec.a, ec.admin, ec.drop("shop"))
	execSQL(t, ec.a, ec.admin, "CREATE DATABASE shop;")
	execSQL(t, ec.a, "shop", ec.seed)
	for _, db := range []string{"shop_r", "shop_re"} {
		execSQL(t, ec.b, ec.admin, ec.drop(db))
	}
	n := atoi(execSQL(t, ec.a, "shop", ec.countSQL("orders")))

	b := backupNow(t, r, st, ec.a, dest, "")
	restore(t, r, st, b, ec.b, "shop_r", "", "")
	check(t, ec, "shop_r", n, n)
	be := backupNow(t, r, st, ec.a, dest, "it-passphrase")
	restore(t, r, st, be, ec.b, "shop_re", "", "it-passphrase")
	check(t, ec, "shop_re", n, n)

	// verify: ok, checksum mismatch (object replaced), missing (object deleted)
	verify := func(want string) {
		t.Helper()
		id, err := r.StartVerify(b.ID)
		if err != nil {
			t.Fatal(err)
		}
		r.Wait()
		if o, _ := st.Operation(id); !strings.Contains(o.Message, want) {
			t.Fatalf("verify: want %q, got %s %q", want, o.Status, o.Message)
		}
	}
	verify("checksum ok")
	sb, err := storage.Open(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()
	sb.Remove(b.Path)
	if err := storage.PutBytes(sb, b.Path, []byte("tampered")); err != nil {
		t.Fatal(err)
	}
	verify("checksum mismatch")
	sb.Remove(b.Path)
	verify("artifact missing")

	// retention: keep last 2 of 3 plan runs, remotely and in history
	plan := store.Plan{Name: "keep2", ConnectionID: ec.a.ID, Databases: []string{"shop"}, DestinationID: dest.ID,
		Schedule: "0 2 * * *", Retention: store.Retention{Last: 2}}
	if err := st.SavePlan(&plan); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r.RunPlan(plan, "full")
		r.Wait()
		time.Sleep(1100 * time.Millisecond) // distinct started_at seconds
	}
	kept, _ := st.Backups(store.BackupFilter{PlanID: plan.ID, Status: "ok"})
	if len(kept) != 2 {
		t.Fatalf("retention kept %d in history", len(kept))
	}
	for _, k := range kept {
		if _, err := sb.Stat(k.Path); err != nil {
			t.Fatalf("kept backup missing remotely: %v", err)
		}
	}
	all, _ := st.Backups(store.BackupFilter{PlanID: plan.ID, Status: "deleted"})
	for _, d := range all {
		if _, err := sb.Stat(d.Path); err == nil {
			t.Fatalf("pruned backup still on destination: %s", d.Path)
		}
	}

	// disaster recovery: a fresh install imports what is on the destination and restores it
	r2, st2 := newIT(t, &ec)
	d2 := dest
	d2.ID = ""
	if err := st2.SaveDestination(&d2); err != nil {
		t.Fatal(err)
	}
	res, err := r2.Rescan(d2)
	if err != nil || res.Imported != 3 { // encrypted one + 2 kept plan backups
		t.Fatalf("rescan %+v %v", res, err)
	}
	imp, _ := st2.Backups(store.BackupFilter{Status: "ok"})
	for _, ib := range imp {
		if ib.Locked {
			if _, err := r2.Unlock(ib.ID, "it-passphrase", false); err != nil {
				t.Fatal(err)
			}
			execSQL(t, ec.b, ec.admin, ec.drop("shop_re"))
			restore(t, r2, st2, ib, ec.b, "shop_re", "", "")
			check(t, ec, "shop_re", n, n)
		}
	}
}

func roundTrip(t *testing.T, ec engineCase) {
	dir := t.TempDir()
	for _, d := range []string{"logs", "tmp", "dest"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	box, _ := secret.New(bytes.Repeat([]byte{1}, 32))
	st, err := store.Open(filepath.Join(dir, "db.sqlite"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	r := New(context.Background(), st, dir, nil)
	for _, c := range []*store.Connection{&ec.a, &ec.b} {
		if err := st.SaveConnection(c); err != nil {
			t.Fatal(err)
		}
	}
	dest := store.Destination{Name: "d", Path: filepath.Join(dir, "dest")}
	st.SaveDestination(&dest)

	// Seed source.
	execSQL(t, ec.a, ec.admin, ec.drop("shop"))
	execSQL(t, ec.a, ec.admin, "CREATE DATABASE shop;")
	execSQL(t, ec.a, "shop", ec.seed)
	for _, db := range []string{"shop_copy", "shop_enc"} {
		execSQL(t, ec.b, ec.admin, ec.drop(db))
	}

	// Backup under write load (SC-004).
	var writes, failed atomic.Int64
	stop := make(chan struct{})
	loadDone := make(chan struct{})
	go func() {
		defer close(loadDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := runSQL(ec.a, "shop", ec.insert); err != nil {
				failed.Add(1)
			}
			writes.Add(1)
		}
	}()
	time.Sleep(500 * time.Millisecond)
	before := atoi(execSQL(t, ec.a, "shop", ec.countSQL("orders")))
	b := backupNow(t, r, st, ec.a, dest, "")
	after := atoi(execSQL(t, ec.a, "shop", ec.countSQL("orders")))
	close(stop)
	<-loadDone
	if failed.Load() > 0 {
		t.Fatalf("SC-004: %d of %d writes failed during backup", failed.Load(), writes.Load())
	}
	t.Logf("%d writes during backup, 0 failed; backup %d bytes", writes.Load(), b.SizeBytes)

	// Restore to the other instance under a new name (SC-003).
	restore(t, r, st, b, ec.b, "shop_copy", "", "")
	check(t, ec, "shop_copy", before, after)

	// Restore again: target exists → confirmation required, then overwrite.
	if _, err := r.StartRestore(RestoreReq{BackupID: b.ID, TargetConnectionID: ec.b.ID, TargetDatabase: "shop_copy"}); code(err) != "confirmation_required" {
		t.Fatalf("expected confirmation_required, got %v", err)
	}
	execSQL(t, ec.b, "shop_copy", "CREATE TABLE stray (id int);")
	restore(t, r, st, b, ec.b, "shop_copy", "shop_copy", "")
	check(t, ec, "shop_copy", before, after)
	if n := execSQL(t, ec.b, "shop_copy", strayQuery(ec.name)); n != "0" {
		t.Fatalf("overwrite must drop+recreate: stray table still there (%s)", n)
	}

	// Encrypted variant.
	be := backupNow(t, r, st, ec.a, dest, "it-passphrase")
	if !strings.HasSuffix(be.Path, ".age") {
		t.Fatalf("encrypted artifact path %s", be.Path)
	}
	restore(t, r, st, be, ec.b, "shop_enc", "", "it-passphrase")
	if got := atoi(execSQL(t, ec.b, "shop_enc", ec.countSQL("items"))); got != 300000 {
		t.Fatalf("encrypted restore items=%d", got)
	}
}

func strayQuery(engine string) string {
	switch engine {
	case "postgres":
		return "SELECT count(*) FROM information_schema.tables WHERE table_name='stray';"
	case "mysql":
		return "SELECT count(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='stray';"
	}
	return "SELECT count(*) FROM sys.tables WHERE name='stray';"
}

func atoi(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

func backupNow(t *testing.T, r *Runner, st *store.Store, c store.Connection, dest store.Destination, pass string) store.Backup {
	t.Helper()
	ids, err := r.StartBackup(BackupReq{Conn: c, Databases: []string{"shop"}, Dest: dest, Passphrase: pass})
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()
	o, _ := st.Operation(ids[0])
	if o.Status != "success" {
		log, _ := os.ReadFile(LogPath(r.DataDir, o.ID))
		t.Fatalf("backup %s: %s\n%s", o.Status, o.Message, log)
	}
	b, _ := st.Backup(o.BackupID)
	return b
}

func restore(t *testing.T, r *Runner, st *store.Store, b store.Backup, target store.Connection, db, confirm, pass string) {
	t.Helper()
	id, err := r.StartRestore(RestoreReq{BackupID: b.ID, TargetConnectionID: target.ID, TargetDatabase: db, ConfirmDatabase: confirm, Passphrase: pass, User: "it"})
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()
	o, _ := st.Operation(id)
	if o.Status != "success" {
		log, _ := os.ReadFile(LogPath(r.DataDir, o.ID))
		t.Fatalf("restore %s: %s\n%s", o.Status, o.Message, log)
	}
}

func check(t *testing.T, ec engineCase, db string, before, after int) {
	t.Helper()
	if got := atoi(execSQL(t, ec.b, db, ec.countSQL("items"))); got != 300000 {
		t.Fatalf("items=%d want 300000", got)
	}
	if got := atoi(execSQL(t, ec.b, db, ec.countSQL("orders"))); got < before || got > after {
		t.Fatalf("orders=%d outside snapshot window [%d,%d]", got, before, after)
	}
}
