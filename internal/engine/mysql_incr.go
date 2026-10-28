package engine

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// MySQL incremental mode: a full is a mysqldump that records its binary log position
// (--source-data, brief global read lock — constitution III v1.1.0); each incremental is the raw
// binary log from the previous link's end, replayed at restore with the database renamed.

// myMarker is the chain state of MySQL links.
type myMarker struct {
	ServerUUID string `json:"server_uuid"`
	Database   string `json:"database"`             // source database (rewritten to the target on replay)
	StartFile  string `json:"start_file,omitempty"` // incrementals: replay from here to the end of their files
	StartPos   int64  `json:"start_pos,omitempty"`
	File       string `json:"file"`          // where the next link starts…
	Pos        int64  `json:"pos,omitempty"` // …at this position (0 = start of file)
}

// grants returns the global privileges of the current account ("ALL PRIVILEGES" expanded).
func parseGrants(lines []string) map[string]bool {
	out := map[string]bool{}
	for _, l := range lines {
		u := strings.ToUpper(l)
		if !strings.HasPrefix(u, "GRANT ") || !strings.Contains(u, " ON *.* TO ") {
			continue
		}
		for _, p := range strings.Split(strings.TrimPrefix(u[:strings.Index(u, " ON *.* TO ")], "GRANT "), ",") {
			out[strings.TrimSpace(p)] = true
		}
	}
	if out["ALL PRIVILEGES"] || out["ALL"] {
		for _, p := range []string{"REPLICATION SLAVE", "REPLICATION CLIENT", "RELOAD", "PROCESS"} {
			out[p] = true
		}
	}
	if out["REPLICATION REPLICA"] {
		out["REPLICATION SLAVE"] = true
	}
	return out
}

func (m mysql) Prereqs(ctx context.Context, c Conn, db string) (missing, warnings []string, err error) {
	out, err := m.sql(ctx, c, "SELECT @@log_bin, @@binlog_format;\nSHOW GRANTS;\n")
	if err != nil {
		return nil, nil, err
	}
	l := lines(out)
	if len(l) == 0 {
		return nil, nil, errors.New("mysql: unexpected empty output")
	}
	if f := strings.Fields(l[0]); len(f) < 2 || f[0] != "1" {
		missing = append(missing, "binary logging is off (log_bin)")
	} else if !strings.EqualFold(f[1], "ROW") {
		warnings = append(warnings, "binlog_format is "+f[1]+": statements touching other databases may be replayed incorrectly")
	}
	g := parseGrants(l[1:])
	for _, p := range []string{"REPLICATION SLAVE", "REPLICATION CLIENT", "RELOAD"} {
		if !g[p] {
			missing = append(missing, "privilege "+p)
		}
	}
	if !g["PROCESS"] {
		warnings = append(warnings, "without PROCESS the pre-check cannot see other sessions; the 5 s watchdog still applies")
	}
	missing = append(missing, MissingIncrTools("mysql")...)
	return missing, warnings, nil
}

// procRow is one information_schema.PROCESSLIST row: id, time (s), command, state, info.
type procRow struct {
	ID          int64
	Time        int64
	Command     string
	State, Info string
}

func parseProcs(out string) []procRow {
	var rows []procRow
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(l, "\t", 5)
		if len(f) < 5 {
			continue
		}
		id, _ := strconv.ParseInt(f[0], 10, 64)
		t, _ := strconv.ParseInt(f[1], 10, 64)
		rows = append(rows, procRow{ID: id, Time: t, Command: f[2], State: f[3], Info: f[4]})
	}
	return rows
}

const procQuery = "SELECT ID, TIME, COMMAND, COALESCE(STATE,''), REPLACE(REPLACE(COALESCE(INFO,''),'\\t',' '),'\\n',' ') " +
	"FROM information_schema.PROCESSLIST WHERE ID <> CONNECTION_ID();\n"

