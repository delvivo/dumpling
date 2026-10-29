package secret

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestSealOpen(t *testing.T) {
	b, _ := New(bytes.Repeat([]byte{1}, 32))
	s, err := b.Seal("hunter2")
	if err != nil || s == "" || s == "hunter2" {
		t.Fatalf("seal: %q %v", s, err)
	}
	if p, err := b.Open(s); err != nil || p != "hunter2" {
		t.Fatalf("open: %q %v", p, err)
	}
	if e, _ := b.Seal(""); e != "" {
		t.Fatal("empty must stay empty")
	}

	raw, _ := base64.StdEncoding.DecodeString(s)
	raw[len(raw)-1] ^= 1
	if _, err := b.Open(base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("tampered value must fail")
	}

	other, _ := New(bytes.Repeat([]byte{2}, 32))
	if _, err := other.Open(s); err == nil {
		t.Fatal("wrong key must fail")
	}
}

func TestLoadKeyCreatesAndReuses(t *testing.T) {
	t.Setenv("DUMPLING_MASTER_KEY", "")
	dir := t.TempDir()
	k1, err := LoadKey(dir)
	if err != nil || len(k1) != 32 {
		t.Fatal(err)
	}
	k2, _ := LoadKey(dir)
	if !bytes.Equal(k1, k2) {
		t.Fatal("key not reused")
	}
	t.Setenv("DUMPLING_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)))
	if k3, _ := LoadKey(dir); k3[0] != 3 {
		t.Fatal("env key ignored")
	}
}
