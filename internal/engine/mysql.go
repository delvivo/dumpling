package engine

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type mysql struct{}

var mysqlSystemDBs = map[string]bool{"information_schema": true, "mysql": true, "performance_schema": true, "sys": true}

// defaultsFile renders a [client] option file; credentials never go on argv.
func defaultsFile(c Conn) string {
	q := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	s := "[client]\nuser=" + q(c.Username) + "\npassword=" + q(c.Password) + "\nhost=" + q(c.Host) +
		"\nport=" + strconv.Itoa(c.Port) + "\n"
	if c.TLSMode != "" {
		s += "ssl-mode=" + c.TLSMode + "\n"
	}
	return s + "[mysql]\nconnect-timeout=10\n" // mysqldump rejects connect-timeout in [client]
}

// withDefaults writes the option file (0600) into c.TmpDir and removes it after fn.
func withDefaults(c Conn, fn func(arg string) error) error {
	f, err := os.CreateTemp(c.TmpDir, "my-*.cnf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	f.Chmod(0o600)
	_, err = f.WriteString(defaultsFile(c))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return fn("--defaults-extra-file=" + f.Name())
}

func myLit(s string) string   { return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(s) + "'" }
func myIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

func (mysql) sql(ctx context.Context, c Conn, query string) (out string, err error) {
	err = withDefaults(c, func(def string) error {
		out, err = output(ctx, "mysql", []string{def, "-N", "-B"}, nil, strings.NewReader(query))
		return err
	})
	return
}

func (m mysql) Test(ctx context.Context, c Conn) (TestResult, error) {
	r := TestResult{Databases: []string{}, Warnings: []string{}}
	out, err := m.sql(ctx, c, "SELECT VERSION();\nSHOW DATABASES;\n")
	if err != nil {
		return r, err
	}
	l := lines(out)
	if len(l) == 0 {
		return r, fmt.Errorf("mysql: unexpected empty output")
	}
	r.Version = l[0]
	for _, db := range l[1:] {
		if !mysqlSystemDBs[db] {
			r.Databases = append(r.Databases, db)
		}
	}
	r.Warnings = append(r.Warnings, MissingTools("mysql")...)
	return r, nil
}

func mysqldumpArgs(def, db string) []string {
	// db as plain argument (no --databases): dump has no CREATE DATABASE/USE, restorable under any name.
	return []string{def, "--single-transaction", "--routines", "--triggers", "--events", "--hex-blob", "--set-gtid-purged=OFF", db}
}

func (m mysql) Backup(ctx context.Context, c Conn, db string, w io.Writer, log io.Writer) ([]string, error) {
	var warnings []string
	out, err := m.sql(ctx, c, "SELECT TABLE_NAME, ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA="+myLit(db)+
		" AND TABLE_TYPE='BASE TABLE' AND ENGINE<>'InnoDB';\n")
	if err != nil {
		return nil, err
	}
	if l := lines(out); len(l) > 0 {
		warnings = append(warnings, "non-transactional tables are not snapshot-consistent: "+strings.Join(l, ", "))
		fmt.Fprintln(log, "warning:", warnings[0])
	}
	return warnings, withDefaults(c, func(def string) error {
		return run(ctx, "mysqldump", mysqldumpArgs(def, db), nil, nil, w, log)
	})
}

func (m mysql) Exists(ctx context.Context, c Conn, db string) (bool, error) {
	out, err := m.sql(ctx, c, "SELECT 1 FROM information_schema.SCHEMATA WHERE SCHEMA_NAME="+myLit(db)+";\n")
	return out == "1", err
}

func (m mysql) Restore(ctx context.Context, c Conn, db string, overwrite bool, r io.Reader, o RestoreOpts, log io.Writer) error {
	q := "CREATE DATABASE " + myIdent(db) + ";\n"
	if overwrite {
		fmt.Fprintf(log, "dropping database %s\n", db)
		q = "DROP DATABASE " + myIdent(db) + ";\n" + q
	}
	if _, err := m.sql(ctx, c, q); err != nil {
		return err
	}
	if o.SkipOwnership {
		r = stripDefiner(r)
	}
	fmt.Fprintf(log, "created database %s, loading dump\n", db)
	return withDefaults(c, func(def string) error {
		return run(ctx, "mysql", []string{def, db}, nil, r, io.Discard, log)
	})
}

var definerRe = regexp.MustCompile("DEFINER=`[^`]*`@`[^`]*` ?")

// stripDefiner removes DEFINER clauses (also inside /*!50013 ... */) line by line, streaming.
func stripDefiner(r io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		br := bufio.NewReaderSize(r, 1<<20)
		bw := bufio.NewWriterSize(pw, 1<<20)
		for {
			line, err := br.ReadString('\n')
			if strings.Contains(line, "DEFINER=") {
				line = definerRe.ReplaceAllString(line, "")
			}
			if _, werr := bw.WriteString(line); werr != nil {
				pw.CloseWithError(werr)
				return
			}
			if err == io.EOF {
				pw.CloseWithError(bw.Flush())
				return
			}
			if err != nil {
				pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr
}
