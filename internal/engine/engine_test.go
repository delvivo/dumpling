package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var c = Conn{Engine: "postgres", Host: "db", Port: 5432, Username: "u", Password: "p@ss'\"w\\rd", TmpDir: ""}

func noSecret(t *testing.T, args []string) {
	t.Helper()
	for _, a := range args {
		if strings.Contains(a, c.Password) {
			t.Fatalf("password on argv: %q", a)
		}
	}
}

func TestPostgresArgs(t *testing.T) {
	args := pgDumpArgs("we=ird'db")
	noSecret(t, args)
	if args[0] != "-Fc" || args[1] != `--dbname=dbname='we=ird\'db'` {
		t.Fatalf("%q", args)
	}
	if a := pgRestoreArgs("x", RestoreOpts{SkipOwnership: true}); !strings.Contains(strings.Join(a, " "), "--no-owner --no-acl") {
		t.Fatalf("%q", a)
	}
	env := strings.Join(pgEnv(c), "\n")
	if !strings.Contains(env, "PGPASSWORD="+c.Password) || !strings.Contains(env, "PGSSLMODE=prefer") {
		t.Fatal(env)
	}
}

func TestMySQLArgs(t *testing.T) {
	args := mysqldumpArgs("--defaults-extra-file=/tmp/x.cnf", "shop")
	noSecret(t, args)
	for _, want := range []string{"--single-transaction", "--set-gtid-purged=OFF", "shop"} {
		if !strings.Contains(strings.Join(args, " "), want) {
			t.Fatalf("missing %s in %q", want, args)
		}
	}
	if strings.Contains(strings.Join(args, " "), "--databases") {
		t.Fatal("--databases would embed CREATE DATABASE")
	}
	f := defaultsFile(c)
	if !strings.Contains(f, `password="p@ss'\"w\\rd"`) {
		t.Fatalf("bad quoting: %s", f)
	}
	if myIdent("a`b") != "`a``b`" || myLit(`o'k\`) != `'o''k\\'` {
		t.Fatal("quoting")
	}
}

func TestStripDefiner(t *testing.T) {
	in := "CREATE TABLE t (id int);\n" +
		"/*!50013 DEFINER=`app`@`%` SQL SECURITY DEFINER */\n" +
		"CREATE DEFINER=`root`@`localhost` PROCEDURE p() BEGIN END;;\n" +
		"INSERT INTO t VALUES (1)"
	b, _ := io.ReadAll(stripDefiner(strings.NewReader(in)))
	out := string(b)
	if strings.Contains(out, "DEFINER=`") || !strings.Contains(out, "CREATE PROCEDURE p()") || !strings.HasSuffix(out, "VALUES (1)") {
		t.Fatalf("%s", out)
	}
}

func TestMSSQLHelpers(t *testing.T) {
	if msIdent("a]b") != "[a]]b]" {
		t.Fatal(msIdent("a]b"))
	}
	if serverPath(`D:\share\`, "x.bak") != `D:\share\x.bak` || serverPath("/var/opt/mssql/share", "x.bak") != "/var/opt/mssql/share/x.bak" {
		t.Fatal("serverPath")
	}
	if dsn := mssqlDSN(Conn{Host: "h", Port: 1433, Username: "sa", Password: "p/w", TLSMode: "trust"}); !strings.Contains(dsn, "TrustServerCertificate=true") {
		t.Fatal(dsn)
	}
}

func TestMajor(t *testing.T) {
	for v, want := range map[string]int{"17.2 (Debian 17.2-1)": 17, "8.4.3": 8, "16.0.4135.4": 16, "": 0} {
		if Major(v) != want {
			t.Fatalf("%q → %d", v, Major(v))
		}
	}
}

// TestHelperProcess is not a real test: run() re-executes the test binary as a fake tool.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("DUMPLING_HELPER") != "1" {
		return
	}
	fmt.Fprintf(os.Stderr, "%s: error: boom\n", filepath.Base(os.Args[0]))
	os.Exit(2)
}

func TestRunErrorHasSinglePrefix(t *testing.T) {
	t.Setenv("DUMPLING_HELPER", "1")
	err := run(context.Background(), os.Args[0], []string{"-test.run=TestHelperProcess"}, nil, nil, io.Discard, nil)
	name := filepath.Base(os.Args[0])
	if err == nil || err.Error() != name+": error: boom" {
		t.Fatalf("got %q", err)
	}
}
