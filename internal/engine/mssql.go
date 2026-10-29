package engine

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "github.com/microsoft/go-mssqldb"
)

// mssql uses native BACKUP/RESTORE DATABASE (COPY_ONLY) through a directory shared between
// SQL Server (SharePathServer) and this app (SharePathLocal).
type mssql struct{}

func mssqlDSN(c Conn) string {
	q := url.Values{"database": {"master"}, "app name": {"dumpling"}, "dial timeout": {"10"}}
	switch c.TLSMode {
	case "disable":
		q.Set("encrypt", "disable")
	case "trust":
		q.Set("encrypt", "true")
		q.Set("TrustServerCertificate", "true")
	default:
		q.Set("encrypt", "true")
	}
	u := url.URL{Scheme: "sqlserver", User: url.UserPassword(c.Username, c.Password),
		Host: c.Host + ":" + strconv.Itoa(c.Port), RawQuery: q.Encode()}
	return u.String()
}

// conn opens a single session (RESTORE after ALTER DATABASE must share it).
func (mssql) conn(ctx context.Context, c Conn) (*sql.Conn, func(), error) {
	db, err := sql.Open("sqlserver", mssqlDSN(c))
	if err != nil {
		return nil, nil, err
	}
	cn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, nil, redactErr(err, c.Password)
	}
	return cn, func() { cn.Close(); db.Close() }, nil
}

func redactErr(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), secret, "***"))
}

func msIdent(s string) string { return "[" + strings.ReplaceAll(s, "]", "]]") + "]" }

