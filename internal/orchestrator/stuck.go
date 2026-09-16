package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/geekychris/chief/internal/attention"
	"github.com/geekychris/chief/internal/project"
	"github.com/geekychris/chief/internal/store"
)

const (
	// DefaultStuckInterval is how often StuckSweeper (d2d9) checks for
	// tasks past their SLA. Hourly is fine — the sweep is cheap (one
	// query per project) and stuck-task detection isn't latency-sensitive.
	DefaultStuckInterval = 1 * time.Hour
	// DefaultStuckActiveHours: task in 'active' longer than this = stuck.
	DefaultStuckActiveHours = 24
	// DefaultStuckSentHours: most recent task.sent > this AND not done = stuck.
	DefaultStuckSentHours = 48
)

// StuckSweeper (d2d9) raises attention-urgency flags for tasks that have
// been active or in Claude's hands (per task.sent events) longer than the
// per-project SLA. Complements the on-demand `chief outliers` CLI (531e)
// by pushing rather than requiring a pull.
type StuckSweeper struct {
	Store     *store.Store
	Projects  *project.Manager // for reading .chief/project.yaml SLA overrides
	Attention *attention.Manager
	Interval  time.Duration
	Clock     func() time.Time

	mu       sync.Mutex
	lastSeen map[string]time.Time // per-task cursor to avoid re-flagging every tick
}

// Run blocks until ctx cancelled. First sweep at boot + 2min, then Interval.
func (s *StuckSweeper) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultStuckInterval
	}
	select {
	case <-time.After(2 * time.Minute):
	case <-ctx.Done():
		return
	}
	if err := s.Sweep(ctx); err != nil {
		slog.Warn("stuck sweeper: first sweep failed", "err", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Sweep(ctx); err != nil {
				slog.Warn("stuck sweeper: tick failed", "err", err)
			}
		}
	}
}

// Sweep runs one pass across every registered project.
func (s *StuckSweeper) Sweep(ctx context.Context) error {
	if s.Store == nil || s.Attention == nil {
		return fmt.Errorf("stuck sweeper: missing dependency")
	}
	projs, err := s.Store.ListProjects(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	for _, p := range projs {
		if err := s.sweepOne(ctx, p, now); err != nil {
			slog.Warn("stuck sweep for project failed", "project", p.Path, "err", err)
		}
	}
	return nil
}

func (s *StuckSweeper) sweepOne(ctx context.Context, p store.Project, now time.Time) error {
	// Per-project SLA overrides (optional).
	activeHours, sentHours := DefaultStuckActiveHours, DefaultStuckSentHours
	if s.Projects != nil {
		pf, err := s.Projects.ReadYAML(ctx, p.ID)
		if err == nil {
			if pf.Stuck.Disable {
				return nil
			}
			if pf.Stuck.ActiveHours > 0 {
				activeHours = pf.Stuck.ActiveHours
			}
			if pf.Stuck.SentHours > 0 {
				sentHours = pf.Stuck.SentHours
			}
		}
	}

	// Case A: task.status == active AND claimed_at older than activeHours.
	tasks, err := s.Store.ListTasks(ctx, store.TaskFilter{ProjectID: p.ID, Status: store.TaskActive})
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.ClaimedAt == nil {
			continue
		}
		age := now.Sub(*t.ClaimedAt)
		if age < time.Duration(activeHours)*time.Hour {
			continue
		}
		s.raise(ctx, p, t, "active", age)
	}
	// Case B: task.sent event > sentHours ago AND task still not done.
	// Rather than joining events, walk pending/active/blocked/deferred tasks
	// and consult events for the most recent task.sent per task.
	nonDone, err := s.pendingLikeTasks(ctx, p.ID)
	if err != nil {
		return err
	}
	for _, t := range nonDone {
		last, err := s.lastTaskSent(ctx, p.ID, t.ID)
		if err != nil || last.IsZero() {
			continue
		}
		age := now.Sub(last)
		if age < time.Duration(sentHours)*time.Hour {
			continue
		}
		s.raise(ctx, p, t, "sent-no-followup", age)
	}
	return nil
}

func (s *StuckSweeper) pendingLikeTasks(ctx context.Context, projectID string) ([]store.Task, error) {
	// Collect anything not done/dropped.
	var out []store.Task
	for _, st := range []store.TaskStatus{store.TaskPending, store.TaskActive, store.TaskBlocked, store.TaskDeferred} {
		ts, err := s.Store.ListTasks(ctx, store.TaskFilter{ProjectID: projectID, Status: st})
		if err != nil {
			return nil, err
		}
		out = append(out, ts...)
	}
	return out, nil
}

func (s *StuckSweeper) lastTaskSent(ctx context.Context, projectID, taskID string) (time.Time, error) {
	// events table has payload JSON; filter with LIKE for cheap lookup.
	// (Chief's event volume is low — a full index isn't worth it.)
	row := s.Store.DB().QueryRowContext(ctx, `
		SELECT MAX(ts) FROM events
		 WHERE project_id = ? AND kind = 'task.sent'
		   AND payload LIKE ?`, projectID, `%"task_id":"`+taskID+`"%`)
	var tsStr string
	if err := row.Scan(&tsStr); err != nil {
		return time.Time{}, err
	}
	if tsStr == "" {
		return time.Time{}, nil
	}
	t, _ := time.Parse(time.RFC3339Nano, tsStr)
	return t, nil
}

func (s *StuckSweeper) raise(ctx context.Context, p store.Project, t store.Task, reason string, age time.Duration) {
	// Don't re-flag within a 6h window per task.
	if last, ok := s.lastFor(t.ID); ok && time.Since(last) < 6*time.Hour {
		return
	}
	body := fmt.Sprintf(
		"⏳ Stuck task ({id:%s}): `%s`\nReason: %s (age %s)\nSuggest: `chief task split %s` or manual review.",
		t.ID, t.Title, reason, humanAgo(age), t.ID,
	)
	_, err := s.Attention.Raise(ctx, attention.RaiseOpts{
		ProjectID: p.ID,
		Kind:      store.FlagKindQuestion,
		Urgency:   store.UrgencyAttention,
		Question:  body,
	})
	if err != nil {
		slog.Warn("stuck: raise flag failed", "project", p.Path, "task", t.ID, "err", err)
		return
	}
	s.recordFor(t.ID, s.now())
	slog.Info("stuck sweeper: raised", "project", p.Name, "task", t.ID, "reason", reason)
}

func (s *StuckSweeper) lastFor(taskID string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastSeen == nil {
		return time.Time{}, false
	}
	t, ok := s.lastSeen[taskID]
	return t, ok
}

func (s *StuckSweeper) recordFor(taskID string, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastSeen == nil {
		s.lastSeen = map[string]time.Time{}
	}
	s.lastSeen[taskID] = t
}

func (s *StuckSweeper) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now().UTC()
}
