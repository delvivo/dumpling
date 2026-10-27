// Package artifact streams backup data through gzip, age encryption and SHA-256 into a file,
// committing atomically (.partial → final) with a JSON metadata sidecar.
package artifact

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filippo.io/age"
)

const PartialSuffix = ".partial"

// ScryptWorkFactor is the age scrypt cost for new artifacts (tests lower it).
var ScryptWorkFactor = 18

type Writer struct {
	final  string
	f      *os.File
	h      io.Writer
	sum    func() string
	n      int64
	layers []io.WriteCloser // innermost first: gzip, age
	w      io.Writer
	done   bool
}

type counter struct {
	w io.Writer
	n *int64
}

func (c counter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	*c.n += int64(n)
	return n, err
}

// Create opens <final>.partial. Data written is gzip-compressed (if gz) then age-encrypted (if passphrase != "").
func Create(final string, gz bool, passphrase string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
		return nil, err
	}
	if _, err := os.Stat(final); err == nil {
		return nil, fmt.Errorf("artifact %s already exists", final)
	}
	f, err := os.OpenFile(final+PartialSuffix, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	w := &Writer{final: final, f: f, sum: func() string { return hex.EncodeToString(h.Sum(nil)) }}
	var out io.Writer = counter{io.MultiWriter(f, h), &w.n}
	var layers []io.WriteCloser
	if passphrase != "" {
		r, err := age.NewScryptRecipient(passphrase)
		if err != nil {
			w.Abort()
			return nil, err
		}
		r.SetWorkFactor(ScryptWorkFactor)
		enc, err := age.Encrypt(out, r)
		if err != nil {
			w.Abort()
			return nil, err
		}
		layers = append([]io.WriteCloser{enc}, layers...)
		out = enc
	}
	if gz {
		g := gzip.NewWriter(out)
		layers = append([]io.WriteCloser{g}, layers...)
		out = g
	}
	w.layers, w.w = layers, out
	return w, nil
}

func (w *Writer) Write(p []byte) (int, error) { return w.w.Write(p) }

// Commit closes all layers, fsyncs, moves the file to its final name (never overwriting) and writes
// the sidecar <final>.json with meta plus size and sha256. Returns size and hex checksum of stored bytes.
func (w *Writer) Commit(meta map[string]any) (int64, string, error) {
	for _, l := range w.layers {
		if err := l.Close(); err != nil {
			w.Abort()
			return 0, "", err
		}
	}
	if err := w.f.Sync(); err != nil {
		w.Abort()
		return 0, "", err
	}
	if err := w.f.Close(); err != nil {
		w.Abort()
		return 0, "", err
	}
	// Link fails if the final name exists, so an artifact is never overwritten.
	if err := os.Link(w.final+PartialSuffix, w.final); err != nil {
		w.Abort()
		return 0, "", err
	}
	os.Remove(w.final + PartialSuffix)
	w.done = true
	size, sum := w.n, w.sum()
	if meta == nil {
		meta = map[string]any{}
	}
	meta["size_bytes"], meta["sha256"] = size, sum
	if err := writeJSONAtomic(w.final+".json", meta); err != nil {
		return size, sum, fmt.Errorf("sidecar: %w", err)
	}
	return size, sum, nil
}

// Abort removes the partial file. Safe to call multiple times and after Commit (no-op).
func (w *Writer) Abort() {
	if w.done {
		return
	}
	w.done = true
	w.f.Close()
	os.Remove(w.final + PartialSuffix)
}

func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + PartialSuffix
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type readCloser struct {
	io.Reader
	closers []io.Closer
}

func (r readCloser) Close() error {
	var errs []error
	for _, c := range r.closers {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

// ErrPassphrase is returned when an encrypted artifact cannot be decrypted.
var ErrPassphrase = errors.New("wrong or missing passphrase")

// Open returns the original data stream of an artifact.
func Open(path string, gz bool, passphrase string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	rc := readCloser{Reader: f, closers: []io.Closer{f}}
	if passphrase != "" {
		id, err := age.NewScryptIdentity(passphrase)
		if err != nil {
			f.Close()
			return nil, err
		}
		dec, err := age.Decrypt(f, id)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("%w: %v", ErrPassphrase, err)
		}
		rc.Reader = dec
	}
	if gz {
		g, err := gzip.NewReader(rc.Reader)
		if err != nil {
			f.Close()
			return nil, err
		}
		rc.Reader = g
		rc.closers = append([]io.Closer{g}, rc.closers...)
	}
	return rc, nil
}

// Checksum returns the hex SHA-256 of the stored file.
func Checksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
