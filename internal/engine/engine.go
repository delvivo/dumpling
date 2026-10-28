// Package engine drives each database engine's native backup/restore tooling.
package engine

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Conn is a resolved connection; Password is plaintext in memory only and never put on argv.
type Conn struct {
	Engine, Host    string
	Port            int
	Username        string
	Password        string
	TLSMode         string
	SharePathServer string // mssql: share dir as seen by SQL Server
	SharePathLocal  string // mssql: same dir as seen by this app
	TmpDir          string // private dir for short-lived credential files
	OpID            string // unique per operation; names server-side temp files
}

type TestResult struct {
	Version   string   `json:"server_version"`
	Databases []string `json:"databases"`
	Warnings  []string `json:"warnings"`
}

type RestoreOpts struct{ SkipOwnership bool }

type Engine interface {
	Test(ctx context.Context, c Conn) (TestResult, error)
	// Backup writes a consistent hot dump of db to w; tool diagnostics go to log.
	Backup(ctx context.Context, c Conn, db string, w io.Writer, log io.Writer) (warnings []string, err error)
	Exists(ctx context.Context, c Conn, db string) (bool, error)
	// Restore loads r into db. overwrite=true drops and recreates an existing db first.
	Restore(ctx context.Context, c Conn, db string, overwrite bool, r io.Reader, opts RestoreOpts, log io.Writer) error
}

// Errors of incremental capture.
var (
	// ErrChainBroken: the previous link cannot be continued (base replaced, log purged, other
	// server); the runner takes a full instead.
	ErrChainBroken = errors.New("chain broken")
	// ErrLockUnsafe: taking the global lock a full needs would stall writes; no backup this run.
	ErrLockUnsafe = errors.New("lock unsafe")
)

// Link is one link of a backup chain as seen by an engine.
type Link struct {
	Marker string // engine resume state (JSON) recorded with the link
	Path   string // local file holding the decoded payload (restore only)
	State  string // local file of engine state from the link (postgres: backup_manifest); "" otherwise
}

// Incremental is implemented by engines that can produce backup chains. state receives engine
// state the next link needs (postgres manifest); it is nil for engines that keep it in the marker.
type Incremental interface {
	Prereqs(ctx context.Context, c Conn, db string) (missing, warnings []string, err error)
	BackupFull(ctx context.Context, c Conn, db string, w, state, log io.Writer) (marker string, warnings []string, err error)
	BackupIncr(ctx context.Context, c Conn, db string, prev Link, w, state, log io.Writer) (marker string, warnings []string, err error)
}

// ChainRestorer restores links (oldest first, ending with the chosen backup) into db.
type ChainRestorer interface {
	RestoreChain(ctx context.Context, c Conn, db string, overwrite bool, links []Link, opts RestoreOpts, log io.Writer) error
}

func For(engine string) (Engine, error) {
	switch engine {
	case "postgres":
		return postgres{}, nil
	case "mysql":
		return mysql{}, nil
	case "mssql":
		return mssql{}, nil
	}
	return nil, fmt.Errorf("unknown engine %q", engine)
}

// Gzip reports whether the app must compress an artifact (pg_dump -Fc compresses itself;
// physical postgres backups are plain tar).
func Gzip(engine string, physical bool) bool { return engine != "postgres" || physical }

// Ext is the artifact extension; kind is "full" or "incr", physical marks postgres base backups.
func Ext(engine, kind string, physical bool) string {
	switch {
	case physical:
		return "tar.gz"
	case engine == "mysql" && kind == "incr":
		return "binlog.tar.gz"
	}
	return map[string]string{"postgres": "pgdump", "mysql": "sql.gz", "mssql": "bak.gz"}[engine]
}

// RequiredTools lists the client binaries an engine needs on PATH.
func RequiredTools(engine string) []string {
	return map[string][]string{"postgres": {"psql", "pg_dump", "pg_restore"}, "mysql": {"mysql", "mysqldump"}}[engine]
}

type Tool struct {
	Path    string `json:"path"`
	Version string `json:"version"`
	Variant string `json:"variant,omitempty"` // mysql client: "mysql" or "mariadb" (unsupported)
}

