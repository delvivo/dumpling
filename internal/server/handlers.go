package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/delvivo/dumpling/internal/engine"
	"github.com/delvivo/dumpling/internal/runner"
	"github.com/delvivo/dumpling/internal/schedule"
	"github.com/delvivo/dumpling/internal/storage"
	"github.com/delvivo/dumpling/internal/store"
)

// ---- system ----

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"version": s.Version, "tools": engine.Tools(), "tls": s.TLS, "bind": s.Bind})
}

// ---- connections ----

func validateConn(c *store.Connection) error {
	switch {
	case len(c.Name) < 1 || len(c.Name) > 64:
		return fmt.Errorf("name must be 1–64 characters")
	case c.Engine != "postgres" && c.Engine != "mysql" && c.Engine != "mssql":
		return fmt.Errorf("engine must be postgres, mysql or mssql")
	case c.Host == "":
		return fmt.Errorf("host required")
	case c.Port < 1 || c.Port > 65535:
		return fmt.Errorf("port must be 1–65535")
	case c.Username == "":
		return fmt.Errorf("username required")
	}
	if c.Engine == "mssql" {
		if c.SharePathServer == "" || c.SharePathLocal == "" {
			return fmt.Errorf("SQL Server connections need both share paths")
		}
		if st, err := os.Stat(c.SharePathLocal); err != nil || !st.IsDir() {
			return fmt.Errorf("local share path %s does not exist", c.SharePathLocal)
		}
	} else {
		c.SharePathServer, c.SharePathLocal = "", ""
	}
	return nil
}

func public(c store.Connection) store.Connection { c.Password = ""; return c }

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	cs, err := s.St.Connections()
	if err != nil {
		fail(w, err)
		return
	}
	for i := range cs {
		cs[i] = public(cs[i])
	}
	writeJSON(w, 200, cs)
}

func (s *Server) createConnection(w http.ResponseWriter, r *http.Request) {
	var c store.Connection
	if !readJSON(w, r, &c) {
		return
	}
	c.ID, c.ServerVersion, c.TestedAt = "", "", ""
	if err := validateConn(&c); err != nil {
		writeErr(w, 400, "invalid", err.Error())
		return
	}
	if err := s.St.SaveConnection(&c); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, public(c))
}

// mergeConn applies an update body onto the stored connection; empty password keeps the stored one.
func (s *Server) mergeConn(id string, in store.Connection) (store.Connection, error) {
	old, err := s.St.Connection(id)
	if err != nil {
		return in, err
	}
	in.ID = id
	if in.Password == "" {
		in.Password = old.Password
	}
	in.ServerVersion, in.TestedAt = old.ServerVersion, old.TestedAt
	return in, nil
}

func (s *Server) updateConnection(w http.ResponseWriter, r *http.Request) {
	var in store.Connection
	if !readJSON(w, r, &in) {
		return
	}
	c, err := s.mergeConn(r.PathValue("id"), in)
	if err != nil {
		fail(w, err)
		return
	}
	if err := validateConn(&c); err != nil {
		writeErr(w, 400, "invalid", err.Error())
		return
	}
	if err := s.St.SaveConnection(&c); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, public(c))
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	if err := s.St.DeleteConnection(r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}

// testConnection accepts an unsaved connection, or {"id": ...} (fields given override the stored ones).
func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	var c store.Connection
	if !readJSON(w, r, &c) {
		return
	}
	if c.ID != "" {
		stored, err := s.St.Connection(c.ID)
		if err != nil {
			fail(w, err)
			return
		}
		if c.Engine == "" { // bare {"id"}: test what is saved
			c = stored
		} else if c, err = s.mergeConn(c.ID, c); err != nil {
			fail(w, err)
			return
		}
	}
	if c.Name == "" {
		c.Name = "test"
	}
	if err := validateConn(&c); err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error(), "databases": []string{}, "warnings": []string{}})
		return
	}
	res, err := s.test(r.Context(), c)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": redact(err.Error(), c.Password), "databases": []string{}, "warnings": nonNil(res.Warnings)})
		return
	}
	if c.ID != "" {
		s.St.SetConnectionVersion(c.ID, res.Version)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "server_version": res.Version, "databases": nonNil(res.Databases), "warnings": nonNil(res.Warnings)})
}

