// Package storage reads and writes backup artifacts on a destination: local directory, S3-compatible
// bucket or SFTP server. Paths ("rel") are slash-separated and relative to the destination root.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/delvivo/dumpling/internal/store"
)

// PartialSuffix marks data still being written (local and SFTP); never listed as a backup.
const PartialSuffix = ".partial"

// Upload is an artifact being written. Nothing is visible under its final name until Commit
// succeeds; Abort discards it. Abort after Commit is a no-op.
type Upload interface {
	io.Writer
	Commit() error
	Abort()
}

// Backend is one opened destination. "Not found" errors wrap fs.ErrNotExist.
type Backend interface {
	// Create starts writing rel; fails if rel already exists (artifacts are never overwritten).
	Create(rel string) (Upload, error)
	Open(rel string) (io.ReadCloser, error)
	Stat(rel string) (int64, error)
	Remove(rel string) error
	// List returns the rel paths of committed files whose name ends with suffix.
	List(suffix string) ([]string, error)
	// Cleanup removes leftovers of interrupted writes (partial files, incomplete multipart uploads).
	Cleanup() error
	Close() error
}

// Open connects to a destination.
func Open(ctx context.Context, d store.Destination) (Backend, error) {
	switch d.Type {
	case "", "local":
		return &local{root: d.Path}, nil
	case "s3":
		return openS3(ctx, d.S3)
	case "sftp":
		return openSFTP(ctx, d.SFTP)
	}
	return nil, fmt.Errorf("unknown destination type %q", d.Type)
}

// Location is the human-readable address of rel on d (no connection needed).
func Location(d store.Destination, rel string) string {
	switch d.Type {
	case "s3":
		return "s3://" + path.Join(d.S3.Bucket, d.S3.Prefix, rel)
	case "sftp":
		return fmt.Sprintf("sftp://%s:%d%s", d.SFTP.Host, d.SFTP.Port, path.Join(d.SFTP.RemoteDir, rel))
	}
	return localPath(d.Path, rel)
}

// PutBytes writes a small file (sidecar) through the normal no-overwrite commit path.
func PutBytes(b Backend, rel string, data []byte) error {
	up, err := b.Create(rel)
	if err != nil {
		return err
	}
	if _, err := io.Copy(up, bytes.NewReader(data)); err != nil {
		up.Abort()
		return err
	}
	return up.Commit()
}

// TestResult is the outcome of a destination test.
type TestResult struct {
	HostKeyFingerprint string `json:"host_key_fingerprint,omitempty"`
}

// TestError is a secret-free, user-facing test failure with a machine code.
type TestError struct {
	Code string // auth_failed | not_found | unreachable | permission_denied | host_key_mismatch
	Msg  string
}

func (e *TestError) Error() string { return e.Msg }

// Test checks that the destination accepts write, read and delete of a probe file (FR-006).
// For SFTP it also returns the server host key fingerprint; with an empty pinned fingerprint the
// key is accepted so the user can confirm it.
func Test(ctx context.Context, d store.Destination) (TestResult, error) {
	var res TestResult
	if d.Type == "sftp" {
		fp, err := ProbeHostKey(ctx, d.SFTP.Host, d.SFTP.Port)
		if err != nil {
			return res, classify(err)
		}
		res.HostKeyFingerprint = fp
		if d.SFTP.HostKeyFingerprint == "" {
			c := *d.SFTP
			c.HostKeyFingerprint = fp
			d.SFTP = &c
		} else if fp != d.SFTP.HostKeyFingerprint {
			return res, &TestError{"host_key_mismatch", "server host key " + fp + " does not match the pinned " + d.SFTP.HostKeyFingerprint}
		}
	}
	b, err := Open(ctx, d)
	if err != nil {
		return res, classify(err)
	}
	defer b.Close()
	probe := ".dumpling-probe-" + store.NewID()
	if err := PutBytes(b, probe, []byte("dumpling")); err != nil {
		return res, classify(err)
	}
	r, err := b.Open(probe)
	if err == nil {
		_, err = io.ReadAll(r)
		r.Close()
	}
	if rmErr := b.Remove(probe); err == nil {
		err = rmErr
	}
	if err != nil {
		return res, classify(err)
	}
	return res, nil
}

// classify maps backend errors to TestError codes. Messages come from the remote/OS and never
// contain our secrets (credentials are not part of error strings of these libraries).
func classify(err error) error {
	var te *TestError
	if errors.As(err, &te) {
		return te
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case errors.Is(err, fs.ErrNotExist) || strings.Contains(low, "nosuchbucket") || strings.Contains(low, "bucket does not exist"):
		return &TestError{"not_found", msg}
	case errors.Is(err, fs.ErrPermission) || strings.Contains(low, "permission denied") || strings.Contains(low, "accessdenied") || strings.Contains(low, "access denied"):
		return &TestError{"permission_denied", msg}
	case strings.Contains(low, "unable to authenticate") || strings.Contains(low, "invalidaccesskeyid") ||
		strings.Contains(low, "signaturedoesnotmatch") || strings.Contains(low, "access key id") || strings.Contains(low, "signature"):
		return &TestError{"auth_failed", msg}
	case strings.Contains(low, "host key mismatch"):
		return &TestError{"host_key_mismatch", msg}
	}
	return &TestError{"unreachable", msg}
}
