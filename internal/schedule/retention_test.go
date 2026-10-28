package schedule

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/delvivo/dumpling/internal/store"
)

func bk(id, at, status string) store.Backup {
	return store.Backup{ID: id, StartedAt: at, Status: status}
}

func ids(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestKeep(t *testing.T) {
	// Daily backups over ~5 weeks, newest first irrelevant: Keep sorts.
	var all []store.Backup
	start := time.Date(2026, 9, 1, 2, 0, 0, 0, time.UTC) // Tuesday
	for i := 0; i < 40; i++ {
		all = append(all, bk(start.AddDate(0, 0, i).Format("0102"), start.AddDate(0, 0, i).Format(time.RFC3339), "ok"))
	}
	// Extra same-day backup: only the newest of the day counts for daily buckets.
	all = append(all, bk("1010b", "2026-10-10T23:00:00Z", "ok"))
	// Failed/deleted are ignored even if newest.
	all = append(all, bk("fail", "2026-12-01T00:00:00Z", "corrupted"), bk("gone", "2026-12-02T00:00:00Z", "deleted"))

	cases := []struct {
		name string
		p    store.Retention
		want []string
	}{
		{"last3", store.Retention{Last: 3}, []string{"1009", "1010", "1010b"}},
		{"daily2", store.Retention{Daily: 2}, []string{"1009", "1010b"}},
		{"weekly2", store.Retention{Weekly: 2}, []string{"1004", "1010b"}}, // Sun 4 Oct ends ISO week 40
		{"monthly2", store.Retention{Monthly: 2}, []string{"0930", "1010b"}},
		{"union", store.Retention{Last: 1, Monthly: 2}, []string{"0930", "1010b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ids(Keep(all, c.p)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestKeepAlwaysNewest(t *testing.T) {
	got := Keep([]store.Backup{bk("a", "2026-01-01T00:00:00Z", "ok"), bk("b", "2026-01-02T00:00:00Z", "ok")}, store.Retention{})
	if !got["b"] || got["a"] {
		t.Fatal(got)
	}
	if len(Keep(nil, store.Retention{Last: 5})) != 0 {
		t.Fatal("empty")
	}
}

func TestValidRetention(t *testing.T) {
	if ValidRetention(store.Retention{}) == nil || ValidRetention(store.Retention{Last: -1, Daily: 2}) == nil || ValidRetention(store.Retention{Weekly: 1}) != nil {
		t.Fatal("validation")
	}
}

func TestClosure(t *testing.T) {
	// linear chain full ← i1 ← i2 ← i3 (mysql/postgres) and a star chain F ← d1, F ← d2 (mssql)
	all := []store.Backup{
		{ID: "full"}, {ID: "i1", ParentID: "full"}, {ID: "i2", ParentID: "i1"}, {ID: "i3", ParentID: "i2"},
		{ID: "F"}, {ID: "d1", ParentID: "F"}, {ID: "d2", ParentID: "F"},
	}
	got := Closure(map[string]bool{"i2": true, "d2": true}, all)
	for _, id := range []string{"i2", "i1", "full", "d2", "F"} {
		if !got[id] {
			t.Errorf("%s not kept", id)
		}
	}
	for _, id := range []string{"i3", "d1"} {
		if got[id] {
			t.Errorf("%s kept", id)
		}
	}
	// dropping the full leaves no kept dependent
	if got := Closure(map[string]bool{}, all); len(got) != 0 {
		t.Fatalf("kept %v", got)
	}
}
