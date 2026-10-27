// Package server exposes the JSON API (contracts/api.md), SSE events and the embedded UI.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/delvivo/dumpling/internal/runner"
	"github.com/delvivo/dumpling/internal/store"
	"github.com/delvivo/dumpling/web"
)

type Server struct {
	St      *store.Store
	R       *runner.Runner
	Reload  func() // scheduler reload after plan changes
	Hub     *Hub
	TLS     bool
	Bind    string
	Version string
	logins  *limiter
}

func (s *Server) Handler() http.Handler {
	s.logins = newLimiter(5)
	if s.Reload == nil {
		s.Reload = func() {}
	}
	mux := http.NewServeMux()
	public := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, h) }
	api := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.auth(h)) }

	public("GET /api/setup", s.getSetup)
	public("POST /api/setup", s.postSetup)
	public("POST /api/login", s.login)
	api("POST /api/logout", s.logout)
	api("GET /api/me", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"username": user(r)}) })
	api("GET /api/system", s.system)
	api("GET /api/events", s.Hub.ServeHTTP)

	api("GET /api/connections", s.listConnections)
	api("POST /api/connections", s.createConnection)
	api("POST /api/connections/test", s.testConnection)
	api("PUT /api/connections/{id}", s.updateConnection)
	api("DELETE /api/connections/{id}", s.deleteConnection)
	api("GET /api/connections/{id}/databases", s.connectionDatabases)

	api("GET /api/destinations", s.listDestinations)
	api("POST /api/destinations", s.saveDestination)
	api("PUT /api/destinations/{id}", s.saveDestination)
	api("DELETE /api/destinations/{id}", s.deleteDestination)

	api("GET /api/plans", s.listPlans)
	api("POST /api/plans", s.savePlan)
	api("PUT /api/plans/{id}", s.savePlan)
	api("DELETE /api/plans/{id}", s.deletePlan)
	api("POST /api/plans/{id}/run", s.runPlan)
	api("POST /api/plans/{id}/pause", s.pausePlan(true))
	api("POST /api/plans/{id}/resume", s.pausePlan(false))

	api("POST /api/backups", s.createBackup)
	api("GET /api/backups", s.listBackups)
	api("GET /api/backups/{id}", s.getBackup)
	api("POST /api/backups/{id}/verify", s.verifyBackup)
	api("DELETE /api/backups/{id}", s.deleteBackup)

	api("POST /api/restores/preflight", s.preflight)
	api("POST /api/restores", s.restore)

	api("GET /api/operations", s.listOperations)
	api("GET /api/operations/{id}", s.getOperation)
	api("GET /api/operations/{id}/log", s.operationLog)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, 404, "not_found", "no such endpoint") })
	mux.Handle("/", spa())
	return csrf(mux)
}

// csrf requires a custom header on state-changing API requests (cross-site forms cannot set it).
func csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet && r.Method != http.MethodHead &&
			r.Header.Get("X-Requested-With") != "dumpling" {
			writeErr(w, http.StatusForbidden, "csrf", "missing X-Requested-With header")
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func spa() http.Handler {
	dist, _ := fs.Sub(web.FS, "dist/ui")
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(dist, "index.html"); err != nil {
			http.Error(w, "UI not built: run `npm ci && npm run build` in web/, then rebuild the binary.", http.StatusServiceUnavailable)
			return
		}
		if _, err := fs.Stat(dist, strings.TrimPrefix(r.URL.Path, "/")); err != nil || r.URL.Path == "/" {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// fail maps domain errors to API errors.
func fail(w http.ResponseWriter, err error) {
	var re *runner.Error
	switch {
	case errors.As(err, &re):
		body := map[string]any{"error": re.Msg, "code": re.Code}
		for k, v := range re.Extra {
			body[k] = v
		}
		writeJSON(w, re.Status, body)
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, 404, "not_found", "not found")
	case errors.Is(err, store.ErrDuplicate):
		writeErr(w, 409, "duplicate", "name already exists")
	case errors.Is(err, store.ErrInUse):
		writeErr(w, 409, "in_use", "still referenced by plans or backups")
	default:
		slog.Error("request failed", "err", err)
		writeErr(w, 500, "internal", err.Error())
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, 400, "bad_json", "invalid request body: "+err.Error())
		return false
	}
	return true
}

type ctxKey struct{}

func user(r *http.Request) string { s, _ := r.Context().Value(ctxKey{}).(string); return s }

func withUser(r *http.Request, name string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKey{}, name))
}
