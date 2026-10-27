// Command dumpling serves the database backup web UI.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/delvivo/dumpling/internal/runner"
	"github.com/delvivo/dumpling/internal/schedule"
	"github.com/delvivo/dumpling/internal/secret"
	"github.com/delvivo/dumpling/internal/server"
	"github.com/delvivo/dumpling/internal/store"
)

var version = "dev"

func main() {
	dataDir := flag.String("data-dir", "./data", "directory for state, logs and master key")
	bind := flag.String("bind", "127.0.0.1:9898", "listen address")
	cert := flag.String("tls-cert", "", "TLS certificate file (enables HTTPS with --tls-key)")
	key := flag.String("tls-key", "", "TLS private key file")
	flag.Parse()
	if err := run(*dataDir, *bind, *cert, *key); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(dataDir, bind, cert, key string) error {
	if (cert == "") != (key == "") {
		return errors.New("--tls-cert and --tls-key must be given together")
	}
	tls := cert != ""
	dataDir, _ = filepath.Abs(dataDir)
	for _, d := range []string{dataDir, filepath.Join(dataDir, "logs"), filepath.Join(dataDir, "tmp")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	k, err := secret.LoadKey(dataDir)
	if err != nil {
		return err
	}
	box, err := secret.New(k)
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dataDir, "dumpling.db"), box)
	if err != nil {
		return err
	}
	defer st.DB.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hub := server.NewHub()
	r := runner.New(ctx, st, dataDir, hub.Publish)
	if err := r.Recover(); err != nil {
		return err
	}
	sched := schedule.New(st, func(p store.Plan) {
		if _, err := r.RunPlan(p); err != nil {
			slog.Error("scheduled run", "plan", p.Name, "err", err)
		}
	})
	go sched.Loop(ctx)

	srv := &http.Server{
		Addr:              bind,
		Handler:           (&server.Server{St: st, R: r, Reload: sched.Reload, Hub: hub, TLS: tls, Bind: bind, Version: version}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if host, _, _ := net.SplitHostPort(bind); !tls && !isLoopback(host) {
		slog.Warn("!!! listening on a non-loopback address WITHOUT TLS: passwords and sessions travel in clear text. " +
			"Use --tls-cert/--tls-key or put a TLS reverse proxy in front.")
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("dumpling listening", "addr", bind, "tls", tls, "data_dir", dataDir, "version", version)
		if tls {
			errc <- srv.ListenAndServeTLS(cert, key)
		} else {
			errc <- srv.ListenAndServe()
		}
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down; running operations are cancelled")
	shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shut)
	r.Wait()
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
