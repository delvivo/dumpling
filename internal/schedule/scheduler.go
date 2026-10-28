package schedule

import (
	"context"
	"log/slog"
	"time"

	"github.com/delvivo/dumpling/internal/store"
	"github.com/robfig/cron/v3"
)

// Parse validates a 5-field cron expression (optional CRON_TZ=/TZ= prefix; default server local time).
func Parse(expr string) (cron.Schedule, error) { return cron.ParseStandard(expr) }

// Next returns the next run of a plan after t (zero if paused or invalid).
func Next(p store.Plan, t time.Time) time.Time {
	if p.Paused {
		return time.Time{}
	}
	s, err := Parse(p.Schedule)
	if err != nil {
		return time.Time{}
	}
	return s.Next(t)
}

// due returns plans with a run in (from, now], and the earliest future run after now.
func due(plans []store.Plan, from, now time.Time) (run []store.Plan, wake time.Time) {
	for _, p := range plans {
		n := Next(p, from)
		if n.IsZero() {
			continue
		}
		if !n.After(now) {
			run = append(run, p)
			n = Next(p, now)
		}
		if wake.IsZero() || n.Before(wake) {
			wake = n
		}
	}
	return
}

type Scheduler struct {
	St     *store.Store
	Run    func(store.Plan) // starts a plan run (overlap handled by the runner)
	reload chan struct{}
}

func New(st *store.Store, run func(store.Plan)) *Scheduler {
	return &Scheduler{St: st, Run: run, reload: make(chan struct{}, 1)}
}

// Reload re-reads plans; call after any plan change.
func (s *Scheduler) Reload() {
	select {
	case s.reload <- struct{}{}:
	default:
	}
}

func (s *Scheduler) Loop(ctx context.Context) {
	from := time.Now()
	for {
		plans, err := s.St.Plans()
		if err != nil {
			slog.Error("scheduler: load plans", "err", err)
		}
		now := time.Now()
		run, wake := due(plans, from, now)
		for _, p := range run {
			slog.Info("scheduler: starting plan", "plan", p.Name)
			s.Run(p)
		}
		from = now
		wait := time.Hour
		if !wake.IsZero() {
			wait = time.Until(wake)
		}
		slog.Debug("scheduler: next wake", "in", wait.Round(time.Second))
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.reload:
			t.Stop()
		case <-t.C:
		}
	}
}