// lockUnsafe reports a statement that would make FLUSH TABLES WITH READ LOCK wait (and so stall
// every writer): running for more than 10 s and not a plain read.
func lockUnsafe(rows []procRow) *procRow {
	for i, r := range rows {
		switch r.Command {
		case "Sleep", "Binlog Dump", "Binlog Dump GTID", "Daemon":
			continue
		}
		info := strings.ToUpper(strings.TrimSpace(r.Info))
		if r.Time > 10 && info != "" && !strings.HasPrefix(info, "SELECT") {
			return &rows[i]
		}
	}
	return nil
}

// lockWaiter is the dump session waiting on the global read lock for longer than limit, if any.
func lockWaiter(rows []procRow, limit time.Duration) *procRow {
	for i, r := range rows {
		waiting := strings.HasPrefix(r.State, "Waiting for") // global read lock, table flush, commit lock, metadata lock
		if waiting && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(r.Info)), "FLUSH") && time.Duration(r.Time)*time.Second >= limit {
			return &rows[i]
		}
	}
	return nil
}

var sourcePosRe = regexp.MustCompile(`(?:SOURCE|MASTER)_LOG_FILE='([^']+)',\s*(?:SOURCE|MASTER)_LOG_POS=(\d+)`)

// posScanner passes the dump through while looking for the --source-data comment in its first
// lines; found is closed once the position is known (the global lock is released by then).
type posScanner struct {
	w     io.Writer
	buf   bytes.Buffer
	lines int
	file  string
	pos   int64
	found chan struct{}
	done  bool
}

func (s *posScanner) Write(p []byte) (int, error) {
	if !s.done {
		s.buf.Write(p)
		for {
			line, err := s.buf.ReadString('\n')
			if err != nil { // incomplete line: keep it for the next write
				s.buf.Reset()
				s.buf.WriteString(line)
				break
			}
			s.lines++
			if m := sourcePosRe.FindStringSubmatch(line); m != nil {
				s.file = m[1]
				s.pos, _ = strconv.ParseInt(m[2], 10, 64)
				s.done = true
				close(s.found)
				break
			}
			if s.lines >= 100 {
				s.done = true
				break
			}
		}
		if s.done {
			s.buf.Reset()
		}
	}
	return s.w.Write(p)
}

func (m mysql) serverUUID(ctx context.Context, c Conn) (string, error) {
	out, err := m.sql(ctx, c, "SELECT @@server_uuid;\n")
	return strings.TrimSpace(out), err
}

const lockLimit = 5 * time.Second

func (m mysql) BackupFull(ctx context.Context, c Conn, db string, w, _, log io.Writer) (string, []string, error) {
	out, err := m.sql(ctx, c, procQuery)
	if err != nil {
		return "", nil, err
	}
	if r := lockUnsafe(parseProcs(out)); r != nil {
		return "", nil, fmt.Errorf("%w: session %d has been running %q for %d s", ErrLockUnsafe, r.ID, trim(r.Info, 80), r.Time)
	}
	uuid, err := m.serverUUID(ctx, c)
	if err != nil {
		return "", nil, err
	}
	warnings, err := m.nonInnoDB(ctx, c, db, log)
	if err != nil {
		return "", nil, err
	}

	fmt.Fprintln(log, "mysqldump --source-data: brief global read lock to record the binary log position")
	sc := &posScanner{w: w, found: make(chan struct{})}
	// watchdog: stop the dump if its FLUSH waits on the lock for more than 5 s (writes would stall)
	var killed atomic.Bool
	wctx, stop := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-wctx.Done():
				return
			case <-sc.found:
				return
			case <-t.C:
			}
			out, err := m.sql(wctx, c, procQuery)
			if err != nil {
				continue
			}
			if r := lockWaiter(parseProcs(out), lockLimit); r != nil {
				killed.Store(true)
				fmt.Fprintf(log, "global read lock not acquired within %s: KILL QUERY %d\n", lockLimit, r.ID)
				m.sql(context.Background(), c, fmt.Sprintf("KILL QUERY %d;\n", r.ID))
				return
			}
		}
	}()
	err = withDefaults(c, func(def string) error {
		args := mysqldumpArgs(def, db)
		// --flush-logs rotates while the lock is held: the recorded position opens a fresh file
		args = append(args[:len(args)-1], "--source-data=2", "--flush-logs", db)
		return run(ctx, "mysqldump", args, nil, nil, sc, log)
	})
	stop()
	if killed.Load() {
		return "", nil, fmt.Errorf("%w: global read lock not acquired within %s", ErrLockUnsafe, lockLimit)
	}
	if err != nil {
		return "", nil, err
	}
	if sc.file == "" {
		return "", nil, errors.New("mysqldump output has no binary log position (is binary logging enabled?)")
	}
	mk, _ := json.Marshal(myMarker{ServerUUID: uuid, Database: db, File: sc.file, Pos: sc.pos})
	return string(mk), warnings, nil
}

