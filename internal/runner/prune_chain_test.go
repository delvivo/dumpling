package runner

import (
	"errors"
	"sort"
	"testing"

	"github.com/delvivo/dumpling/internal/store"
)

func okIDs(t *testing.T, st *store.Store, planID string) []string {
	t.Helper()
	bs, err := st.Backups(store.BackupFilter{PlanID: planID, Status: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, b := range bs {
		out = append(out, b.Path)
	}
	sort.Strings(out)
	return out
}

func TestPruneKeepsChains(t *testing.T) {
	for _, shape := range []string{"linear", "star"} {
		t.Run(shape, func(t *testing.T) {
			e := setup(t)
			p := e.incrPlan(t)
			p.Retention = store.Retention{Last: 3}
			full := e.link(t, p, nil, 0)
			prev := full
			var last store.Backup
			for m := 1; m <= 5; m++ {
				parent := prev
				if shape == "star" {
					parent = full // mssql: every diff applies on the full
				}
				last = e.link(t, p, &parent, m)
				prev = last
			}
			e.r.prune(p, last)
			got := okIDs(t, e.st, p.ID)
			// keep last 3 = links 3,4,5; linear needs 0,1,2 too, star only the full
			want := map[string][]string{"linear": {"0", "1", "2", "3", "4", "5"}, "star": {"0", "3", "4", "5"}}[shape]
			if len(got) != len(want) {
				t.Fatalf("kept %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("kept %v, want %v", got, want)
				}
			}
			// every kept link is restorable: its ancestors are all ok
			bs, _ := e.st.Backups(store.BackupFilter{PlanID: p.ID, Status: "ok"})
			for _, b := range bs {
				if _, err := e.st.Ancestors(b); err != nil {
					t.Fatalf("kept %s lost a link: %v", b.Path, err)
				}
			}
		})
	}
}

func TestPruneNonIncrementalUnchanged(t *testing.T) {
	e := setup(t)
	p := e.incrPlan(t)
	p.IncrSchedule, p.Retention = "", store.Retention{Last: 2}
	var last store.Backup
	for m := 0; m < 4; m++ {
		last = e.link(t, p, nil, m)
	}
	e.r.prune(p, last)
	if got := okIDs(t, e.st, p.ID); len(got) != 2 || got[0] != "2" || got[1] != "3" {
		t.Fatalf("kept %v", got)
	}
}

func TestDeleteCascade(t *testing.T) {
	e := setup(t)
	p := e.incrPlan(t)
	full := e.link(t, p, nil, 0)
	i1 := e.link(t, p, &full, 1)
	e.link(t, p, &i1, 2)

	err := e.r.DeleteBackup(full.ID, "shop", false)
	var re *Error
	if code(err) != "has_dependents" {
		t.Fatalf("no cascade: %v", err)
	}
	if errors.As(err, &re); re.Extra["dependents"] != 2 {
		t.Fatalf("dependents: %v", re.Extra)
	}
	if got := okIDs(t, e.st, p.ID); len(got) != 3 {
		t.Fatalf("deleted without cascade: %v", got)
	}
	if err := e.r.DeleteBackup(full.ID, "shop", true); err != nil {
		t.Fatal(err)
	}
	if got := okIDs(t, e.st, p.ID); len(got) != 0 {
		t.Fatalf("left %v", got)
	}
}
