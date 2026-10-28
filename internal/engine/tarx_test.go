package engine

import (
	"archive/tar"
	"bytes"
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