// nonInnoDB warns about tables not covered by the consistent snapshot (as Backup does).
func (m mysql) nonInnoDB(ctx context.Context, c Conn, db string, log io.Writer) ([]string, error) {
	out, err := m.sql(ctx, c, "SELECT TABLE_NAME, ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA="+myLit(db)+
		" AND TABLE_TYPE='BASE TABLE' AND ENGINE<>'InnoDB';\n")
	if err != nil {
		return nil, err
	}
	if l := lines(out); len(l) > 0 {
		w := "non-transactional tables are not snapshot-consistent: " + strings.Join(l, ", ")
		fmt.Fprintln(log, "warning:", w)
		return []string{w}, nil
	}
	return nil, nil
}

// binlogRange returns the log files from start to end inclusive, as listed by SHOW BINARY LOGS.
func binlogRange(listed []string, start, end string) ([]string, bool) {
	var out []string
	in := false
	for _, f := range listed {
		if f == start {
			in = true
		}
		if in {
			out = append(out, f)
		}
		if f == end {
			break
		}
	}
	return out, in
}

func (m mysql) BackupIncr(ctx context.Context, c Conn, db string, prev Link, w, _, log io.Writer) (string, []string, error) {
	var pm myMarker
	if err := json.Unmarshal([]byte(prev.Marker), &pm); err != nil || pm.File == "" {
		return "", nil, fmt.Errorf("%w: previous link has no binary log position", ErrChainBroken)
	}
	uuid, err := m.serverUUID(ctx, c)
	if err != nil {
		return "", nil, err
	}
	if uuid != pm.ServerUUID {
		return "", nil, fmt.Errorf("%w: server identity changed (server_uuid %s, chain %s)", ErrChainBroken, uuid, pm.ServerUUID)
	}
	// Rotate first: every file up to the new one is then closed and holds only this link's events
	// from the previous link's position on, so raw files can be fetched whole.
	if _, err := m.sql(ctx, c, "FLUSH BINARY LOGS;\n"); err != nil {
		return "", nil, err
	}
	logs, err := m.sql(ctx, c, "SHOW BINARY LOGS;\n")
	if err != nil {
		return "", nil, err
	}
	var listed []string
	for _, l := range lines(logs) {
		listed = append(listed, strings.Fields(l)[0])
	}
	if len(listed) < 2 {
		return "", nil, errors.New("binary logging is off")
	}
	next := listed[len(listed)-1] // just opened by the flush: the next link starts here
	files, ok := binlogRange(listed[:len(listed)-1], pm.File, listed[len(listed)-2])
	if !ok {
		return "", nil, fmt.Errorf("%w: binary log %s needed since the previous link was purged", ErrChainBroken, pm.File)
	}

	dir, err := os.MkdirTemp(c.TmpDir, "binlog-*")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(dir)
	fmt.Fprintf(log, "fetching binary logs %s..%s (from position %d)\n", files[0], files[len(files)-1], pm.Pos)
	err = withDefaults(c, func(def string) error {
		args := append([]string{def, "--read-from-remote-server", "--raw", "--result-file=" + dir + string(filepath.Separator)}, files...)
		return run(ctx, "mysqlbinlog", args, nil, nil, io.Discard, log)
	})
	if err != nil {
		return "", nil, err
	}
	if err := tarFiles(w, dir, files); err != nil {
		return "", nil, err
	}
	mk, _ := json.Marshal(myMarker{ServerUUID: uuid, Database: pm.Database, StartFile: pm.File, StartPos: pm.Pos, File: next})
	return string(mk), nil, nil
}

