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

// Next returns the next full run of a plan after t (zero if paused or invalid).
func Next(p store.Plan, t time.Time) time.Time { return next(p, p.Schedule, t) }

// NextIncr returns the next incremental run after t (zero if incremental mode is off).
func NextIncr(p store.Plan, t time.Time) time.Time { return next(p, p.IncrSchedule, t) }

func next(p store.Plan, expr string, t time.Time) time.Time {
	if p.Paused || expr == "" {
		return time.Time{}
	}
	s, err := Parse(expr)
	if err != nil {
		return time.Time{}
	}
	return s.Next(t)
}

// Due is a plan run to start; Kind is "full" or "incremental".
type Due struct {
	Plan store.Plan
	Kind string
}

// due returns plan runs in (from, now] — a full wins when both schedules fire in the same
// tick — and the earliest future run after now.
func due(plans []store.Plan, from, now time.Time) (run []Due, wake time.Time) {
	for _, p := range plans {
		fired := ""
		for _, k := range []struct {
			kind string
			next func(store.Plan, time.Time) time.Time
		}{{"full", Next}, {"incremental", NextIncr}} {
			n := k.next(p, from)
			if n.IsZero() {
				continue
			}
			if !n.After(now) {
				if fired == "" {
					fired = k.kind
				}
				n = k.next(p, now)
			}
			if wake.IsZero() || n.Before(wake) {
				wake = n
			}
		}
		if fired != "" {
			run = append(run, Due{p, fired})
		}
	}
	return
}

type Scheduler struct {
	St     *store.Store
	Run    func(p store.Plan, kind string) // starts a plan run (overlap handled by the runner)
	reload chan struct{}
}

func New(st *store.Store, run func(store.Plan, string)) *Scheduler {
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
		for _, d := range run {
			slog.Info("scheduler: starting plan", "plan", d.Plan.Name, "kind", d.Kind)
			s.Run(d.Plan, d.Kind)
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
