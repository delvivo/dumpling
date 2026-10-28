package runner

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/delvivo/dumpling/internal/artifact"
	"github.com/delvivo/dumpling/internal/storage"
	"github.com/delvivo/dumpling/internal/store"
)

// PostgreSQL links keep their backup_manifest next to the artifact (<rel>.manifest, encrypted
// like the artifact) so imports keep working, plus a local cache the next incremental reads.

func (r *Runner) manifestCache(id string) string { return filepath.Join(r.DataDir, "manifests", id) }

// manifestRel is the destination path of a link's manifest, as recorded in its marker.
func manifestRel(b store.Backup) string {
	var m struct {
		Manifest string `json:"manifest"`
	}
	json.Unmarshal([]byte(b.Marker), &m)
	if m.Manifest == "" {
		return b.Path + ".manifest"
	}
	return m.Manifest
}

// storeManifest uploads state as <rel>.manifest and caches it under the backup id.
func (r *Runner) storeManifest(dst storage.Backend, b store.Backup, rel string, state *os.File, passphrase string) error {
	if _, err := state.Seek(0, io.SeekStart); err != nil {
		return err
	}
	up, err := dst.Create(rel + ".manifest")
	if err != nil {
		return err
	}
	w, err := artifact.Create(up, true, passphrase)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, state); err != nil {
		w.Abort()
		return err
	}
	if _, _, err := w.CommitBare(); err != nil {
		return err
	}
	if _, err := state.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return copyFile(state, r.manifestCache(b.ID))
}

// manifest returns a local file with the manifest of link b: the cache, or a download + decrypt
// from the destination (fresh install, imported chain) that refills the cache.
func (r *Runner) manifest(dst storage.Backend, b store.Backup, l *OpLog) (string, error) {
	cache := r.manifestCache(b.ID)
	if _, err := os.Stat(cache); err == nil {
		return cache, nil
	}
	l.Printf("manifest of %s not cached, fetching %s", b.ID, manifestRel(b))
	src, err := dst.Open(manifestRel(b))
	if err != nil {
		return "", err
	}
	pass, err := r.linkPassphrase(b, "")
	if err != nil {
		src.Close()
		return "", err
	}
	rc, err := artifact.Open(src, true, pass)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	if err := copyFile(rc, cache); err != nil {
		return "", err
	}
	return cache, nil
}

// removeManifest deletes a link's manifest object and cache (prune/delete).
func (r *Runner) removeManifest(dst storage.Backend, b store.Backup) {
	if !b.Physical() {
		return
	}
	dst.Remove(manifestRel(b))
	os.Remove(r.manifestCache(b.ID))
}

func copyFile(src io.Reader, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
