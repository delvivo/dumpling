package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIncrementalPlanValidation(t *testing.T) {
	c, _ := newTestServer(t)
	c.login(t)
	mk := func(body string) string {
		var v struct{ ID string }
		_, b := c.do("POST", "/api/connections", body, true)
		json.Unmarshal([]byte(b), &v)
		return v.ID
	}
	ms := mk(`{"name":"ms","engine":"mssql","host":"127.0.0.1","port":1,"username":"sa","tls_mode":"disable","share_path_server":"/s","share_path_local":` + jsonStr(t.TempDir()) + `}`)
	pg := mk(`{"name":"pg","engine":"postgres","host":"127.0.0.1","port":1,"username":"u"}`)
	var d struct{ ID string }
	_, db := c.do("POST", "/api/destinations", `{"name":"d","path":`+jsonStr(t.TempDir())+`}`, true)
	json.Unmarshal([]byte(db), &d)

	plan := func(conn, dbs, extra string) string {
		return `{"name":"p","connection_id":"` + conn + `","databases":` + dbs + `,"destination_id":"` + d.ID +
			`","schedule":"0 2 * * 0","retention":{"last":3}` + extra + `}`
	}
	expect := func(method, path, body string, status int, code string) string {
		t.Helper()
		res, b := c.do(method, path, body, true)
		if res.StatusCode != status || code != "" && !strings.Contains(b, `"code":"`+code+`"`) {
			t.Fatalf("%s %s: %d %s (want %d %s)", method, path, res.StatusCode, b, status, code)
		}
		return b
	}

	expect("POST", "/api/plans", plan(ms, `["shop"]`, `,"incr_schedule":"0 * * * *"`), 400, "ack_required")
	expect("POST", "/api/plans", plan(ms, `["shop"]`, `,"incr_schedule":"nope","ack_incremental_risk":true`), 400, "invalid_schedule")
	expect("POST", "/api/plans", plan(ms, `["shop"]`, `,"incr_schedule":"0 * * * *","max_chain":5000,"ack_incremental_risk":true`), 400, "invalid")
	b := expect("POST", "/api/plans", plan(ms, `["shop"]`, `,"incr_schedule":"0 * * * *","ack_incremental_risk":true`), 201, "")
	var p struct {
		ID          string `json:"id"`
		IncrAckAt   string `json:"incr_ack_at"`
		MaxChain    int    `json:"max_chain"`
		NextIncrRun string `json:"next_incr_run"`
	}
	json.Unmarshal([]byte(b), &p)
	if p.IncrAckAt == "" || p.MaxChain != 30 || p.NextIncrRun == "" {
		t.Fatalf("saved plan: %s", b)
	}
	// still incremental: the stored acknowledgment is kept without re-sending it
	b = expect("PUT", "/api/plans/"+p.ID, plan(ms, `["shop"]`, `,"incr_schedule":"30 * * * *"`), 200, "")
	json.Unmarshal([]byte(b), &p)
	if p.IncrAckAt == "" {
		t.Fatal("ack lost on update")
	}
	// turning incremental off clears it, so re-enabling asks again
	b = expect("PUT", "/api/plans/"+p.ID, plan(ms, `["shop"]`, ``), 200, "")
	json.Unmarshal([]byte(b), &p)
	if p.IncrAckAt != "" {
		t.Fatal("ack kept after disabling")
	}
	expect("PUT", "/api/plans/"+p.ID, plan(ms, `["shop"]`, `,"incr_schedule":"0 * * * *"`), 400, "ack_required")

	// PostgreSQL incremental plans are server-wide; "*" only there
	expect("POST", "/api/plans", plan(pg, `["shop"]`, `,"incr_schedule":"0 * * * *"`), 400, "invalid")
	expect("POST", "/api/plans", plan(pg, `["*"]`, ``), 400, "invalid")
	expect("POST", "/api/plans", plan(ms, `["*"]`, `,"incr_schedule":"0 * * * *","ack_incremental_risk":true`), 400, "invalid")
	// prerequisites are checked when enabling (server unreachable → reported, not saved)
	expect("POST", "/api/plans", plan(pg, `["*"]`, `,"incr_schedule":"0 * * * *"`), 400, "prerequisite_missing")

	expect("POST", "/api/plans/"+p.ID+"/run?kind=bogus", "", 400, "invalid")
}
