package storage

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type local struct{ root string }

func localPath(root, rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

type localUpload struct {
	f     *os.File
	final string
	done  bool
}

func (l *local) Create(rel string) (Upload, error) {
	final := localPath(l.root, rel)
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
	return &localUpload{f: f, final: final}, nil
}

func (u *localUpload) Write(p []byte) (int, error) { return u.f.Write(p) }

// Commit fsyncs and links the partial file to its final name; Link fails if the name exists, so an
// artifact is never overwritten.
func (u *localUpload) Commit() error {
	if err := u.f.Sync(); err != nil {
		u.Abort()
		return err
	}
	if err := u.f.Close(); err != nil {
		u.Abort()
		return err
	}
	if err := os.Link(u.final+PartialSuffix, u.final); err != nil {
		u.Abort()
		return err
	}
	os.Remove(u.final + PartialSuffix)
	u.done = true
	return nil
}

func (u *localUpload) Abort() {
	if u.done {
		return
	}
	u.done = true
	u.f.Close()
	os.Remove(u.final + PartialSuffix)
}

func (l *local) Open(rel string) (io.ReadCloser, error) { return os.Open(localPath(l.root, rel)) }

func (l *local) Stat(rel string) (int64, error) {
	st, err := os.Stat(localPath(l.root, rel))
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func (l *local) Remove(rel string) error { return os.Remove(localPath(l.root, rel)) }

func (l *local) walk(fn func(p, rel string)) error {
	err := filepath.WalkDir(l.root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(l.root, p)
		fn(p, filepath.ToSlash(rel))
		return nil
	})
	if _, statErr := os.Stat(l.root); statErr != nil {
		return statErr
	}
	return err
}

func (l *local) List(suffix string) ([]string, error) {
	out := []string{}
	err := l.walk(func(_, rel string) {
		if strings.HasSuffix(rel, suffix) && !strings.HasSuffix(rel, PartialSuffix) {
			out = append(out, rel)
		}
	})
	return out, err
}

func (l *local) Cleanup() error {
	return l.walk(func(p, _ string) {
		if strings.HasSuffix(p, PartialSuffix) {
			os.Remove(p)
		}
	})
}

func (l *local) Close() error { return nil }
