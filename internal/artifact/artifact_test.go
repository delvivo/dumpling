package artifact

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func init() { ScryptWorkFactor = 10 }

func TestRoundTrip(t *testing.T) {
	data := bytes.Repeat([]byte("row 1, hello world\n"), 5000)
	for _, tc := range []struct {
		gz   bool
		pass string
	}{{false, ""}, {true, ""}, {false, "pw"}, {true, "pw"}} {
		t.Run(fmt.Sprintf("gz=%v,enc=%v", tc.gz, tc.pass != ""), func(t *testing.T) {
			final := filepath.Join(t.TempDir(), "a", "b.dump")
			w, err := Create(final, tc.gz, tc.pass)
			if err != nil {
				t.Fatal(err)
			}
			w.Write(data)
			size, sum, err := w.Commit(map[string]any{"database": "shop"})
			if err != nil {
				t.Fatal(err)
			}
			if st, _ := os.Stat(final); st.Size() != size {
				t.Fatalf("size %d != %d", st.Size(), size)
			}
			if _, err := os.Stat(final + PartialSuffix); !os.IsNotExist(err) {
				t.Fatal("partial left behind")
			}
			if _, err := os.Stat(final + ".json"); err != nil {
				t.Fatal("no sidecar")
			}
			if c, _ := Checksum(final); c != sum {
				t.Fatal("checksum mismatch")
			}
			r, err := Open(final, tc.gz, tc.pass)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(r)
			r.Close()
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("round trip differs: %v", err)
			}
			if tc.pass != "" {
				if _, err := Open(final, tc.gz, "wrong"); !errors.Is(err, ErrPassphrase) {
					t.Fatalf("wrong passphrase: %v", err)
				}
			}
		})
	}
}

func TestAbortAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "x.dump")
	w, _ := Create(final, true, "")
	w.Write([]byte("partial"))
	w.Abort()
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("abort left %d files", len(entries))
	}

	w, _ = Create(final, false, "")
	w.Write([]byte("first"))
	if _, _, err := w.Commit(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(final, false, ""); err == nil {
		t.Fatal("create over existing artifact must fail")
	}
	// Race: final appears after Create; Commit must refuse instead of overwriting.
	other := filepath.Join(dir, "y.dump")
	w2, _ := Create(other, false, "")
	os.WriteFile(other, []byte("someone else"), 0o600)
	w2.Write([]byte("second"))
	if _, _, err := w2.Commit(nil); err == nil {
		t.Fatal("commit over existing file must fail")
	}
	if b, _ := os.ReadFile(other); string(b) != "someone else" {
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
	final := filepath.Join(t.TempDir(), "big.sql.gz.age")
	w, err := Create(final, true, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(w, &lines{left: total}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Commit(nil); err != nil {
		t.Fatal(err)
	}
	r, err := Open(final, true, "pw")
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
