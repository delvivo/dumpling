package storage

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/delvivo/dumpling/internal/store"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sftpServer runs an in-process SSH server with an in-memory SFTP filesystem. It accepts password
// "pw" and the given client public key.
func sftpServer(t *testing.T, clientKey ssh.PublicKey) (host string, port int, fingerprint string) {
	t.Helper()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(hostPriv)
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == "pw" {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if clientKey != nil && bytes.Equal(k.Marshal(), clientKey.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	fs := sftp.InMemHandler() // shared across connections
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nch := range chans {
					ch, creqs, _ := nch.Accept()
					go func() {
						for req := range creqs {
							ok := req.Type == "subsystem" && string(req.Payload[4:]) == "sftp"
							req.Reply(ok, nil)
							if ok {
								srv := sftp.NewRequestServer(ch, fs)
								srv.Serve()
								srv.Close()
							}
						}
					}()
				}
			}()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, ssh.FingerprintSHA256(signer.PublicKey())
}

func TestSFTPHostKeyPinning(t *testing.T) {
	host, port, fp := sftpServer(t, nil)
	ctx := context.Background()
	if got, err := ProbeHostKey(ctx, host, port); err != nil || got != fp {
		t.Fatalf("probe %q %v, want %q", got, err, fp)
	}
	cfg := store.SFTPConfig{Host: host, Port: port, Username: "u", Auth: "password", Password: "pw", RemoteDir: "/up", HostKeyFingerprint: fp}
	d := store.Destination{Type: "sftp", SFTP: &cfg}
	if res, err := Test(ctx, d); err != nil || res.HostKeyFingerprint != fp {
		t.Fatalf("test %+v %v", res, err)
	}
	b, err := Open(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	put(t, b, "a/x.dump", []byte("data"))
	if _, err := b.Create("a/x.dump"); err == nil {
		t.Fatal("overwrite must fail")
	}
	b.Close()

	bad := cfg
	bad.HostKeyFingerprint = "SHA256:somethingelse"
	if _, err := Open(ctx, store.Destination{Type: "sftp", SFTP: &bad}); err == nil || !strings.Contains(err.Error(), "host key mismatch") {
		t.Fatalf("mismatch: %v", err)
	}
	var te *TestError
	if _, err := Test(ctx, store.Destination{Type: "sftp", SFTP: &bad}); !errors.As(err, &te) || te.Code != "host_key_mismatch" {
		t.Fatalf("test mismatch: %v", err)
	}
	b, _ = Open(ctx, d)
	defer b.Close()
	if l, _ := b.List(""); len(l) != 1 {
		t.Fatalf("nothing must be written on mismatch: %v", l)
	}

	wrong := cfg
	wrong.Password = "nope"
	if _, err := Test(ctx, store.Destination{Type: "sftp", SFTP: &wrong}); !errors.As(err, &te) || te.Code != "auth_failed" {
		t.Fatalf("wrong password: %v", err)
	}
}

func TestSFTPPrivateKey(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	host, port, fp := sftpServer(t, sshPub)
	plain, _ := ssh.MarshalPrivateKey(priv, "")
	enc, _ := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("kp"))
	for name, c := range map[string]store.SFTPConfig{
		"plain":      {PrivateKey: string(pem.EncodeToMemory(plain))},
		"passphrase": {PrivateKey: string(pem.EncodeToMemory(enc)), KeyPassphrase: "kp"},
	} {
		t.Run(name, func(t *testing.T) {
			c.Host, c.Port, c.Username, c.Auth, c.RemoteDir, c.HostKeyFingerprint = host, port, "u", "key", "/k", fp
			b, err := Open(context.Background(), store.Destination{Type: "sftp", SFTP: &c})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			put(t, b, name+".dump", []byte("k"))
			r, err := b.Open(name + ".dump")
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(r)
			r.Close()
			if string(got) != "k" {
				t.Fatalf("got %q", got)
			}
		})
	}
}
