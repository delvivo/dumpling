package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	cookieName = "dumpling_session"
	sessionTTL = 7 * 24 * time.Hour
)

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy"), bcrypt.DefaultCost)

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) {
	n, err := s.St.UserCount()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"needed": n == 0})
}

func (s *Server) postSetup(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	if n, _ := s.St.UserCount(); n > 0 {
		writeErr(w, 409, "already_setup", "admin account already exists")
		return
	}
	if l := len(c.Username); l < 1 || l > 64 {
		writeErr(w, 400, "invalid", "username must be 1–64 characters")
		return
	}
	if len(c.Password) < 8 || len(c.Password) > 72 {
		writeErr(w, 400, "invalid", "password must be 8–72 characters")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.St.CreateUser(c.Username, string(hash)); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !s.logins.allow(ip) {
		slog.Warn("login rate limited", "remote", ip)
		writeErr(w, 429, "rate_limited", "too many login attempts, retry in a minute")
		return
	}
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	id, hash, err := s.St.UserByName(c.Username)
	if err != nil {
		hash = string(dummyHash) // same bcrypt cost for unknown users (no username probing by timing)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(c.Password)) != nil || err != nil {
		slog.Warn("login failed", "user", c.Username, "remote", ip)
		writeErr(w, 401, "unauthorized", "invalid username or password")
		return
	}
	tok := make([]byte, 32)
	rand.Read(tok)
	token := hex.EncodeToString(tok)
	if err := s.St.PutSession(hashToken(token), id, time.Now().Add(sessionTTL).UTC().Format(time.RFC3339)); err != nil {
		fail(w, err)
		return
	}
	s.setCookie(w, token, int(sessionTTL.Seconds()))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.St.DeleteSession(hashToken(c.Value))
	}
	s.setCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: s.TLS, SameSite: http.SameSiteStrictMode})
}

func hashToken(t string) string { h := sha256.Sum256([]byte(t)); return hex.EncodeToString(h[:]) }

// auth requires a valid session and slides its expiry.
func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			writeErr(w, 401, "unauthorized", "login required")
			return
		}
		th := hashToken(c.Value)
		uid, name, err := s.St.Session(th)
		if err != nil {
			writeErr(w, 401, "unauthorized", "session expired")
			return
		}
		s.St.PutSession(th, uid, time.Now().Add(sessionTTL).UTC().Format(time.RFC3339))
		next(w, withUser(r, name))
	})
}

// limiter allows n events per IP per minute.
// ponytail: in-memory map never shrinks below active IPs; fine for a single-admin app.
type limiter struct {
	mu   sync.Mutex
	n    int
	hits map[string][]time.Time
}

func newLimiter(n int) *limiter { return &limiter{n: n, hits: map[string][]time.Time{}} }

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := time.Now().Add(-time.Minute)
	h := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cut) {
			h = append(h, t)
		}
	}
	if len(h) >= l.n {
		l.hits[key] = h
		return false
	}
	l.hits[key] = append(h, time.Now())
	return true
}
