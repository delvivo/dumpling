package schedule

import (
	"testing"
	"time"

	"github.com/delvivo/dumpling/internal/store"
)

func TestDue(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	nightly := store.Plan{ID: "n", Schedule: "CRON_TZ=UTC 0 2 * * *"}
	hourly := store.Plan{ID: "h", Schedule: "CRON_TZ=UTC 30 * * * *"}
	paused := store.Plan{ID: "p", Schedule: "CRON_TZ=UTC * * * * *", Paused: true}
	bad := store.Plan{ID: "b", Schedule: "not cron"}
	plans := []store.Plan{nightly, hourly, paused, bad}

	run, wake := due(plans, at("2026-10-27T01:00:00Z"), at("2026-10-27T01:10:00Z"))
	if len(run) != 0 || !wake.Equal(at("2026-10-27T01:30:00Z")) {
		t.Fatalf("nothing due: %v %v", run, wake)
	}
	run, wake = due(plans, at("2026-10-27T01:10:00Z"), at("2026-10-27T02:00:00Z"))
	if len(run) != 2 || !wake.Equal(at("2026-10-27T02:30:00Z")) {
		t.Fatalf("both due once: %v %v", len(run), wake)
	}
	// Edited plan (now paused) is no longer fired.
	nightly.Paused = true
	if run, _ := due([]store.Plan{nightly}, at("2026-10-27T01:00:00Z"), at("2026-10-27T03:00:00Z")); len(run) != 0 {
		t.Fatal("paused plan fired")
	}
	// Incremental schedule: hourly incrementals, nightly full; full wins at 02:00.
	incr := store.Plan{ID: "i", Schedule: "CRON_TZ=UTC 0 2 * * *", IncrSchedule: "CRON_TZ=UTC 0 * * * *"}
	run, wake = due([]store.Plan{incr}, at("2026-10-27T00:30:00Z"), at("2026-10-27T01:00:00Z"))
	if len(run) != 1 || run[0].Kind != "incremental" || !wake.Equal(at("2026-10-27T02:00:00Z")) {
		t.Fatalf("incremental due: %+v %v", run, wake)
	}
	run, _ = due([]store.Plan{incr}, at("2026-10-27T01:30:00Z"), at("2026-10-27T02:00:00Z"))
	if len(run) != 1 || run[0].Kind != "full" {
		t.Fatalf("full must win: %+v", run)
	}
	if !NextIncr(nightly, at("2026-10-27T00:00:00Z")).IsZero() {
		t.Fatal("incremental run for a non-incremental plan")
	}
	if _, err := Parse("CRON_TZ=Europe/Rome 0 3 * * 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse("61 * * * *"); err == nil {
		t.Fatal("invalid minute accepted")
	}
}