// nonNil keeps the API contract "always an array": a nil slice would encode as null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Server) test(ctx context.Context, c store.Connection) (engine.TestResult, error) {
	eng, err := engine.For(c.Engine)
	if err != nil {
		return engine.TestResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return eng.Test(ctx, s.R.EngineConn(c))
}

func redact(msg, secret string) string {
	if secret == "" {
		return msg
	}
	return strings.ReplaceAll(msg, secret, "***")
}

func (s *Server) connectionDatabases(w http.ResponseWriter, r *http.Request) {
	c, err := s.St.Connection(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	res, err := s.test(r.Context(), c)
	if err != nil {
		writeErr(w, 502, "unreachable", redact(err.Error(), c.Password))
		return
	}
	writeJSON(w, 200, res.Databases)
}

// ---- destinations ----

func (s *Server) listDestinations(w http.ResponseWriter, r *http.Request) {
	ds, err := s.St.Destinations()
	if err != nil {
		fail(w, err)
		return
	}
	for i := range ds {
		if ds[i].Type == "local" {
			if free, err := diskFree(ds[i].Path); err == nil {
				ds[i].FreeBytes = &free
			}
		}
		ds[i] = ds[i].Public()
	}
	writeJSON(w, 200, ds)
}

// prepareDest normalizes input, merges stored secrets and validates; checkPin=false lets a test run
// before the SFTP host key has been confirmed.
func (s *Server) prepareDest(w http.ResponseWriter, d *store.Destination, checkPin bool) bool {
	err := s.St.PrepareDestination(d)
	if err == nil {
		v := *d
		if !checkPin && v.SFTP != nil && v.SFTP.HostKeyFingerprint == "" {
			c := *v.SFTP
			c.HostKeyFingerprint = "unconfirmed"
			v.SFTP = &c
		}
		err = v.Validate()
	}
	var ve store.ValidationError
	switch {
	case err == nil:
		return true
	case errors.As(err, &ve):
		writeErr(w, 400, "invalid", ve.Error())
	case errors.Is(err, store.ErrLocationLocked):
		writeErr(w, 409, "location_locked", "type and location cannot change while the destination holds backups")
	default:
		fail(w, err)
	}
	return false
}

// testDest checks a destination is usable: local paths must exist and be writable (never created),
// remote ones accept a probe write/read/delete. Errors are written to w.
func (s *Server) testDest(w http.ResponseWriter, r *http.Request, d store.Destination) (storage.TestResult, bool) {
	if d.Type == "local" {
		f, err := os.CreateTemp(filepath.Clean(d.Path), ".dumpling-write-test-*")
		if err != nil {
			writeErr(w, 400, "not_writable", "path must exist and be writable: "+err.Error())
			return storage.TestResult{}, false
		}
		f.Close()
		os.Remove(f.Name())
		return storage.TestResult{}, true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := storage.Test(ctx, d)
	if err != nil {
		msg, code := err.Error(), "unreachable"
		var te *storage.TestError
		if errors.As(err, &te) {
			code = te.Code
		}
		for _, sec := range d.Secrets() {
			msg = redact(msg, sec)
		}
		status := "test_failed"
		if code == "host_key_mismatch" {
			status = code
		}
		writeJSON(w, 400, map[string]any{"error": msg, "code": status, "detail": code,
			"host_key_fingerprint": res.HostKeyFingerprint})
		return res, false
	}
	return res, true
}

func (s *Server) testDestination(w http.ResponseWriter, r *http.Request) {
	var d store.Destination
	if !readJSON(w, r, &d) || !s.prepareDest(w, &d, false) {
		return
	}
	if res, ok := s.testDest(w, r, d); ok {
		writeJSON(w, 200, map[string]any{"ok": true, "host_key_fingerprint": res.HostKeyFingerprint})
	}
}

type destView struct {
	store.Destination
	Rescan *runner.RescanResult `json:"rescan,omitempty"`
}

func (s *Server) saveDestination(w http.ResponseWriter, r *http.Request) {
	var d store.Destination
	if !readJSON(w, r, &d) {
		return
	}
	d.ID = r.PathValue("id")
	if !s.prepareDest(w, &d, true) {
		return
	}
	// SFTP: Test re-probes the server and rejects a key different from the confirmed one (FR-004).
	if _, ok := s.testDest(w, r, d); !ok {
		return
	}
	created := d.ID == ""
	if err := s.St.SaveDestination(&d); err != nil {
		fail(w, err)
		return
	}
	v := destView{Destination: d.Public()}
	status := 200
	if created { // import backups already on the destination (FR-016)
		status = 201
		if res, err := s.R.Rescan(d); err == nil {
			v.Rescan = &res
		} else {
			slog.Warn("rescan after create", "destination", d.Name, "err", err)
		}
	}
	if saved, err := s.St.Destination(d.ID); err == nil {
		v.Destination = saved.Public()
	}
	writeJSON(w, status, v)
}

func (s *Server) rescanDestination(w http.ResponseWriter, r *http.Request) {
	d, err := s.St.Destination(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	res, err := s.R.Rescan(d)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) deleteDestination(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.St.DeleteDestination(id); errors.Is(err, store.ErrInUse) {
		plans, n, _ := s.St.DestinationUsage(id)
		writeJSON(w, 409, map[string]any{"error": "still used by plans or backups", "code": "in_use",
			"detail": map[string]any{"plans": plans, "backup_count": n}})
		return
	} else if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}

// ---- plans ----

type planView struct {
	store.Plan
	NextRun       string           `json:"next_run"`
	LastOperation *store.Operation `json:"last_operation"`
}

func view(s *Server, p store.Plan) planView {
	v := planView{Plan: p}
	if n := schedule.Next(p, time.Now()); !n.IsZero() {
		v.NextRun = n.UTC().Format(time.RFC3339)
	}
	if o := s.St.LastOperation(p.ID); o.ID != "" {
		v.LastOperation = &o
	}
	return v
}

func (s *Server) listPlans(w http.ResponseWriter, r *http.Request) {
	ps, err := s.St.Plans()
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]planView, len(ps))
	for i, p := range ps {
		out[i] = view(s, p)
	}
	writeJSON(w, 200, out)
}

func (s *Server) savePlan(w http.ResponseWriter, r *http.Request) {
	var p store.Plan
	if !readJSON(w, r, &p) {
		return
	}
	p.ID = r.PathValue("id")
	switch {
	case len(p.Name) < 1 || len(p.Name) > 64:
		writeErr(w, 400, "invalid", "name must be 1–64 characters")
		return
	case len(p.Databases) < 1:
		writeErr(w, 400, "invalid", "select at least one database")
		return
	}
	if _, err := schedule.Parse(p.Schedule); err != nil {
		writeErr(w, 400, "invalid_schedule", "invalid cron schedule: "+err.Error())
		return
	}
	if err := schedule.ValidRetention(p.Retention); err != nil {
		writeErr(w, 400, "invalid_retention", err.Error())
		return
	}
	if _, err := s.St.Connection(p.ConnectionID); err != nil {
		writeErr(w, 400, "invalid", "unknown connection")
		return
	}
	if _, err := s.St.Destination(p.DestinationID); err != nil {
		writeErr(w, 400, "invalid", "unknown destination")
		return
	}
	status := 200
	if p.ID == "" {
		status = 201
	}
	if err := s.St.SavePlan(&p); err != nil {
		fail(w, err)
		return
	}
	s.Reload()
	saved, err := s.St.Plan(p.ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, status, view(s, saved))
}

func (s *Server) deletePlan(w http.ResponseWriter, r *http.Request) {
	if err := s.St.DeletePlan(r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	s.Reload()
	w.WriteHeader(204)
}

func (s *Server) runPlan(w http.ResponseWriter, r *http.Request) {
	p, err := s.St.Plan(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	ids, err := s.R.RunPlan(p)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"operation_ids": ids})
}

func (s *Server) pausePlan(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.St.SetPlanPaused(r.PathValue("id"), paused); err != nil {
			fail(w, err)
			return
		}
		s.Reload()
		w.WriteHeader(204)
	}
}

