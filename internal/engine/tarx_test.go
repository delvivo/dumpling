package engine

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name string
	typ  byte
	body string
}

func tarOf(t *testing.T, es ...entry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range es {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644, Size: int64(len(e.body))}
		if e.typ == tar.TypeSymlink {
			h.Linkname, h.Size = "/etc/passwd", 0
		}
		if e.typ == tar.TypeDir {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(e.body))
	}
	tw.Close()
	return &buf
}

func TestUntar(t *testing.T) {
	dir := t.TempDir()
	err := untar(tarOf(t, entry{"base", tar.TypeDir, ""}, entry{"base/PG_VERSION", tar.TypeReg, "17\n"},
		entry{"backup_manifest", tar.TypeReg, "{}"}), dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "base", "PG_VERSION")); string(b) != "17\n" {
		t.Fatalf("content %q", b)
	}

	for _, bad := range []entry{
		{"../evil", tar.TypeReg, "x"},
		{"a/../../evil", tar.TypeReg, "x"},
		{"/abs", tar.TypeReg, "x"},
		{"link", tar.TypeSymlink, ""},
		{"hard", tar.TypeLink, ""},
		{"fifo", tar.TypeFifo, ""},
	} {
		if err := untar(tarOf(t, bad), t.TempDir()); err == nil {
			t.Errorf("%s (%c) accepted", bad.name, bad.typ)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil")); err == nil {
		t.Fatal("file written outside target")
	}
	// existing file is not overwritten
	if err := untar(tarOf(t, entry{"backup_manifest", tar.TypeReg, "x"}), dir); err == nil {
		t.Fatal("overwrote existing file")
	}
}

func TestPgBasebackupArgs(t *testing.T) {
	if got := strings.Join(pgBasebackupArgs(""), " "); got != "-D - -Ft -X fetch --checkpoint=fast" {
		t.Fatalf("full: %s", got)
	}
	if got := strings.Join(pgBasebackupArgs("/m/1"), " "); !strings.HasSuffix(got, "--incremental=/m/1") {
		t.Fatalf("incremental: %s", got)
	}
}

func TestSummaryGap(t *testing.T) {
	// verbatim pg_basebackup 17 output when the summaries were deleted (reproduced on pg-a)
	gap := errors.New(`pg_basebackup: error: could not initiate base backup: ERROR:  WAL summaries are required on timeline 1 from 0/11000028 to 0/16000028, but the summaries for that timeline and LSN range are incomplete`)
	if err := summaryGap(gap, true); !errors.Is(err, ErrChainBroken) {
		t.Fatalf("incremental gap not a broken chain: %v", err)
	}
	if err := summaryGap(gap, false); errors.Is(err, ErrChainBroken) {
		t.Fatal("full backup error mapped to a broken chain")
	}
	other := errors.New("pg_basebackup: error: connection to server failed")
	if err := summaryGap(other, true); errors.Is(err, ErrChainBroken) {
		t.Fatal("unrelated error mapped to a broken chain")
	}
}
