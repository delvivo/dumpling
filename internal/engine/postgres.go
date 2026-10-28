package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type postgres struct{}

// ponytail: maintenance database fixed to "postgres"; make it configurable if someone needs another.
const pgMaintenanceDB = "postgres"

func pgEnv(c Conn) []string {
	mode := c.TLSMode
	if mode == "" {
		mode = "prefer"
	}
	return []string{
		"PGHOST=" + c.Host, "PGPORT=" + strconv.Itoa(c.Port), "PGUSER=" + c.Username,
		"PGPASSWORD=" + c.Password, "PGSSLMODE=" + mode, "PGCONNECT_TIMEOUT=10", "PGAPPNAME=dumpling",
	}
}

// pgConninfo quotes a database name as a libpq conninfo value, so names containing '=' are not
// mistaken for connection strings.
func pgConninfo(db string) string {
	return "dbname='" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(db) + "'"
}

// psqlArgs runs SQL from stdin with psql variables (:'db' / :"db" quote safely) and stops on error.
func psqlArgs(db string, vars map[string]string) []string {
	args := []string{"-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1", "--dbname=" + pgConninfo(db)}
	for k, v := range vars {
		args = append(args, "-v", k+"="+v)
	}
	return args
}

func (postgres) sql(ctx context.Context, c Conn, query string, vars map[string]string) (string, error) {
	return output(ctx, "psql", psqlArgs(pgMaintenanceDB, vars), pgEnv(c), strings.NewReader(query))
}

func (p postgres) Test(ctx context.Context, c Conn) (TestResult, error) {
	r := TestResult{Databases: []string{}, Warnings: []string{}}
	out, err := p.sql(ctx, c, "SHOW server_version;\nSELECT datname FROM pg_database WHERE NOT datistemplate ORDER BY 1;\n", nil)
	if err != nil {
		return r, err
	}
	l := lines(out)
	if len(l) == 0 {
		return r, fmt.Errorf("psql: unexpected empty output")
	}
	r.Version, r.Databases = strings.Fields(l[0])[0], l[1:]
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if t := detect(tool); t == nil {
			r.Warnings = append(r.Warnings, tool+" not found on PATH")
		} else if Major(t.Version) < Major(r.Version) {
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s %s is older than server %s; install client ≥ %d", tool, t.Version, r.Version, Major(r.Version)))
		}
	}
	return r, nil
}

// pgDumpArgs: custom format (compressed, restorable under any name); one repeatable-read snapshot.
func pgDumpArgs(db string) []string { return []string{"-Fc", "--dbname=" + pgConninfo(db)} }

func (postgres) Backup(ctx context.Context, c Conn, db string, w io.Writer, log io.Writer) ([]string, error) {
	return nil, run(ctx, "pg_dump", pgDumpArgs(db), pgEnv(c), nil, w, log)
}

func (p postgres) Exists(ctx context.Context, c Conn, db string) (bool, error) {
	out, err := p.sql(ctx, c, "SELECT 1 FROM pg_database WHERE datname = :'db';\n", map[string]string{"db": db})
	return out == "1", err
}

// pgRestoreArgs: pg_restore emits the SQL script on stdout; psql loads it in one transaction.
func pgRestoreArgs(o RestoreOpts) []string {
	args := []string{"-f", "-"}
	if o.SkipOwnership {
		args = append(args, "--no-owner", "--no-acl")
	}
	return args
}

// pgDropTimeout removes the header `SET transaction_timeout = 0;` that pg_dump ≥ 17 always emits
// and servers < 17 reject. Only the first occurrence: later identical lines may be data.
func pgDropTimeout(r io.Reader) io.Reader {
	done := false
	return mapLines(r, func(line string) string {
		if !done && line == "SET transaction_timeout = 0;\n" {
			done = true
			return ""
		}
		return line
	})
}

// pgHint points at "Skip ownership" when the target lacks the roles the dump assigns objects to.
func pgHint(err error, o RestoreOpts) error {
	if msg := err.Error(); !o.SkipOwnership && strings.Contains(msg, `role "`) && strings.Contains(msg, "does not exist") {
		return fmt.Errorf(`%w; the target lacks the dump's roles: retry with "Skip ownership"`, err)
	}
	return err
}

func (p postgres) Restore(ctx context.Context, c Conn, db string, overwrite bool, r io.Reader, o RestoreOpts, log io.Writer) error {
	vars := map[string]string{"db": db}
	num, err := p.sql(ctx, c, "SHOW server_version_num;\n", nil)
	if err != nil {
		return err
	}
	n, _ := strconv.Atoi(num)
	drop := `DROP DATABASE :"db" WITH (FORCE);`
	if n < 130000 {
		drop = "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = :'db' AND pid <> pg_backend_pid();\n" +
			`DROP DATABASE :"db";`
	}
	if overwrite {
		fmt.Fprintf(log, "terminating connections and dropping database %s\n", db)
		if _, err := p.sql(ctx, c, drop+"\n", vars); err != nil {
			return err
		}
	}
	if _, err := p.sql(ctx, c, "CREATE DATABASE :\"db\";\n", vars); err != nil {
		return err
	}
	fmt.Fprintf(log, "created database %s, running pg_restore\n", db)
	if err := p.load(ctx, c, db, r, o, n < 170000, log); err != nil {
		if _, derr := p.sql(ctx, c, drop+"\n", vars); derr != nil {
			fmt.Fprintf(log, "could not drop database %s after failed restore: %v\n", db, derr)
		} else {
			fmt.Fprintf(log, "dropped database %s after failed restore\n", db)
		}
		return pgHint(err, o)
	}
	return nil
}

// load pipes `pg_restore -f -` into `psql --single-transaction`: a failure leaves the database empty.
func (postgres) load(ctx context.Context, c Conn, db string, r io.Reader, o RestoreOpts, pre17 bool, log io.Writer) error {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := run(ctx, "pg_restore", pgRestoreArgs(o), pgEnv(c), r, pw, log)
		pw.CloseWithError(err)
		done <- err
	}()
	var script io.Reader = pr
	if pre17 {
		script = pgDropTimeout(pr)
	}
	err := run(ctx, "psql", append(psqlArgs(db, nil), "--single-transaction"), pgEnv(c), script, io.Discard, log)
	pr.CloseWithError(errors.New("psql exited")) // unblock pg_restore if psql stopped early
	if rerr := <-done; err == nil {
		err = rerr // psql's error is the server-side cause; pg_restore's only matters otherwise
	}
	return err
}
