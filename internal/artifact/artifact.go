// Package artifact streams backup data through gzip, age encryption and SHA-256 into a storage
// upload, committing atomically with a JSON metadata sidecar.
package artifact

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"filippo.io/age"
	"github.com/delvivo/dumpling/internal/storage"
)

// ScryptWorkFactor is the age scrypt cost for new artifacts (tests lower it).
var ScryptWorkFactor = 18

type Writer struct {
	up     storage.Upload
	sum    func() string
	n      int64
	layers []io.WriteCloser // innermost first: gzip, age
	w      io.Writer
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

// Create wraps an upload. Data written is gzip-compressed (if gz) then age-encrypted
// (if passphrase != ""); size and SHA-256 are computed over the stored bytes.
func Create(up storage.Upload, gz bool, passphrase string) (*Writer, error) {
	h := sha256.New()
	w := &Writer{up: up, sum: func() string { return hex.EncodeToString(h.Sum(nil)) }}
	var out io.Writer = counter{io.MultiWriter(up, h), &w.n}
	var layers []io.WriteCloser
	if passphrase != "" {
		r, err := age.NewScryptRecipient(passphrase)
		if err != nil {
			up.Abort()
			return nil, err
		}
		r.SetWorkFactor(ScryptWorkFactor)
		enc, err := age.Encrypt(out, r)
		if err != nil {
			up.Abort()
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

// Commit closes all layers, commits the upload (never overwriting) and writes the sidecar
// <rel>.json with meta plus size and sha256 to b. Returns size and hex checksum of stored bytes.
// A sidecar error is returned together with a valid size: the artifact itself is committed.
func (w *Writer) Commit(b storage.Backend, rel string, meta map[string]any) (int64, string, error) {
	size, sum, err := w.CommitBare()
	if err != nil {
		return 0, "", err
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta["size_bytes"], meta["sha256"] = size, sum
	j, err := json.MarshalIndent(meta, "", "  ")
	if err == nil {
		err = storage.PutBytes(b, rel+".json", j)
	}
	if err != nil {
		return size, sum, fmt.Errorf("sidecar: %w", err)
	}
	return size, sum, nil
}

// CommitBare closes all layers and commits the upload without a sidecar (auxiliary files such as
// PostgreSQL manifests, which must not look like backups to a rescan).
func (w *Writer) CommitBare() (int64, string, error) {
	for _, l := range w.layers {
		if err := l.Close(); err != nil {
			w.up.Abort()
			return 0, "", err
		}
	}
	if err := w.up.Commit(); err != nil {
		return 0, "", err
	}
	return w.n, w.sum(), nil
}

// Abort discards the upload. Safe after Commit (no-op).
func (w *Writer) Abort() { w.up.Abort() }

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

// Open returns the original data stream of a stored artifact; closing it closes src.
func Open(src io.ReadCloser, gz bool, passphrase string) (io.ReadCloser, error) {
	rc := readCloser{Reader: src, closers: []io.Closer{src}}
	if passphrase != "" {
		id, err := age.NewScryptIdentity(passphrase)
		if err != nil {
			src.Close()
			return nil, err
		}
		dec, err := age.Decrypt(src, id)
		if err != nil {
			src.Close()
			return nil, fmt.Errorf("%w: %v", ErrPassphrase, err)
		}
		rc.Reader = dec
	}
	if gz {
		g, err := gzip.NewReader(rc.Reader)
		if err != nil {
			src.Close()
			return nil, err
		}
		rc.Reader = g
		rc.closers = append([]io.Closer{g}, rc.closers...)
	}
	return rc, nil
}

// CheckPassphrase verifies a passphrase against an encrypted artifact by decrypting its age
// header only (the header MAC fails on a wrong passphrase); no full read needed.
func CheckPassphrase(r io.Reader, passphrase string) error {
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return err
	}
	if _, err := age.Decrypt(r, id); err != nil {
		return fmt.Errorf("%w: %v", ErrPassphrase, err)
	}
	return nil
}

// Checksum returns the hex SHA-256 of a stored artifact stream.
func Checksum(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