// ---- backups ----

func (s *Server) createBackup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ConnectionID  string   `json:"connection_id"`
		Databases     []string `json:"databases"`
		DestinationID string   `json:"destination_id"`
		Passphrase    string   `json:"passphrase"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if len(in.Databases) == 0 {
		writeErr(w, 400, "invalid", "select at least one database")
		return
	}
	c, err := s.St.Connection(in.ConnectionID)
	if err != nil {
		writeErr(w, 400, "invalid", "unknown connection")
		return
	}
	d, err := s.St.Destination(in.DestinationID)
	if err != nil {
		writeErr(w, 400, "invalid", "unknown destination")
		return
	}
	ids, err := s.R.StartBackup(runner.BackupReq{Conn: c, Databases: in.Databases, Dest: d, Passphrase: in.Passphrase})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"operation_ids": ids})
}

// withLocation fills destination type and display location (FR-015).
func (s *Server) withLocation(bs []store.Backup) {
	dests := map[string]store.Destination{}
	for i := range bs {
		d, ok := dests[bs[i].DestinationID]
		if !ok {
			d, _ = s.St.Destination(bs[i].DestinationID)
			dests[bs[i].DestinationID] = d
		}
		if d.ID != "" {
			bs[i].DestinationType, bs[i].Location = d.Type, storage.Location(d, bs[i].Path)
		}
	}
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bs, err := s.St.Backups(store.BackupFilter{PlanID: q.Get("plan_id"), ConnectionID: q.Get("connection_id"),
		Database: q.Get("database"), Status: q.Get("status"), DestinationID: q.Get("destination_id")})
	if err != nil {
		fail(w, err)
		return
	}
	s.withLocation(bs)
	writeJSON(w, 200, bs)
}

func (s *Server) getBackup(w http.ResponseWriter, r *http.Request) {
	b, err := s.St.Backup(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	bs := []store.Backup{b}
	s.withLocation(bs)
	writeJSON(w, 200, bs[0])
}

func (s *Server) unlockBackup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Passphrase    string `json:"passphrase"`
		ApplyToOrigin bool   `json:"apply_to_origin"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ids, err := s.R.Unlock(r.PathValue("id"), in.Passphrase, in.ApplyToOrigin)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"unlocked": ids})
}

