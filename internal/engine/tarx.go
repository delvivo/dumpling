package engine

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// untar extracts a tar stream into dir (which must exist). Artifacts can come from destinations
// other parties write to, so only regular files and directories inside dir are accepted: absolute
// names, ".." components, links and special files are rejected.
func untar(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := filepath.FromSlash(h.Name)
		if filepath.IsAbs(name) || filepath.VolumeName(name) != "" || strings.HasPrefix(h.Name, "/") {
			return fmt.Errorf("tar: absolute path %q", h.Name)
		}
		for _, part := range strings.Split(h.Name, "/") {
			if part == ".." {
				return fmt.Errorf("tar: path escapes target: %q", h.Name)
			}
		}
		dst := filepath.Join(dir, name)
		if rel, err := filepath.Rel(dir, dst); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("tar: path escapes target: %q", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("tar: unsupported entry %q (type %c)", h.Name, h.Typeflag)
		}
	}
}
