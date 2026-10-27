package server

import (
	"encoding/json"
	"testing"
)

// A failed connection test must return arrays, never null: the UI maps over them (null crashed the page).
func TestConnectionTestFailureReturnsArrays(t *testing.T) {
	c, _ := newTestServer(t)
	c.login(t)
	_, body := c.do("POST", "/api/connections/test",
		`{"name":"x","engine":"postgres","host":"127.0.0.1","port":1,"username":"postgres","password":"bad","tls_mode":"disable"}`, true)
	var res map[string]any
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if res["ok"] != false || res["error"] == "" {
		t.Fatalf("expected failure: %s", body)
	}
	for _, k := range []string{"warnings", "databases"} {
		if _, ok := res[k].([]any); !ok {
			t.Fatalf("%s must be an array, got %#v (%s)", k, res[k], body)
		}
	}
}
