package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestNoSecretLeaks: a known password/passphrase must not appear in API responses, logs or the raw
// database file (SC-005). Operations fail (unreachable server) to exercise error paths.
func TestNoSecretLeaks(t *testing.T) {
	const pw, pass = "Leak-Canary-Pw-91", "Leak-Canary-Pass-37"
	c, s := newTestServer(t)
	c.login(t)
	var bodies []string
	call := func(method, path, body string) string {
		_, b := c.do(method, path, body, true)
		bodies = append(bodies, b)
		return b
	}

	share := t.TempDir()
	dest := t.TempDir()
	conn := map[string]any{"name": "ms", "engine": "mssql", "host": "127.0.0.1", "port": 1, "username": "sa", "password": pw,
		"tls_mode": "disable", "share_path_server": "/share", "share_path_local": share}
	cb, _ := json.Marshal(conn)
	var created struct{ ID string }
	json.Unmarshal([]byte(call("POST", "/api/connections", string(cb))), &created)
	call("POST", "/api/connections/test", string(cb))
	call("POST", "/api/connections/test", `{"id":"`+created.ID+`"}`)
	call("GET", "/api/connections", "")
	call("GET", "/api/connections/"+created.ID+"/databases", "")

	var d struct{ ID string }
	json.Unmarshal([]byte(call("POST", "/api/destinations", `{"name":"d","path":`+jsonStr(dest)+`}`)), &d)
	var p struct{ ID string }
	json.Unmarshal([]byte(call("POST", "/api/plans", `{"name":"p","connection_id":"`+created.ID+`","databases":["shop"],"destination_id":"`+d.ID+
		`","schedule":"0 2 * * *","retention":{"last":3},"passphrase":"`+pass+`"}`)), &p)
	call("GET", "/api/plans", "")
	call("POST", "/api/plans/"+p.ID+"/run", "")
	call("POST", "/api/backups", `{"connection_id":"`+created.ID+`","databases":["shop"],"destination_id":"`+d.ID+`","passphrase":"`+pass+`"}`)
	s.R.Wait()
	time.Sleep(50 * time.Millisecond)

	var ops []struct{ ID, Status, Message string }
	json.Unmarshal([]byte(call("GET", "/api/operations", "")), &ops)
	if len(ops) < 2 {
		t.Fatalf("expected operations, got %d", len(ops))
	}
	for _, o := range ops {
		if o.Status != "failed" {
			t.Fatalf("op %s status %s (expected failure against unreachable server)", o.ID, o.Status)
		}
		call("GET", "/api/operations/"+o.ID+"/log", "")
	}

	for i, b := range bodies {
		if strings.Contains(b, pw) || strings.Contains(b, pass) {
			t.Fatalf("secret in API response %d: %s", i, b)
		}
	}
	filepath.Walk(s.R.DataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(path)
		if strings.Contains(string(b), pw) || strings.Contains(string(b), pass) {
			t.Fatalf("secret found in %s", path)
		}
		return nil
	})
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }
