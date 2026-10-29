package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"math/rand"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/delvivo/dumpling/internal/store"
)

// testDests returns the destinations to run the contract against: local always, S3 and SFTP when
// DUMPLING_TEST_S3_* / DUMPLING_TEST_SFTP_* are set (the compose `it` service sets them).
func testDests(t *testing.T) map[string]store.Destination {
	out := map[string]store.Destination{"local": {Type: "local", Path: t.TempDir()}}
	host, port, fp := sftpServer(t, nil)
	out["sftp-mem"] = store.Destination{Type: "sftp", SFTP: &store.SFTPConfig{Host: host, Port: port, Username: "u",
		Auth: "password", Password: "pw", RemoteDir: "/c", HostKeyFingerprint: fp}}
	if d, ok := s3TestDest(); ok {
		out["s3"] = d
	}
	if d, ok := sftpTestDest(t); ok {
		out["sftp"] = d
	}
	return out
}

func s3TestDest() (store.Destination, bool) {
	ep := os.Getenv("DUMPLING_TEST_S3_ENDPOINT")
	if ep == "" {
		return store.Destination{}, false
	}
	return store.Destination{Type: "s3", S3: &store.S3Config{Endpoint: ep, Region: "us-east-1",
		Bucket: os.Getenv("DUMPLING_TEST_S3_BUCKET"), Prefix: "it-" + store.NewID(), PathStyle: true,
		AccessKeyID: os.Getenv("DUMPLING_TEST_S3_KEY"), SecretAccessKey: os.Getenv("DUMPLING_TEST_S3_SECRET")}}, true
}

func sftpTestDest(t *testing.T) (store.Destination, bool) {
	addr := os.Getenv("DUMPLING_TEST_SFTP_ADDR")
	if addr == "" {
		return store.Destination{}, false
	}
	host, p, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(p)
	fp, err := ProbeHostKey(context.Background(), host, port)
	if err != nil {
		t.Fatal(err)
	}
	return store.Destination{Type: "sftp", SFTP: &store.SFTPConfig{Host: host, Port: port, Auth: "password",
		Username: os.Getenv("DUMPLING_TEST_SFTP_USER"), Password: os.Getenv("DUMPLING_TEST_SFTP_PASSWORD"),
		RemoteDir: os.Getenv("DUMPLING_TEST_SFTP_DIR") + "/it-" + store.NewID(), HostKeyFingerprint: fp}}, true
}

func put(t *testing.T, b Backend, rel string, data []byte) {
	t.Helper()
	if err := PutBytes(b, rel, data); err != nil {
		t.Fatal(err)
	}
}

func TestContract(t *testing.T) {
	for name, d := range testDests(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := Test(ctx, d); err != nil {
				t.Fatalf("Test: %v", err)
			}
			b, err := Open(ctx, d)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			data := bytes.Repeat([]byte("dumpling "), 10000)
			put(t, b, "p/x/a.dump", data)

			if n, err := b.Stat("p/x/a.dump"); err != nil || n != int64(len(data)) {
				t.Fatalf("Stat %d %v", n, err)
			}
			r, err := b.Open("p/x/a.dump")
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(r)
			r.Close()
			if !bytes.Equal(got, data) {
				t.Fatal("content differs")
			}
			if _, err := b.Create("p/x/a.dump"); err == nil {
				t.Fatal("create over existing must fail")
			}

			up, _ := b.Create("p/x/aborted.dump")
			up.Write([]byte("half"))
			up.Abort()
			put(t, b, "p/x/a.dump.json", []byte("{}"))
			if l, _ := b.List(""); len(l) != 2 {
				t.Fatalf("List after abort: %v", l)
			}
			if l, _ := b.List(".json"); len(l) != 1 || l[0] != "p/x/a.dump.json" {
				t.Fatalf("List .json: %v", l)
			}

			if err := b.Remove("p/x/a.dump"); err != nil {
				t.Fatal(err)
			}
			if _, err := b.Open("p/x/a.dump"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("Open removed: %v", err)
			}
			if _, err := b.Stat("p/x/a.dump"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("Stat removed: %v", err)
			}
			if err := b.Remove("p/x/a.dump"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("Remove missing: %v", err)
			}
		})
	}
}

// TestCrash: a write interrupted without Commit/Abort (process killed) never shows up as an
// artifact, and Cleanup removes its leftovers (SC-004).
func TestCrash(t *testing.T) {
	for name, d := range testDests(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			b, err := Open(ctx, d)
			if err != nil {
				t.Fatal(err)
			}
			size := 1 << 20
			if name == "s3" {
				size = 70 << 20 // more than one part: the multipart upload is initiated
			}
			up, err := b.Create("c/crash.dump")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.CopyN(up, rand.New(rand.NewSource(1)), int64(size)); err != nil {
				t.Fatal(err)
			}
			cancel() // simulated crash: no Commit, no Abort
			if lu, ok := up.(*localUpload); ok {
				lu.f.Close() // the OS closes handles of a dead process (Windows cannot delete open files)
			}
			b.Close()

			b, err = Open(context.Background(), d)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if _, err := b.Stat("c/crash.dump"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("crashed artifact visible: %v", err)
			}
			if err := b.Cleanup(); err != nil {
				t.Fatal(err)
			}
			if l, _ := b.List(""); len(l) != 0 {
				t.Fatalf("leftovers after cleanup: %v", l)
			}
			if s, ok := b.(*s3b); ok {
				for u := range s.c.ListIncompleteUploads(context.Background(), s.bucket, s.listPrefix(), true) {
					t.Fatalf("incomplete upload left: %s %v", u.Key, u.Err)
				}
			}
			if l, ok := b.(*local); ok {
				if entries, _ := os.ReadDir(l.root + "/c"); len(entries) != 0 {
					t.Fatalf("partial left: %v", entries)
				}
			}
		})
	}
}

// TestS3UploadMemory streams 2 GiB of incompressible data to S3 with bounded heap (SC-003).
func TestS3UploadMemory(t *testing.T) {
	d, ok := s3TestDest()
	if !ok || testing.Short() {
		t.Skip("needs DUMPLING_TEST_S3_* and no -short")
	}
	b, err := Open(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	base := m.HeapInuse
	var peak atomic.Uint64
	stop := make(chan struct{})
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapInuse > peak.Load() {
					peak.Store(m.HeapInuse)
				}
			}
		}
	}()
	up, err := b.Create("big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(up, rand.New(rand.NewSource(2)), 2<<30); err != nil {
		t.Fatal(err)
	}
	if err := up.Commit(); err != nil {
		t.Fatal(err)
	}
	close(stop)
	defer b.Remove("big.bin")
	if p := peak.Load(); p > base+200<<20 {
		t.Fatalf("heap grew by %d MiB", (p-base)>>20)
	}
}
