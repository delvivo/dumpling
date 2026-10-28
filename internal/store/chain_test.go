package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// chainFixture stores a plan plus backups; parents[i] is the index of backup i's parent (-1 = full).
func chainFixture(t *testing.T, parents ...int) (*Store, []Backup) {
	t.Helper()
	st := openTest(t, filepath.Join(t.TempDir(), "c.db"))
	c := Connection{Name: "c", Engine: "mysql", Host: "h", Port: 1, Username: "u"}
	if err := st.SaveConnection(&c); err != nil {
		t.Fatal(err)
	}
	d := Destination{Name: "d", Path: t.TempDir()}
	if err := st.SaveDestination(&d); err != nil {
		t.Fatal(err)
	}
	p := Plan{Name: "p", ConnectionID: c.ID, Databases: []string{"shop"}, DestinationID: d.ID, Schedule: "@daily", IncrSchedule: "@hourly"}
	if err := st.SavePlan(&p); err != nil {
		t.Fatal(err)
	}
	bs := make([]Backup, len(parents))
	for i, par := range parents {
		b := Backup{ID: NewID(), PlanID: p.ID, ConnectionID: c.ID, ConnectionName: "c", Engine: "mysql", Database: "shop",
			DestinationID: d.ID, Path: fmt.Sprint(i), Status: "ok", StartedAt: fmt.Sprintf("2026-10-28T10:%02d:00Z", i)}
		if par < 0 {
			b.ChainID = b.ID
		} else {
			b.Kind, b.ChainID, b.ParentID, b.Seq = "incr", bs[par].ChainID, bs[par].ID, bs[par].Seq+1
		}
		if err := st.InsertBackup(&b); err != nil {
			t.Fatal(err)
		}
		bs[i] = b
	}
	return st, bs
}

func ids(bs []Backup) []string {
	out := []string{}
	for _, b := range bs {
		out = append(out, b.Path)
	}
	return out
}

func TestChainQueries(t *testing.T) {
	st, bs := chainFixture(t, -1, 0, 1, 2) // linear: full ← 1 ← 2 ← 3

	head, ok, err := st.ChainHead(bs[0].PlanID, "shop")
	if err != nil || !ok || head.ID != bs[3].ID {
		t.Fatalf("head = %v %v %v", head.Path, ok, err)
	}
	anc, err := st.Ancestors(bs[3])
	if err != nil || fmt.Sprint(ids(anc)) != "[0 1 2]" {
		t.Fatalf("ancestors = %v %v", ids(anc), err)
	}
	if anc, err := st.Ancestors(bs[0]); err != nil || len(anc) != 0 {
		t.Fatalf("full has ancestors: %v %v", anc, err)
	}
	dep, err := st.Dependents(bs[1].ID)
	if err != nil || len(dep) != 2 {
		t.Fatalf("dependents = %v %v", ids(dep), err)
	}

	// corrupted intermediate link breaks every later link
	st.SetBackupStatus(bs[1].ID, "corrupted")
	var ce *ChainError
	if _, err := st.Ancestors(bs[3]); !errors.As(err, &ce) || ce.Link != bs[1].ID || ce.Status != "corrupted" {
		t.Fatalf("corrupted: %v", err)
	}
	// missing (deleted row gone) link
	st.DB.Exec(`DELETE FROM backups WHERE id=?`, bs[0].ID)
	if _, err := st.Ancestors(bs[1]); !errors.As(err, &ce) || ce.Status != "missing" {
		t.Fatalf("missing: %v", err)
	}
	// a corrupted head is still the head (closes the chain instead of forking it)
	st.SetBackupStatus(bs[3].ID, "corrupted")
	if head, _, _ := st.ChainHead(bs[3].PlanID, "shop"); head.ID != bs[3].ID {
		t.Fatalf("head skipped corrupted link: %v", head.Path)
	}
}

func TestChainStarShaped(t *testing.T) {
	st, bs := chainFixture(t, -1, 0, 0, 0) // mssql: every diff applies on the full
	anc, err := st.Ancestors(bs[3])
	if err != nil || fmt.Sprint(ids(anc)) != "[0]" {
		t.Fatalf("ancestors = %v %v", ids(anc), err)
	}
	st.SetBackupStatus(bs[1].ID, "corrupted") // another diff's damage does not matter
	if _, err := st.Ancestors(bs[3]); err != nil {
		t.Fatal(err)
	}
	if dep, _ := st.Dependents(bs[0].ID); len(dep) != 3 {
		t.Fatalf("dependents = %v", ids(dep))
	}
}
