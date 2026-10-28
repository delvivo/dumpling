package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "github.com/microsoft/go-mssqldb"
)

// mssql uses native BACKUP/RESTORE DATABASE (COPY_ONLY; incremental plans: full + DIFFERENTIAL)
// through a directory shared between SQL Server (SharePathServer) and this app (SharePathLocal).
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
	return nil, m.backupTo(ctx, c, db, "COPY_ONLY, CHECKSUM, INIT", w, log)
}

// backupTo runs BACKUP DATABASE … WITH <opts> into the share and streams the .bak into w.
func (m mssql) backupTo(ctx context.Context, c Conn, db, opts string, w io.Writer, log io.Writer) error {
	cn, done, err := m.conn(ctx, c)
	if err != nil {
		return err
	}
	defer done()
	name := "dumpling-" + c.OpID + ".bak"
	local := filepath.Join(c.SharePathLocal, name)
	defer os.Remove(local)
	fmt.Fprintf(log, "BACKUP DATABASE %s to share (%s)\n", db, opts)
	if _, err := cn.ExecContext(ctx, `BACKUP DATABASE @p1 TO DISK = @p2 WITH `+opts, db, serverPath(c.SharePathServer, name)); err != nil {
		return err
	}
	f, err := os.Open(local)
	if err != nil {
		return fmt.Errorf("backup file not found in local share path: %w", err)
	}
	defer f.Close()
	fmt.Fprintln(log, "streaming .bak into artifact")
	_, err = io.Copy(w, f)
	return err
}

// msMarker is the chain state of SQL Server links: the differential base every diff builds on.
type msMarker struct {
	BaseGUID string `json:"base_guid"`
}

// baseGUID is the database's current differential base (set by the last non-COPY_ONLY full).
func (m mssql) baseGUID(ctx context.Context, c Conn, db string) (string, error) {
	cn, done, err := m.conn(ctx, c)
	if err != nil {
		return "", err
	}
	defer done()
	var g sql.NullString
	err = cn.QueryRowContext(ctx, `SELECT CONVERT(nvarchar(36), differential_base_guid) FROM sys.master_files
		WHERE database_id = DB_ID(@p1) AND file_id = 1`, db).Scan(&g)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("database %s not found", db)
	}
	return strings.ToLower(g.String), err
}

func (mssql) Prereqs(context.Context, Conn, string) (missing, warnings []string, err error) {
	return nil, nil, nil // native BACKUP only; the share is probed by Test
}

// BackupFull takes a non-COPY_ONLY full: it becomes the differential base (FR-007).
func (m mssql) BackupFull(ctx context.Context, c Conn, db string, w, _, log io.Writer) (string, []string, error) {
	if err := m.backupTo(ctx, c, db, "CHECKSUM, INIT", w, log); err != nil {
		return "", nil, err
	}
	g, err := m.baseGUID(ctx, c, db)
	if err != nil {
		return "", nil, err
	}
	mk, _ := json.Marshal(msMarker{BaseGUID: g})
	return string(mk), nil, nil
}

// BackupIncr takes a differential, refusing when another full replaced the chain's base.
func (m mssql) BackupIncr(ctx context.Context, c Conn, db string, prev Link, w, _, log io.Writer) (string, []string, error) {
	var pm msMarker
	json.Unmarshal([]byte(prev.Marker), &pm)
	g, err := m.baseGUID(ctx, c, db)
	if err != nil {
		return "", nil, err
	}
	if pm.BaseGUID == "" || g != pm.BaseGUID {
		return "", nil, fmt.Errorf("%w: differential base changed outside Dumpling (now %s, chain %s)", ErrChainBroken, g, pm.BaseGUID)
	}
	if err := m.backupTo(ctx, c, db, "DIFFERENTIAL, CHECKSUM, INIT", w, log); err != nil {
		return "", nil, err
	}
	return prev.Marker, nil, nil
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
	if err := toShare(c, name, r, log); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(c.SharePathLocal, name))
	cn, done, err := m.conn(ctx, c)
	if err != nil {
		return err
	}
	defer done()
	return m.restoreFull(ctx, cn, c, db, overwrite, serverPath(c.SharePathServer, name), false, log)
}

// RestoreChain restores a full and, for a differential, that one differential on top: SQL Server
// differentials are cumulative, so links is [full] or [full, diff].
func (m mssql) RestoreChain(ctx context.Context, c Conn, db string, overwrite bool, links []Link, _ RestoreOpts, log io.Writer) error {
	if len(links) == 0 || len(links) > 2 {
		return fmt.Errorf("mssql chain restore needs the full and at most one differential, got %d links", len(links))
	}
	paths := make([]string, len(links))
	for i, l := range links {
		name := fmt.Sprintf("dumpling-%s-%d.bak", c.OpID, i)
		f, err := os.Open(l.Path)
		if err != nil {
			return err
		}
		err = toShare(c, name, f, log)
		f.Close()
		defer os.Remove(filepath.Join(c.SharePathLocal, name))
		if err != nil {
			return err
		}
		paths[i] = serverPath(c.SharePathServer, name)
	}
	cn, done, err := m.conn(ctx, c)
	if err != nil {
		return err
	}
	defer done()
	diff := len(links) == 2
	if err := m.restoreFull(ctx, cn, c, db, overwrite, paths[0], diff, log); err != nil {
		return err
	}
	if !diff {
		return nil
	}
	fmt.Fprintf(log, "RESTORE DATABASE %s from differential (RECOVERY)\n", db)
	if _, err := cn.ExecContext(ctx, `RESTORE DATABASE @p1 FROM DISK = @p2 WITH RECOVERY, CHECKSUM`, db, paths[1]); err != nil {
		if !overwrite { // created by this restore: remove the half-restored database
			if _, derr := cn.ExecContext(context.Background(), `DROP DATABASE `+msIdent(db)); derr == nil {
				fmt.Fprintf(log, "dropped database %s after failed restore\n", db)
				return err
			}
		}
		return fmt.Errorf("%w; database %s is left in RESTORING state (full restored, differential not applied)", err, db)
	}
	return nil
}

// toShare copies a .bak into the share directory for SQL Server to read.
func toShare(c Conn, name string, r io.Reader, log io.Writer) error {
	f, err := os.OpenFile(filepath.Join(c.SharePathLocal, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	fmt.Fprintln(log, "writing .bak into share")
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// restoreFull restores a full .bak (RECOVERY, or NORECOVERY when a differential follows), moving
// files to the instance default directories under names derived from db.
func (m mssql) restoreFull(ctx context.Context, cn *sql.Conn, c Conn, db string, overwrite bool, path string, norecovery bool, log io.Writer) error {
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
	if norecovery {
		q += ", NORECOVERY"
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
	mode := "RECOVERY"
	if norecovery {
		mode = "NORECOVERY"
	}
	fmt.Fprintf(log, "RESTORE DATABASE %s (%s)\n", db, mode)
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
