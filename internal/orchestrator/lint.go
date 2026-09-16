package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/geekychris/chief/internal/attention"
	"github.com/geekychris/chief/internal/claudetrace"
	"github.com/geekychris/chief/internal/constitution"
	"github.com/geekychris/chief/internal/store"
)

const (
	// DefaultLintInterval is how often the constitution linter sweeps
	// each project's session logs. 6h is a compromise: often enough to
	// catch drift while a session is still fresh in the user's mind,
	// rare enough to keep noise low.
	DefaultLintInterval = 6 * time.Hour
	// DefaultLintLookback is how far back into session logs the first
	// sweep after boot scans. Subsequent sweeps only touch entries
	// newer than the previous sweep timestamp.
	DefaultLintLookback = 24 * time.Hour
)

// LintSweeper reads each project's .chief/lint.yaml (if present) +
// scans that project's Claude session jsonl files for Bash tool_use
// commands matching forbidden patterns. Raises info-urgency flags via
// the attention manager. Aggregation coalesces near-duplicates so a
// pattern that fires 10 times in one session doesn't flood the inbox.
type LintSweeper struct {
	Store     *store.Store
	Attention *attention.Manager
	Interval  time.Duration
	Clock     func() time.Time

	mu       sync.Mutex
	lastSeen map[string]time.Time // per-project cursor
}

// Run blocks until ctx cancelled. First sweep at boot + 90s (letting
// re-attach finish), then every Interval.
func (l *LintSweeper) Run(ctx context.Context) {
	interval := l.Interval
	if interval <= 0 {
		interval = DefaultLintInterval
	}
	select {
	case <-time.After(90 * time.Second):
	case <-ctx.Done():
		return
	}
	if err := l.Sweep(ctx); err != nil {
		slog.Warn("lint sweeper: first sweep failed", "err", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := l.Sweep(ctx); err != nil {
				slog.Warn("lint sweeper: tick failed", "err", err)
			}
		}
	}
}

// Sweep runs one pass across every registered project. Exported so
// tests + a future `chief lint` CLI can drive it directly.
func (l *LintSweeper) Sweep(ctx context.Context) error {
	if l.Store == nil || l.Attention == nil {
		return fmt.Errorf("lint sweeper: missing dependency")
	}
	projs, err := l.Store.ListProjects(ctx)
	if err != nil {
		return err
	}
	now := l.now()
	for _, p := range projs {
		if err := l.sweepOne(ctx, p, now); err != nil {
			slog.Warn("lint sweep for project failed", "project", p.Path, "err", err)
		}
	}
	return nil
}

func (l *LintSweeper) sweepOne(ctx context.Context, p store.Project, now time.Time) error {
	rules, present, err := constitution.LoadRules(p.Path)
	if err != nil {
		return err
	}
	if !present || rules.Disable || len(rules.Forbidden) == 0 {
		return nil
	}
	c, compileErrs := constitution.Compile(rules)
	for _, e := range compileErrs {
		slog.Warn("lint: bad regex in project", "project", p.Path, "err", e)
	}
	if len(c.Patterns) == 0 {
		return nil
	}
	// Determine `since` — either the last-sweep cursor for this
	// project or a lookback from now.
	lookback := DefaultLintLookback
	if rules.SweepHours > 0 {
		lookback = time.Duration(rules.SweepHours) * time.Hour
	}
	since := l.lastFor(p.ID)
	if since.IsZero() || now.Sub(since) > lookback {
		since = now.Add(-lookback)
	}

	sessions, err := claudetrace.ListSessions(p.Path)
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(sessions))
	for _, s := range sessions {
		paths = append(paths, s.Path)
	}
	vs, err := constitution.ScanSessions(paths, since, c)
	if err != nil {
		return err
	}
	l.recordFor(p.ID, now)
	if len(vs) == 0 {
		return nil
	}
	// Raise info flags. Attention manager's agg-key coalesces
	// near-duplicates (same-project + same first-N-tokens), so 10
	// hits on the same pattern collapse to a single flag.
	for _, v := range vs {
		body := fmt.Sprintf(
			"Constitution violation (%s): `%s`\nRule: %s\nSession: %s",
			v.Pattern, v.Command, v.Reason, v.SessionID,
		)
		_, err := l.Attention.Raise(ctx, attention.RaiseOpts{
			ProjectID: p.ID,
			Kind:      store.FlagKindQuestion,
			Urgency:   store.UrgencyInfo,
			Question:  body,
		})
		if err != nil {
			slog.Warn("lint: raise flag failed", "project", p.Path, "err", err)
		}
	}
	slog.Info("lint sweeper: raised violations", "project", p.Name, "count", len(vs))
	return nil
}

func (l *LintSweeper) lastFor(pid string) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastSeen == nil {
		return time.Time{}
	}
	return l.lastSeen[pid]
}

func (l *LintSweeper) recordFor(pid string, t time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastSeen == nil {
		l.lastSeen = map[string]time.Time{}
	}
	l.lastSeen[pid] = t
}

func (l *LintSweeper) now() time.Time {
	if l.Clock != nil {
		return l.Clock()
	}
	return time.Now().UTC()
}
