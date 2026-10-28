package runner

import (
	"fmt"
	"strings"
	"testing"

	"github.com/delvivo/dumpling/internal/store"
)

// incrPlan saves an incremental plan on the test env.
func (e *env) incrPlan(t *testing.T) store.Plan {
	t.Helper()
	p := store.Plan{Name: "inc", ConnectionID: e.conn.ID, Databases: []string{"shop"}, DestinationID: e.dest.ID,
		Schedule: "0 2 * * 0", IncrSchedule: "0 * * * *", MaxChain: 3, Retention: store.Retention{Last: 50}}
	if err := e.st.SavePlan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

// link inserts a chain row (no artifact) on top of parent (nil = full), started at minute m.
func (e *env) link(t *testing.T, p store.Plan, parent *store.Backup, m int) store.Backup {
	t.Helper()
	b := store.Backup{ID: store.NewID(), PlanID: p.ID, ConnectionID: e.conn.ID, ConnectionName: "a", Engine: "mssql",
		Database: "shop", DestinationID: e.dest.ID, Path: fmt.Sprint(m), Status: "ok", StartedAt: fmt.Sprintf("2026-10-28T10:%02d:00Z", m)}
	if parent == nil {
		b.ChainID = b.ID
	} else {
		b.Kind, b.ChainID, b.ParentID, b.Seq = "incr", parent.ChainID, parent.ID, parent.Seq+1
	}
	if err := e.st.InsertBackup(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecideKind(t *testing.T) {
	e := setup(t)
	p := e.incrPlan(t)
	want := func(name, requested, kind, reason string) {
		t.Helper()
		d, err := e.r.decideKind(p, "shop", requested)
		if err != nil || d.Kind != kind || !strings.Contains(d.Reason, reason) {
			t.Fatalf("%s: got %+v %v, want %s %q", name, d, err, kind, reason)
		}
	}

	want("rule 1: no base", "incremental", "full", "no base")
	full := e.link(t, p, nil, 0)
	i1 := e.link(t, p, &full, 1)
	want("continue chain", "incremental", "incr", "")
	if d, _ := e.r.decideKind(p, "shop", "incremental"); d.Head.ID != i1.ID || d.Full.ID != full.ID {
		t.Fatalf("head/full: %+v", d)
	}
	want("full requested", "full", "full", "")
	np := p
	np.IncrSchedule = ""
	if d, _ := e.r.decideKind(np, "shop", "incremental"); d.Kind != "full" || d.Reason != "" {
		t.Fatalf("non-incremental plan: %+v", d)
	}

	// rule 2: corrupted intermediate link, then corrupted head
	i2 := e.link(t, p, &i1, 2)
	e.st.SetBackupStatus(i1.ID, "corrupted")
	want("rule 2: corrupted intermediate", "incremental", "full", "chain broken: link "+i1.ID)
	e.st.SetBackupStatus(i1.ID, "ok")
	e.st.SetBackupStatus(i2.ID, "corrupted")
	want("rule 2: corrupted head", "incremental", "full", "chain broken: link "+i2.ID)
	e.st.SetBackupStatus(i2.ID, "ok")

	// rule 3: chain length (MaxChain 3)
	e.link(t, p, &i2, 3)
	want("rule 3: chain length", "incremental", "full", "chain length limit")

	// rule 4: plan changed after the chain's full
	e.link(t, p, nil, 4)
	want("new chain", "incremental", "incr", "")
	p.ChainResetAt = "2026-10-28T10:05:00Z"
	want("rule 4: plan changed", "incremental", "full", "plan changed")
	p.ChainResetAt = "2026-10-28T10:03:59Z" // before the new full started
	want("reset before chain", "incremental", "incr", "")
}