func (s *Server) verifyBackup(w http.ResponseWriter, r *http.Request) {
	id, err := s.R.StartVerify(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 202, map[string]string{"operation_id": id})
}

func (s *Server) deleteBackup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.R.DeleteBackup(r.PathValue("id"), in.Confirm); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}

// ---- restores ----

func (s *Server) preflight(w http.ResponseWriter, r *http.Request) {
	var req runner.RestoreReq
	if !readJSON(w, r, &req) {
		return
	}
	pf, err := s.R.Preflight(req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, pf)
}

func (s *Server) restore(w http.ResponseWriter, r *http.Request) {
	var req runner.RestoreReq
	if !readJSON(w, r, &req) {
		return
	}
	req.User = user(r)
	id, err := s.R.StartRestore(req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 202, map[string]string{"operation_id": id})
}

// ---- operations ----

func (s *Server) listOperations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	ops, err := s.St.Operations(store.OpFilter{Type: q.Get("type"), Status: q.Get("status"), PlanID: q.Get("plan_id"),
		ConnectionID: q.Get("connection_id"), Before: q.Get("before"), Limit: limit})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, ops)
}

func (s *Server) getOperation(w http.ResponseWriter, r *http.Request) {
	o, err := s.St.Operation(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, o)
}

func (s *Server) operationLog(w http.ResponseWriter, r *http.Request) {
	o, err := s.St.Operation(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	b, err := os.ReadFile(runner.LogPath(s.R.DataDir, o.ID))
	if err != nil && !os.IsNotExist(err) {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(b)
}
