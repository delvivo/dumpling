// Package store persists app state in SQLite.
package store

import (
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/binary"
	"errors"
	"strings"
	"time"

	"github.com/delvivo/dumpling/internal/secret"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

var ErrNotFound = errors.New("not found")

type Store struct {
	DB  *sql.DB
	Box *secret.Box
}

func Open(path string, box *secret.Box) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // ponytail: single writer connection; SQLite serializes writes anyway
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db, Box: box}, nil
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns a 26-char sortable id: 48-bit ms timestamp + 80 random bits, Crockford base32.
func NewID() string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixMilli())<<16)
	rand.Read(b[6:])
	hi, lo := binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out)
}

func Now() string { return time.Now().UTC().Format(time.RFC3339) }

// null maps "" to SQL NULL for nullable FK columns.
func null(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func isUnique(err error) bool { return err != nil && strings.Contains(err.Error(), "UNIQUE") }

var ErrDuplicate = errors.New("name already exists")

func wrap(err error) error {
	if isUnique(err) {
		return ErrDuplicate
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
