package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/delvivo/dumpling/internal/secret"
)

func openTest(t *testing.T, path string) *Store {
	t.Helper()
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(path, box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB.Close() })
	return st
}

// A database created before migration 1 (old schema, user_version 0) gains the new columns.
func TestMigrateFromOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO destinations(id,name,path) VALUES('d1','disk','/srv/b')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st := openTest(t, path)
	d, err := st.Destination("d1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Type != "local" || d.Path != "/srv/b" {
		t.Fatalf("got %+v", d)
	}
	var v int
	st.DB.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != len(migrations) {
		t.Fatalf("user_version %d", v)
	}
	st.DB.Close()
	openTest(t, path) // reopening is a no-op
}
