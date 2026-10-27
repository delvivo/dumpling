package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/delvivo/dumpling/internal/store"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// pkg/sftp maps SSH_FX_NO_SUCH_FILE to os.ErrNotExist (== fs.ErrNotExist).
type sftpb struct {
	conn *ssh.Client
	c    *sftp.Client
	root string
}

var errHostKeyMismatch = errors.New("host key mismatch")

func dialSSH(ctx context.Context, host string, port int, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	d := net.Dialer{Timeout: 15 * time.Second}
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = 15 * time.Second
	sc, chans, reqs, err := ssh.NewClientConn(nc, addr, cfg)
	if err != nil {
		nc.Close()
		return nil, err
	}
	return ssh.NewClient(sc, chans, reqs), nil
}

// ProbeHostKey returns the server's SHA256 host key fingerprint without authenticating.
func ProbeHostKey(ctx context.Context, host string, port int) (string, error) {
	var fp string
	cfg := &ssh.ClientConfig{
		User: "dumpling-probe",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp = ssh.FingerprintSHA256(key)
			return errors.New("probe done")
		},
	}
	c, err := dialSSH(ctx, host, port, cfg)
	if c != nil {
		c.Close()
	}
	if fp == "" {
		return "", err
	}
	return fp, nil
}

func openSFTP(ctx context.Context, cfg *store.SFTPConfig) (Backend, error) {
	var auth ssh.AuthMethod
	if cfg.Auth == "key" {
		var signer ssh.Signer
		var err error
		if cfg.KeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(cfg.PrivateKey), []byte(cfg.KeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		}
		if err != nil {
			return nil, errors.New("invalid private key or key passphrase")
		}
		auth = ssh.PublicKeys(signer)
	} else {
		auth = ssh.Password(cfg.Password)
	}
	pinned := cfg.HostKeyFingerprint
	conn, err := dialSSH(ctx, cfg.Host, cfg.Port, &ssh.ClientConfig{
		User: cfg.Username,
		Auth: []ssh.AuthMethod{auth},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if fp := ssh.FingerprintSHA256(key); fp != pinned {
				return fmt.Errorf("%w: server presented %s, pinned %s", errHostKeyMismatch, fp, pinned)
			}
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	c, err := sftp.NewClient(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &sftpb{conn: conn, c: c, root: cfg.RemoteDir}, nil
}

func (b *sftpb) abs(rel string) string { return path.Join(b.root, rel) }

type sftpUpload struct {
	b     *sftpb
	f     *sftp.File
	final string
	done  bool
}

func (b *sftpb) Create(rel string) (Upload, error) {
	final := b.abs(rel)
	if err := b.c.MkdirAll(path.Dir(final)); err != nil {
		return nil, err
	}
	if _, err := b.c.Stat(final); err == nil {
		return nil, fmt.Errorf("artifact %s already exists", final)
	}
	f, err := b.c.OpenFile(final+PartialSuffix, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return nil, err
	}
	return &sftpUpload{b: b, f: f, final: final}, nil
}

func (u *sftpUpload) Write(p []byte) (int, error) { return u.f.Write(p) }

// Commit closes the partial file and renames it; SFTP v3 rename fails if the target exists, so an
// artifact is never overwritten.
func (u *sftpUpload) Commit() error {
	if err := u.f.Close(); err != nil {
		u.Abort()
		return err
	}
	if err := u.b.c.Rename(u.final+PartialSuffix, u.final); err != nil {
		u.Abort()
		return err
	}
	u.done = true
	return nil
}

func (u *sftpUpload) Abort() {
	if u.done {
		return
	}
	u.done = true
	u.f.Close()
	u.b.c.Remove(u.final + PartialSuffix)
}

func (b *sftpb) Open(rel string) (io.ReadCloser, error) {
	f, err := b.c.Open(b.abs(rel))
	return f, err
}

func (b *sftpb) Stat(rel string) (int64, error) {
	st, err := b.c.Stat(b.abs(rel))
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func (b *sftpb) Remove(rel string) error { return b.c.Remove(b.abs(rel)) }

func (b *sftpb) walk(fn func(abs, rel string)) error {
	w := b.c.Walk(b.root)
	for w.Step() {
		if w.Err() != nil {
			if w.Path() == b.root {
				return w.Err()
			}
			continue
		}
		if w.Stat().IsDir() {
			continue
		}
		fn(w.Path(), strings.TrimPrefix(strings.TrimPrefix(w.Path(), b.root), "/"))
	}
	return nil
}

func (b *sftpb) List(suffix string) ([]string, error) {
	out := []string{}
	err := b.walk(func(_, rel string) {
		if strings.HasSuffix(rel, suffix) && !strings.HasSuffix(rel, PartialSuffix) {
			out = append(out, rel)
		}
	})
	return out, err
}

func (b *sftpb) Cleanup() error {
	return b.walk(func(abs, _ string) {
		if strings.HasSuffix(abs, PartialSuffix) {
			b.c.Remove(abs)
		}
	})
}

func (b *sftpb) Close() error {
	b.c.Close()
	return b.conn.Close()
}