// tarFiles writes the named files of dir into w as a tar stream (names only, no directories).
func tarFiles(w io.Writer, dir string, names []string) error {
	tw := tar.NewWriter(w)
	for _, n := range names {
		f, err := os.Open(filepath.Join(dir, n))
		if err != nil {
			return err
		}
		fi, err := f.Stat()
		if err == nil {
			err = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o600, Size: fi.Size(), ModTime: fi.ModTime(), Typeflag: tar.TypeReg})
		}
		if err == nil {
			_, err = io.Copy(tw, f)
		}
		f.Close()
		if err != nil {
			return err
		}
	}
	return tw.Close()
}

// binlogArgs builds the replay of one link: GTIDs skipped (restoring onto the source server would
// otherwise silently skip transactions it already executed), events renamed to the target and
// filtered on the renamed database.
func binlogArgs(mk myMarker, target string, files []string) []string {
	args := []string{"--skip-gtids", "--rewrite-db=" + mk.Database + "->" + target, "--database=" + target}
	if mk.StartPos > 0 {
		args = append(args, "--start-position="+strconv.FormatInt(mk.StartPos, 10))
	}
	return append(args, files...)
}

func (m mysql) RestoreChain(ctx context.Context, c Conn, db string, overwrite bool, links []Link, o RestoreOpts, log io.Writer) error {
	full, err := os.Open(links[0].Path)
	if err != nil {
		return err
	}
	err = m.Restore(ctx, c, db, overwrite, full, o, log)
	full.Close()
	if err != nil {
		return err
	}
	for i, l := range links[1:] {
		if err := m.replay(ctx, c, db, l, log); err != nil {
			if !overwrite { // created by this restore: remove the partial database
				if _, derr := m.sql(context.Background(), c, "DROP DATABASE "+myIdent(db)+";\n"); derr == nil {
					fmt.Fprintf(log, "dropped database %s after failed restore\n", db)
					return err
				}
			}
			return fmt.Errorf("%w; target database %s is partially restored (full + links 1..%d)", err, db, i)
		}
	}
	return nil
}

func (m mysql) replay(ctx context.Context, c Conn, target string, l Link, log io.Writer) error {
	var mk myMarker
	if err := json.Unmarshal([]byte(l.Marker), &mk); err != nil || mk.Database == "" {
		return errors.New("incremental link has no binary log marker")
	}
	dir, err := os.MkdirTemp(c.TmpDir, "replay-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	f, err := os.Open(l.Path)
	if err != nil {
		return err
	}
	err = untar(f, dir)
	f.Close()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []string
	for _, e := range entries {
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files) // binlog.NNNNNN: lexical = chronological
	if len(files) == 0 {
		return nil
	}
	fmt.Fprintf(log, "replaying binary log from %s:%d (%d file(s)) into %s\n", mk.StartFile, mk.StartPos, len(files), target)
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := run(ctx, "mysqlbinlog", binlogArgs(mk, target, files), nil, nil, pw, log)
		pw.CloseWithError(err)
		done <- err
	}()
	err = withDefaults(c, func(def string) error {
		return run(ctx, "mysql", []string{def, target}, nil, bufio.NewReader(pr), io.Discard, log)
	})
	pr.CloseWithError(errors.New("mysql exited"))
	if rerr := <-done; err == nil {
		err = rerr
	}
	return err
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
