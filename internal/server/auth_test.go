package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/delvivo/dumpling/internal/runner"
	"github.com/delvivo/dumpling/internal/secret"
	"github.com/delvivo/dumpling/internal/store"
)

type client struct {
	t      *testing.T
	srv    *httptest.Server
	cookie *http.Cookie
}

func newTestServer(t *testing.T) (*client, *Server) {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"logs", "tmp"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	box, _ := secret.New(bytes.Repeat([]byte{9}, 32))
	st, err := store.Open(filepath.Join(dir, "dumpling.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	s := &Server{St: st, R: runner.New(context.Background(), st, dir, hub.Publish), Hub: hub}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); st.DB.Close() })
	return &client{t: t, srv: ts}, s
}

func (c *client) do(method, path, body string, csrf bool) (*http.Response, string) {
	req, _ := http.NewRequest(method, c.srv.URL+path, strings.NewReader(body))
	if csrf {
		req.Header.Set("X-Requested-With", "dumpling")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var b bytes.Buffer
	b.ReadFrom(res.Body)
	for _, ck := range res.Cookies() {
		if ck.Name == cookieName {
			c.cookie = ck
		}
	}
	return res, b.String()
}

func (c *client) login(t *testing.T) {
	c.do("POST", "/api/setup", `{"username":"admin","password":"correct-horse"}`, true)
	if res, _ := c.do("POST", "/api/login", `{"username":"admin","password":"correct-horse"}`, true); res.StatusCode != 204 {
		t.Fatalf("login %d", res.StatusCode)
	}
}

func TestAuthFlow(t *testing.T) {
	c, s := newTestServer(t)
	if res, body := c.do("GET", "/api/setup", "", false); !strings.Contains(body, `"needed":true`) {
		t.Fatalf("setup needed: %d %s", res.StatusCode, body)
	}
	if res, _ := c.do("POST", "/api/setup", `{"username":"admin","password":"correct-horse"}`, false); res.StatusCode != 403 {
		t.Fatalf("missing csrf header must be 403, got %d", res.StatusCode)
	}
	if res, _ := c.do("POST", "/api/setup", `{"username":"admin","password":"correct-horse"}`, true); res.StatusCode != 201 {
		t.Fatalf("setup %d", res.StatusCode)
	}
	if res, _ := c.do("POST", "/api/setup", `{"username":"evil","password":"correct-horse"}`, true); res.StatusCode != 409 {
		t.Fatalf("second setup must be 409, got %d", res.StatusCode)
	}
	if res, _ := c.do("GET", "/api/connections", "", false); res.StatusCode != 401 {
		t.Fatalf("unauthenticated %d", res.StatusCode)
	}
	if res, _ := c.do("POST", "/api/login", `{"username":"admin","password":"nope"}`, true); res.StatusCode != 401 {
		t.Fatalf("bad password %d", res.StatusCode)
	}
	c.login(t)
	if !c.cookie.HttpOnly || c.cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("cookie flags")
	}
	if res, _ := c.do("GET", "/api/connections", "", false); res.StatusCode != 200 {
		t.Fatalf("authenticated %d", res.StatusCode)
	}

	// Expired session is rejected.
	s.St.DB.Exec(`UPDATE sessions SET expires_at='2000-01-01T00:00:00Z'`)
	if res, _ := c.do("GET", "/api/connections", "", false); res.StatusCode != 401 {
		t.Fatalf("expired %d", res.StatusCode)
	}

	// Rate limit: 5 attempts per minute per IP (1 bad + 1 good used above).
	codes := []int{}
	for i := 0; i < 4; i++ {
		res, _ := c.do("POST", "/api/login", `{"username":"admin","password":"nope"}`, true)
		codes = append(codes, res.StatusCode)
	}
	if codes[len(codes)-1] != 429 {
		t.Fatalf("rate limit codes %v", codes)
	}
}