var versionRe = regexp.MustCompile(`\d+\.\d+(\.\d+)?`)

// IncrTools lists the extra client binaries an engine needs for incremental mode.
func IncrTools(engine string) []string {
	return map[string][]string{"postgres": {"pg_basebackup", "pg_combinebackup"}, "mysql": {"mysqlbinlog"}}[engine]
}

// Tools detects client binaries; missing ones map to nil.
func Tools() map[string]*Tool {
	out := map[string]*Tool{}
	for _, name := range []string{"psql", "pg_dump", "pg_restore", "mysql", "mysqldump", "pg_basebackup", "pg_combinebackup", "mysqlbinlog"} {
		out[name] = detect(name)
	}
	return out
}

func detect(name string) *Tool {
	p, err := exec.LookPath(name)
	if err != nil {
		return nil
	}
	b, _ := exec.Command(p, "--version").Output()
	t := &Tool{Path: p, Version: versionRe.FindString(string(b))}
	if strings.HasPrefix(name, "mysql") {
		t.Variant = "mysql"
		if strings.Contains(strings.ToLower(string(b)), "mariadb") {
			t.Variant = "mariadb"
		}
	}
	return t
}

// MissingTools returns required tools not found (or unsupported) for engine.
func MissingTools(engine string) []string { return missing(RequiredTools(engine)) }

// MissingIncrTools is MissingTools for incremental mode (base plus incremental tools).
func MissingIncrTools(engine string) []string {
	return missing(append(RequiredTools(engine), IncrTools(engine)...))
}

func missing(names []string) []string {
	var missing []string
	for _, name := range names {
		t := detect(name)
		if t == nil {
			missing = append(missing, name)
		} else if t.Variant == "mariadb" {
			missing = append(missing, name+" (MariaDB client unsupported: needs Oracle MySQL client)")
		}
	}
	return missing
}

// Major parses the leading major version number ("17.2 (Debian…)" → 17, "8.4.3" → 8).
func Major(v string) int {
	v = versionRe.FindString(v)
	n, _ := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	return n
}

// run executes a tool with extra env; stderr goes to log (or is captured into the error if log is nil).
func run(ctx context.Context, name string, args, env []string, stdin io.Reader, stdout, log io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin, cmd.Stdout = stdin, stdout
	var errBuf bytes.Buffer
	if log != nil {
		cmd.Stderr = io.MultiWriter(log, &tailBuf{b: &errBuf})
	} else {
		cmd.Stderr = &tailBuf{b: &errBuf}
	}
	if err := cmd.Run(); err != nil {
		tool := filepath.Base(name)
		if msg := strings.TrimSpace(errBuf.String()); strings.HasPrefix(msg, tool+":") {
			return errors.New(msg) // tools already prefix their messages ("psql: error: …")
		} else if msg != "" {
			return fmt.Errorf("%s: %s", tool, msg)
		}
		return fmt.Errorf("%s: %w", tool, err)
	}
	return nil
}

// tailBuf keeps only the last 2 KiB written (bounded memory for chatty tools).
type tailBuf struct{ b *bytes.Buffer }

func (t *tailBuf) Write(p []byte) (int, error) {
	t.b.Write(p)
	if over := t.b.Len() - 2048; over > 0 {
		t.b.Next(over)
	}
	return len(p), nil
}

// output runs a tool and returns its trimmed stdout.
func output(ctx context.Context, name string, args, env []string, stdin io.Reader) (string, error) {
	var out bytes.Buffer
	err := run(ctx, name, args, env, stdin, &out, nil)
	return strings.TrimSpace(out.String()), err
}

// mapLines streams r through f line by line (each line keeps its trailing newline).
func mapLines(r io.Reader, f func(string) string) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		br := bufio.NewReaderSize(r, 1<<20)
		bw := bufio.NewWriterSize(pw, 1<<20)
		for {
			line, err := br.ReadString('\n')
			if _, werr := bw.WriteString(f(line)); werr != nil {
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

func lines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
