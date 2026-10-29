// Package schedule runs backup plans on cron schedules and computes retention.
package schedule

import (
	"fmt"
	"sort"
	"time"

	"github.com/delvivo/dumpling/internal/store"
)

func ValidRetention(p store.Retention) error {
	if p.Last < 0 || p.Daily < 0 || p.Weekly < 0 || p.Monthly < 0 {
		return fmt.Errorf("retention values must be ≥ 0")
	}
	if p.Last+p.Daily+p.Weekly+p.Monthly == 0 {
		return fmt.Errorf("at least one retention value must be > 0")
	}
	return nil
}

// Keep returns the ids of backups to keep: union of the newest p.Last, the newest per day for
// p.Daily days, per ISO week for p.Weekly weeks, per month for p.Monthly months. Only backups with
// status "ok" count; the newest ok backup is always kept. Buckets are calendar periods in UTC.
func Keep(backups []store.Backup, p store.Retention) map[string]bool {
	var ok []store.Backup
	for _, b := range backups {
		if b.Status == "ok" {
			ok = append(ok, b)
		}
	}
	sort.Slice(ok, func(i, j int) bool { return ok[i].StartedAt > ok[j].StartedAt })
	keep := map[string]bool{}
	if len(ok) == 0 {
		return keep
	}
	keep[ok[0].ID] = true
	for i := 0; i < p.Last && i < len(ok); i++ {
		keep[ok[i].ID] = true
	}
	bucket := func(n int, key func(time.Time) string) {
		seen := map[string]bool{}
		for _, b := range ok {
			if len(seen) >= n {
				return
			}
			t, err := time.Parse(time.RFC3339, b.StartedAt)
			if err != nil {
				continue
			}
			if k := key(t.UTC()); !seen[k] {
				seen[k] = true
				keep[b.ID] = true
			}
		}
	}
	bucket(p.Daily, func(t time.Time) string { return t.Format("2006-01-02") })
	bucket(p.Weekly, func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprintf("%d-W%02d", y, w) })
	bucket(p.Monthly, func(t time.Time) string { return t.Format("2006-01") })
	return keep
}
