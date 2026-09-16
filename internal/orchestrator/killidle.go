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
	// DefaultKillIdleInterval — check every 10min. AfterHours is measured
	// in hours, so a coarser tick is fine.
	DefaultKillIdleInterval = 10 * time.Minute
	// DefaultKillIdleAfterHours — 4h with no title change means the
	// session has almost certainly been left running after work stopped.
	DefaultKillIdleAfterHours = 4
)

// KillIdleSweeper (1b72) finds Claude cmux surfaces whose title hasn't
// changed in >AfterHours and either raises an urgent flag ("this session
// looks abandoned; close it to save API budget?") or, if the project
// opted in via kill_idle.auto_close, calls cmux close-surface directly.
//
// Different from IdleSweeper (idle.go):
//   - Filters to IsClaude=true only (terminal panes running non-Claude
//     work are always the user's business).
//   - Hours-scale threshold vs the info-tier 30min "possibly idle" flag.
//   - Actually terminates when auto-close is on.
type KillIdleSweeper struct {
	Store     *store.Store
	Projects  *project.Manager
	Cmux      *cmux.Client
	Attention *attention.Manager
	Interval  time.Duration
	Clock     func() time.Time

	mu     sync.Mutex
	titles map[string]titleObs // per-surface-ref
}

type titleObs struct {
	Title string
	Since time.Time
}

func (k *KillIdleSweeper) Run(ctx context.Context) {
	if k.Cmux == nil {
		slog.Info("kill-idle sweeper: cmux nil, disabled")
		return
	}
	interval := k.Interval
	if interval <= 0 {
		interval = DefaultKillIdleInterval
	}
	// Give re-attach a chance to finish so we don't kill sessions that
	// just started up.
	select {
	case <-time.After(2 * time.Minute):
	case <-ctx.Done():
		return
	}
	if err := k.Sweep(ctx); err != nil {
		slog.Warn("kill-idle sweeper: first sweep failed", "err", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := k.Sweep(ctx); err != nil {
				slog.Warn("kill-idle sweeper: tick failed", "err", err)
			}
		}
	}
}

// Sweep runs one pass. Exported for tests + a manual `chief killidle`
// trigger if we add one.
func (k *KillIdleSweeper) Sweep(ctx context.Context) error {
	if k.Store == nil || k.Projects == nil || k.Attention == nil {
		return fmt.Errorf("kill-idle sweeper: missing dependency")
	}
	surfaces, err := k.Cmux.ListSurfaces(ctx)
	if err != nil {
		slog.Warn("kill-idle sweeper: cmux unreachable", "err", err)
		return nil
	}
	// Map from surface ref → live surface.
	byRef := map[string]cmux.Surface{}
	for _, s := range surfaces {
		byRef[s.Ref] = s
	}
	now := k.now()
	projs, err := k.Store.ListProjects(ctx)
	if err != nil {
		return err
	}

	for _, p := range projs {
		pf, err := k.Projects.ReadYAML(ctx, p.ID)
		if err != nil {
			continue
		}
		if pf.KillIdle.Disable {
			continue
		}
		ref := pf.Cmux.SurfaceID
		if ref == "" {
			continue
		}
		sur, alive := byRef[ref]
		if !alive {
			// The regular IdleSweeper handles stale-binding notification;
			// don't double-flag here.
			k.forget(ref)
			continue
		}
		if !sur.IsClaude {
			continue
		}
		afterHours := pf.KillIdle.AfterHours
		if afterHours <= 0 {
			afterHours = DefaultKillIdleAfterHours
		}
		threshold := time.Duration(afterHours) * time.Hour

		if changed := k.observe(ref, sur.Title, now); changed {
			continue
		}
		age := k.since(ref, now)
		if age < threshold {
			continue
		}
		// Threshold exceeded. Auto-close or raise urgent flag.
		if pf.KillIdle.AutoClose {
			if err := k.Cmux.CloseSurface(ctx, ref); err != nil {
				slog.Warn("kill-idle: close-surface failed", "project", p.Name, "surface", ref, "err", err)
				// Fall through to flag so the user knows it's still open.
			} else {
				k.forget(ref)
				_, _ = k.Attention.Raise(ctx, attention.RaiseOpts{
					ProjectID: p.ID,
					Kind:      store.FlagKindQuestion,
					Urgency:   store.UrgencyInfo,
					Question: fmt.Sprintf(
						"Auto-closed idle Claude session on %s (title unchanged for %s) — saving API budget. Rebind via `chief cmux bind` when you resume.",
						p.Name, age.Round(time.Minute),
					),
				})
				_ = k.Store.InsertEvent(ctx, p.ID, "", "killidle.closed", map[string]any{
					"surface": ref, "idle_seconds": int(age.Seconds()),
				})
				slog.Info("kill-idle: closed", "project", p.Name, "surface", ref, "idle", age)
				continue
			}
		}
		// Prompt-only path.
		_, _ = k.Attention.Raise(ctx, attention.RaiseOpts{
			ProjectID: p.ID,
			Kind:      store.FlagKindQuestion,
			Urgency:   store.UrgencyUrgent,
			Question: fmt.Sprintf(
				"Claude session on %s (title: %q) has been idle for %s. Close via `cmux close-surface --surface %s` to save API budget, or enable kill_idle.auto_close in .chief/project.yaml.",
				p.Name, sur.Title, age.Round(time.Minute), ref,
			),
		})
	}
	return nil
}

func (k *KillIdleSweeper) observe(ref, title string, now time.Time) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.titles == nil {
		k.titles = map[string]titleObs{}
	}
	prev, ok := k.titles[ref]
	if !ok || prev.Title != title {
		k.titles[ref] = titleObs{Title: title, Since: now}
		return true
	}
	return false
}

func (k *KillIdleSweeper) since(ref string, now time.Time) time.Duration {
	k.mu.Lock()
	defer k.mu.Unlock()
	if s, ok := k.titles[ref]; ok {
		return now.Sub(s.Since)
	}
	return 0
}

func (k *KillIdleSweeper) forget(ref string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.titles, ref)
}

func (k *KillIdleSweeper) now() time.Time {
	if k.Clock != nil {
		return k.Clock()
	}
	return time.Now().UTC()
}
