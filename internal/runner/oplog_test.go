package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpLogRedacts(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "logs"), 0o755)
	var lines []string
	l, err := CreateLog(dir, "op1", func(s string) { lines = append(lines, s) })
	if err != nil {
		t.Fatal(err)
	}
	l.AddSecret("s3cr3t-pw")
	l.Write([]byte("error: password s3cr"))   // secret split across writes
	l.Write([]byte("3t-pw rejected\nnext line")) // trailing partial line flushed on Close
	l.Close()

	b, _ := os.ReadFile(LogPath(dir, "op1"))
	if strings.Contains(string(b), "s3cr3t") {
		t.Fatalf("secret leaked to file: %s", b)
	}
	if !strings.Contains(string(b), "password *** rejected") || !strings.Contains(string(b), "next line") {
		t.Fatalf("unexpected log: %s", b)
	}
	if len(lines) != 2 || strings.Contains(lines[0], "s3cr3t") {
		t.Fatalf("callback lines: %q", lines)
	}
	if got := l.Redact("x s3cr3t-pw y"); got != "x *** y" {
		t.Fatal(got)
	}
}
