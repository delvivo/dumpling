package artifact

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/delvivo/dumpling/internal/storage"
	"github.com/delvivo/dumpling/internal/store"
)

func init() { ScryptWorkFactor = 10 }

func localBackend(t *testing.T) (storage.Backend, string) {
	dir := t.TempDir()
	b, err := storage.Open(context.Background(), store.Destination{Type: "local", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	return b, dir
}

func create(t *testing.T, b storage.Backend, rel string, gz bool, pass string) *Writer {
	t.Helper()
	up, err := b.Create(rel)
	if err != nil {
		t.Fatal(err)
	}
	w, err := Create(up, gz, pass)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func openFile(t *testing.T, path string, gz bool, pass string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return Open(f, gz, pass)
}

func fileSum(path string) string {
	f, _ := os.Open(path)
	defer f.Close()
	s, _ := Checksum(f)
	return s
}

func TestRoundTrip(t *testing.T) {
	data := bytes.Repeat([]byte("row 1, hello world\n"), 5000)
	for _, tc := range []struct {
		gz   bool
		pass string
	}{{false, ""}, {true, ""}, {false, "pw"}, {true, "pw"}} {
		t.Run(fmt.Sprintf("gz=%v,enc=%v", tc.gz, tc.pass != ""), func(t *testing.T) {
			b, dir := localBackend(t)
			final := filepath.Join(dir, "a", "b.dump")
			w := create(t, b, "a/b.dump", tc.gz, tc.pass)
			w.Write(data)
			size, sum, err := w.Commit(b, "a/b.dump", map[string]any{"database": "shop"})
			if err != nil {
				t.Fatal(err)
			}
			if st, _ := os.Stat(final); st.Size() != size {
				t.Fatalf("size %d != %d", st.Size(), size)
			}
			if _, err := os.Stat(final + storage.PartialSuffix); !os.IsNotExist(err) {
				t.Fatal("partial left behind")
			}
			if _, err := os.Stat(final + ".json"); err != nil {
				t.Fatal("no sidecar")
			}
			if fileSum(final) != sum {
				t.Fatal("checksum mismatch")
			}
			r, err := openFile(t, final, tc.gz, tc.pass)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(r)
			r.Close()
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("round trip differs: %v", err)
			}
			if tc.pass != "" {
				if _, err := openFile(t, final, tc.gz, "wrong"); !errors.Is(err, ErrPassphrase) {
					t.Fatalf("wrong passphrase: %v", err)
				}
				f, _ := os.Open(final)
				if err := CheckPassphrase(f, "wrong"); !errors.Is(err, ErrPassphrase) {
					t.Fatalf("CheckPassphrase wrong: %v", err)
				}
				f.Seek(0, io.SeekStart)
				if err := CheckPassphrase(f, tc.pass); err != nil {
					t.Fatalf("CheckPassphrase right: %v", err)
				}
				f.Close()
			}
		})
	}
}

func TestAbortAndNoOverwrite(t *testing.T) {
	b, dir := localBackend(t)
	w := create(t, b, "x.dump", true, "")
	w.Write([]byte("partial"))
	w.Abort()
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("abort left %d files", len(entries))
	}

	w = create(t, b, "x.dump", false, "")
	w.Write([]byte("first"))
	if _, _, err := w.Commit(b, "x.dump", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Create("x.dump"); err == nil {
		t.Fatal("create over existing artifact must fail")
	}
	// Race: final appears after Create; Commit must refuse instead of overwriting.
	other := filepath.Join(dir, "y.dump")
	w2 := create(t, b, "y.dump", false, "")
	os.WriteFile(other, []byte("someone else"), 0o600)
	w2.Write([]byte("second"))
	if _, _, err := w2.Commit(b, "y.dump", nil); err == nil {
		t.Fatal("commit over existing file must fail")
	}
	if got, _ := os.ReadFile(other); string(got) != "someone else" {
		t.Fatal("existing file overwritten")
	}
}

var chunk = bytes.Repeat([]byte("INSERT INTO t VALUES (42,'abcdefghij');\n"), 1600)

// lines is a cheap, compressible source of `left` bytes.
type lines struct{ left int64 }

func (l *lines) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, io.EOF
	}
	n := copy(p, chunk)
	if int64(n) > l.left {
		n = int(l.left)
	}
	l.left -= int64(n)
	return n, nil
}

// TestPipelineBoundedMemory streams 2 GiB through gzip+age and back, checking the heap stays small (SC-008).
func TestPipelineBoundedMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("long")
	}
	const total = 2 << 30
	var peak atomic.Uint64
	stop := make(chan struct{})
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapInuse > peak.Load() {
					peak.Store(m.HeapInuse)
				}
			}
		}
	}()
	b, dir := localBackend(t)
	final := filepath.Join(dir, "big.sql.gz.age")
	w := create(t, b, "big.sql.gz.age", true, "pw")
	if _, err := io.Copy(w, &lines{left: total}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Commit(b, "big.sql.gz.age", nil); err != nil {
		t.Fatal(err)
	}
	r, err := openFile(t, final, true, "pw")
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, r)
	r.Close()
	close(stop)
	if err != nil || n != total {
		t.Fatalf("read back %d bytes: %v", n, err)
	}
	if p := peak.Load(); p > 64<<20 {
		t.Fatalf("heap peaked at %d MiB", p>>20)
	}
}
