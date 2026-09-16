// Package orchestrator runs periodic supervisory sweeps over projects:
// idle-session detection today; heartbeat/health checks + auto-recovery
// as more hooks land.
//
// The idle sweeper is deliberately shimmed on top of what cmux already
// exposes (list-panels returns per-surface title). No sessions table,
// no heartbeat protocol — those arrive in M2. The signal is:
//
//  1. If a project's bound cmux surface has DISAPPEARED (cmux restarted,
//     pane closed), raise an info-urgency flag telling the user to
//     rebind. This is a definite signal and always fires.
//
//  2. If the surface still exists but its title has been unchanged for
//     IdleTimeout, raise an info-urgency "possibly idle" flag. This is
//     a heuristic — Claude sometimes thinks for a long time on one
//     step without updating the title — hence info urgency (badge-only,
//     no notification) so it doesn't annoy.
//
// Both paths flow through attention.Manager which coalesces near-
// duplicates so a persistently-idle surface doesn't spam.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/geekychris/chief/internal/attention"
	"github.com/geekychris/chief/internal/cmux"
	"github.com/geekychris/chief/internal/project"
	"github.com/geekychris/chief/internal/store"
)

const (
	// DefaultTickInterval is how often the sweeper wakes up. Kept short
	// so "your cmux binding just died" surfaces within a couple minutes
	// rather than half an hour.
	DefaultTickInterval = 2 * time.Minute
	// DefaultIdleTimeout is the "title unchanged for this long" threshold.
	// 30min matches the task description in the backlog item.
	DefaultIdleTimeout = 30 * time.Minute
)

// CmuxLister is the subset of cmux.Client we use — kept as an interface
// so tests can inject a fake surface list.
type CmuxLister interface {
	ListSurfaces(ctx context.Context) ([]cmux.Surface, error)
}

// IdleSweeper polls cmux + project bindings on a timer.
type IdleSweeper struct {
	Store     *store.Store
	Projects  *project.Manager
	Cmux      CmuxLister
	Attention *attention.Manager
	// Optional overrides for tests. Zero values use the Default* constants.
	TickInterval time.Duration
	IdleTimeout  time.Duration
	Clock        func() time.Time

	// Per-surface title snapshots + first-seen timestamps. Reset on
	// chiefd restart — we accept re-notifying after boot rather than
	// persisting this state.
	mu     sync.Mutex
	titles map[string]titleSample
}

type titleSample struct {
	Title string
	Since time.Time
}

// Run blocks until ctx is cancelled, ticking through Sweep() on every
// interval. Errors are logged, never returned — the sweeper is meant
// to keep running through transient failures.
func (s *IdleSweeper) Run(ctx context.Context) {
	interval := s.TickInterval
	if interval <= 0 {
		interval = DefaultTickInterval
	}
	// First sweep on boot after a short delay so chiefd's own re-attach
	// finishes before we start emitting stale-binding flags.
	select {
	case <-time.After(15 * time.Second):
	case <-ctx.Done():
		return
	}
	if err := s.Sweep(ctx); err != nil {
		slog.Warn("idle sweeper: first sweep failed", "err", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Sweep(ctx); err != nil {
				slog.Warn("idle sweeper: tick failed", "err", err)
			}
		}
	}
}

// Sweep runs one pass. Exported so tests can drive it deterministically.
func (s *IdleSweeper) Sweep(ctx context.Context) error {
	if s.Cmux == nil || s.Projects == nil || s.Store == nil || s.Attention == nil {
		return fmt.Errorf("orchestrator: missing dependency (cmux/projects/store/attention)")
	}
	projects, err := s.Store.ListProjects(ctx)
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}

	// Fetch cmux surfaces once per sweep so N projects don't fan out N
	// list-panels calls.
	surfaces, err := s.Cmux.ListSurfaces(ctx)
	if err != nil {
		// cmux may be down; that's not itself a per-project alarm — we
		// warn once per sweep and skip. When cmux comes back the next
		// tick recovers.
		slog.Warn("idle sweeper: cmux unreachable", "err", err)
		return nil
	}
	byRef := map[string]cmux.Surface{}
	for _, sur := range surfaces {
		byRef[sur.Ref] = sur
	}

	now := s.now()
	idleTimeout := s.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}

	for _, p := range projects {
		pf, err := s.Projects.ReadYAML(ctx, p.ID)
		if err != nil {
			continue
		}
		if pf.Idle.Disable {
			continue
		}
		ref := pf.Cmux.SurfaceID
		if ref == "" {
			// Nothing bound — no idle check to run.
			continue
		}
		sur, alive := byRef[ref]
		if !alive {
			// Stale binding — cmux forgot the surface. Raise a flag.
			_, err := s.Attention.Raise(ctx, attention.RaiseOpts{
				ProjectID: p.ID,
				Kind:      store.FlagKindQuestion,
				Urgency:   store.UrgencyInfo,
				Question:  fmt.Sprintf("cmux surface %s is gone for project %s — rebind via `chief cmux bind` or the UI picker.", ref, p.Name),
			})
			if err != nil {
				slog.Warn("idle sweeper: raise stale-binding flag failed", "project", p.ID, "err", err)
			}
			// Also drop our cached snapshot so if the ref is reassigned
			// later we don't compare against a stale title.
			s.forget(ref)
			continue
		}

		// Surface alive — compare title against snapshot.
		perProjectTimeout := idleTimeout
		if pf.Idle.TimeoutMinutes > 0 {
			perProjectTimeout = time.Duration(pf.Idle.TimeoutMinutes) * time.Minute
		}
		if changed := s.observe(ref, sur.Title, now); !changed {
			// Title unchanged since our last observation. Check how long
			// it's been in this state.
			since := s.since(ref, now)
			if since >= perProjectTimeout {
				body := fmt.Sprintf(
					"Session on %s (title: %q) has looked idle for %s. If Claude is stuck, kick it via cmux or send a new prompt.",
					ref, sur.Title, since.Round(time.Minute),
				)
				_, err := s.Attention.Raise(ctx, attention.RaiseOpts{
					ProjectID: p.ID,
					Kind:      store.FlagKindQuestion,
					Urgency:   store.UrgencyInfo,
					Question:  body,
				})
				if err != nil {
					slog.Warn("idle sweeper: raise idle flag failed", "project", p.ID, "err", err)
				}
			}
		}
	}
	return nil
}

// observe records a fresh title sample for a surface. Returns true if
// the title CHANGED since the last observation (which resets the idle
// timer). A first observation (no prior sample) counts as "changed".
func (s *IdleSweeper) observe(ref, title string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.titles == nil {
		s.titles = map[string]titleSample{}
	}
	prev, ok := s.titles[ref]
	if !ok || prev.Title != title {
		s.titles[ref] = titleSample{Title: title, Since: now}
		return true
	}
	return false
}

// since returns how long the surface's title has been unchanged.
func (s *IdleSweeper) since(ref string, now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sample, ok := s.titles[ref]; ok {
		return now.Sub(sample.Since)
	}
	return 0
}

// forget drops a cached snapshot. Called when a surface goes away so
// we don't false-idle after a rebind to the same ref.
func (s *IdleSweeper) forget(ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.titles, ref)
}

func (s *IdleSweeper) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now().UTC()
}