// serverPath joins a file name onto the server-side share using the server's separator.
func serverPath(share, name string) string {
	sep := "/"
	if strings.Contains(share, `\`) {
		sep = `\`
	}
	return strings.TrimRight(share, `/\`) + sep + name
}

func (m mssql) Test(ctx context.Context, c Conn) (TestResult, error) {
	r := TestResult{Databases: []string{}, Warnings: []string{}}
	cn, done, err := m.conn(ctx, c)
	if err != nil {
		return r, err
	}
	defer done()
	if err := cn.QueryRowContext(ctx, `SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128))`).Scan(&r.Version); err != nil {
		return r, err
	}
	rows, err := cn.QueryContext(ctx, `SELECT name FROM sys.databases WHERE database_id > 4 ORDER BY name`)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		r.Databases = append(r.Databases, n)
	}
	rows.Close()

	// Probe the share: SQL Server writes a tiny backup, the app must be able to read it.
	name := "dumpling-probe-" + c.OpID + ".bak"
	if _, err := cn.ExecContext(ctx, `BACKUP DATABASE [model] TO DISK = @p1 WITH COPY_ONLY, INIT`, serverPath(c.SharePathServer, name)); err != nil {
		return r, fmt.Errorf("share probe: SQL Server cannot write to %s: %v", c.SharePathServer, err)
	}
	local := filepath.Join(c.SharePathLocal, name)
	defer os.Remove(local)
	f, err := os.Open(local)
	if err != nil {
		return r, fmt.Errorf("share probe: file written by SQL Server not readable at %s: %v", c.SharePathLocal, err)
	}
	f.Close()
	return r, nil
}

func (m mssql) Backup(ctx context.Context, c Conn, db string, w io.Writer, log io.Writer) ([]string, error) {
	cn, done, err := m.conn(ctx, c)
	if err != nil {
		return nil, err
	}
	defer done()
	name := "dumpling-" + c.OpID + ".bak"
	local := filepath.Join(c.SharePathLocal, name)
	defer os.Remove(local)
	fmt.Fprintf(log, "BACKUP DATABASE %s to share (COPY_ONLY, CHECKSUM)\n", db)
	if _, err := cn.ExecContext(ctx, `BACKUP DATABASE @p1 TO DISK = @p2 WITH COPY_ONLY, CHECKSUM, INIT`, db, serverPath(c.SharePathServer, name)); err != nil {
		return nil, err
	}
	f, err := os.Open(local)
	if err != nil {
		return nil, fmt.Errorf("backup file not found in local share path: %w", err)
	}
	defer f.Close()
	fmt.Fprintln(log, "streaming .bak into artifact")
	_, err = io.Copy(w, f)
	return nil, err
}

func (m mssql) Exists(ctx context.Context, c Conn, db string) (bool, error) {
	cn, done, err := m.conn(ctx, c)
	if err != nil {
		return false, err
	}
	defer done()
	var n int
	err = cn.QueryRowContext(ctx, `SELECT count(*) FROM sys.databases WHERE name = @p1`, db).Scan(&n)
	return n > 0, err
}

type msFile struct{ logical, typ string }

func (m mssql) Restore(ctx context.Context, c Conn, db string, overwrite bool, r io.Reader, _ RestoreOpts, log io.Writer) error {
	name := "dumpling-" + c.OpID + ".bak"
	local := filepath.Join(c.SharePathLocal, name)
	defer os.Remove(local)
	f, err := os.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	fmt.Fprintln(log, "writing .bak into share")
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	cn, done, err := m.conn(ctx, c)
	if err != nil {
		return err
	}
	defer done()
	path := serverPath(c.SharePathServer, name)
	files, err := m.fileList(ctx, cn, path)
	if err != nil {
		return err
	}
	var dataDir, logDir string
	if err := cn.QueryRowContext(ctx, `SELECT CAST(SERVERPROPERTY('InstanceDefaultDataPath') AS nvarchar(4000)),
		CAST(SERVERPROPERTY('InstanceDefaultLogPath') AS nvarchar(4000))`).Scan(&dataDir, &logDir); err != nil {
		return err
	}

	q := `RESTORE DATABASE @db FROM DISK = @path WITH CHECKSUM`
	args := []any{sql.Named("db", db), sql.Named("path", path)}
	if overwrite {
		q += ", REPLACE"
	}
	for i, fl := range files {
		dir, ext := dataDir, ".mdf"
		switch fl.typ {
		case "L":
			dir, ext = logDir, ".ldf"
		case "D":
			if i > 0 {
				ext = ".ndf"
			}
		default:
			ext = ""
		}
		q += fmt.Sprintf(", MOVE @l%d TO @t%d", i, i)
		args = append(args, sql.Named(fmt.Sprintf("l%d", i), fl.logical),
			sql.Named(fmt.Sprintf("t%d", i), serverPath(dir, fmt.Sprintf("%s_%d%s", db, i, ext))))
	}

	if overwrite {
		fmt.Fprintf(log, "taking database %s offline (ROLLBACK IMMEDIATE)\n", db)
		if _, err := cn.ExecContext(ctx, `ALTER DATABASE `+msIdent(db)+` SET OFFLINE WITH ROLLBACK IMMEDIATE`); err != nil {
			return err
		}
	}
	fmt.Fprintf(log, "RESTORE DATABASE %s\n", db)
	if _, err := cn.ExecContext(ctx, q, args...); err != nil {
		if overwrite {
			cn.ExecContext(context.Background(), `ALTER DATABASE `+msIdent(db)+` SET ONLINE`)
		}
		return err
	}
	return nil
}

func (mssql) fileList(ctx context.Context, cn *sql.Conn, path string) ([]msFile, error) {
	rows, err := cn.QueryContext(ctx, `RESTORE FILELISTONLY FROM DISK = @p1`, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []msFile
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		var fl msFile
		for i, col := range cols {
			s := fmt.Sprint(vals[i])
			if b, ok := vals[i].([]byte); ok {
				s = string(b)
			}
			switch col {
			case "LogicalName":
				fl.logical = s
			case "Type":
				fl.typ = s
			}
		}
		out = append(out, fl)
	}
	return out, rows.Err()
}
