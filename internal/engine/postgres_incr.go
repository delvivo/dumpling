package engine

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PostgreSQL incremental mode (17+): physical base backups of the whole cluster. A full is
// `pg_basebackup -Ft -D -`; an incremental adds --incremental=<previous backup_manifest>. Restore
// rebuilds a data directory with pg_combinebackup; Dumpling never loads it into a running server.

type pgMarker struct {
	SystemID string `json:"system_id"` // cluster identity: a chain never spans two clusters
}

func (p postgres) Prereqs(ctx context.Context, c Conn, _ string) (missing, warnings []string, err error) {
	num, err := p.sql(ctx, c, "SHOW server_version_num;\n", nil)
	if err != nil {
		return nil, nil, err
	}
	n, _ := strconv.Atoi(num)
	if n < 170000 {
		return []string{fmt.Sprintf("PostgreSQL 17 or later (server is %d.%d)", n/10000, n%10000/100)}, nil, nil
	}
	out, err := p.sql(ctx, c, "SHOW summarize_wal;\nSELECT rolreplication OR rolsuper FROM pg_roles WHERE rolname = current_user;\n", nil)
	if err != nil {
		return nil, nil, err
	}
	l := lines(out)
	if len(l) < 2 || l[0] != "on" {
		missing = append(missing, "summarize_wal = on")
	}
	if len(l) < 2 || l[1] != "t" {
		missing = append(missing, "role with REPLICATION")
	}
	missing = append(missing, MissingIncrTools("postgres")...)
	for _, tool := range IncrTools("postgres") {
		if t := detect(tool); t != nil && Major(t.Version) < n/10000 {
			missing = append(missing, fmt.Sprintf("%s ≥ %d (found %s)", tool, n/10000, t.Version))
		}
	}
	return missing, nil, nil
}

// systemID is the cluster's system identifier, read over the replication protocol (the same
// access base backups need).
func (postgres) systemID(ctx context.Context, c Conn) (string, error) {
	out, err := output(ctx, "psql", []string{"-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1", "--dbname=dbname=postgres replication=database"},
		pgEnv(c), strings.NewReader("IDENTIFY_SYSTEM;\n"))
	if err != nil {
		return "", err
	}
	id := strings.SplitN(out, "|", 2)[0]
	if id == "" {
		return "", errors.New("IDENTIFY_SYSTEM returned no system identifier")
	}
	return id, nil
}

func pgBasebackupArgs(manifest string) []string {
	args := []string{"-D", "-", "-Ft", "-X", "fetch", "--checkpoint=fast"}
	if manifest != "" {
		args = append(args, "--incremental="+manifest)
	}
	return args
}

func (p postgres) BackupFull(ctx context.Context, c Conn, _ string, w, state, log io.Writer) (string, []string, error) {
	return p.basebackup(ctx, c, "", "", w, state, log)
}

func (p postgres) BackupIncr(ctx context.Context, c Conn, _ string, prev Link, w, state, log io.Writer) (string, []string, error) {
	var pm pgMarker
	json.Unmarshal([]byte(prev.Marker), &pm)
	if prev.State == "" {
		return "", nil, fmt.Errorf("%w: previous backup manifest unavailable", ErrChainBroken)
	}
	return p.basebackup(ctx, c, pm.SystemID, prev.State, w, state, log)
}

// basebackup streams one base backup tar into w and its backup_manifest member into state.
func (p postgres) basebackup(ctx context.Context, c Conn, wantID, manifest string, w, state, log io.Writer) (string, []string, error) {
	id, err := p.systemID(ctx, c)
	if err != nil {
		return "", nil, err
	}
	if wantID != "" && id != wantID {
		return "", nil, fmt.Errorf("%w: server identity changed (system identifier %s, chain %s)", ErrChainBroken, id, wantID)
	}
	if state == nil {
		return "", nil, errors.New("postgres base backup needs a state writer for its manifest")
	}
	pr, pw := io.Pipe()
	found := make(chan error, 1)
	go func() { // read the tar alongside the upload to keep the manifest the next link needs
		tr := tar.NewReader(pr)
		got := false
		for {
			h, err := tr.Next()
			if err != nil {
				if errors.Is(err, io.EOF) && !got {
					err = errors.New("pg_basebackup output has no backup_manifest")
				} else if errors.Is(err, io.EOF) {
					err = nil
				}
				io.Copy(io.Discard, pr)
				found <- err
				return
			}
			if h.Name == "backup_manifest" {
				if _, err := io.Copy(state, tr); err != nil {
					pr.CloseWithError(err)
					found <- err
					return
				}
				got = true
			}
		}
	}()
	what := "full"
	if manifest != "" {
		what = "incremental"
	}
	fmt.Fprintf(log, "pg_basebackup (%s, whole cluster %s)\n", what, id)
	err = run(ctx, "pg_basebackup", pgBasebackupArgs(manifest), pgEnv(c), nil, io.MultiWriter(w, pw), log)
	pw.CloseWithError(err)
	if ferr := <-found; err == nil {
		err = ferr
	}
	if err != nil {
		return "", nil, err
	}
	mk, _ := json.Marshal(pgMarker{SystemID: id})
	return string(mk), nil, nil
}

// RestoreChain rebuilds a data directory at target (absolute, missing or empty, inside the
// restore root — checked by the runner) from links oldest first.
func (postgres) RestoreChain(ctx context.Context, c Conn, target string, _ bool, links []Link, _ RestoreOpts, log io.Writer) error {
	work, err := os.MkdirTemp(c.TmpDir, "pgchain-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	dirs := make([]string, len(links))
	for i, l := range links {
		dirs[i] = filepath.Join(work, strconv.Itoa(i))
		if err := os.Mkdir(dirs[i], 0o700); err != nil {
			return err
		}
		f, err := os.Open(l.Path)
		if err != nil {
			return err
		}
		err = untar(f, dirs[i])
		f.Close()
		if err != nil {
			return fmt.Errorf("extract link %d: %w", i, err)
		}
	}
	os.Remove(target) // an empty target folder: pg_combinebackup creates it itself
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	fmt.Fprintf(log, "pg_combinebackup %d backup(s) → %s\n", len(dirs), target)
	if err := run(ctx, "pg_combinebackup", append(dirs, "-o", target), nil, nil, io.Discard, log); err != nil {
		os.RemoveAll(target)
		return err
	}
	return os.Chmod(target, 0o700)
}
