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

// DefaultWatchInterval is how often WatchSweeper looks for fresh
// urgent violations. 30s is short enough to feel real-time relative
// to Claude turns, long enough to avoid pathological I/O when a
// session is idle.
const DefaultWatchInterval = 30 * time.Second

// DefaultWatchLookback bounds the first sweep after boot. Subsequent
// sweeps only touch entries newer than the previous sweep cursor.
const DefaultWatchLookback = 2 * time.Minute

// defaultUrgentPatterns are the built-in "you'd want to know NOW"
// checks that fire for every project regardless of .chief/lint.yaml.
// Add-only via user's `watch:` section — subtracting a default takes
// a code change (intentional; these are safety rails, not preferences).
var defaultUrgentPatterns = []constitution.ForbiddenRule{
	{Regex: `\brm\s+-rf\s+(/|\$HOME|~)($|\s|/)`, Reason: "rm -rf against a root/home path — irrecoverable if it runs"},
	{Regex: `\bgit\s+push\s+.*--force(-with-lease)?\b.*\b(main|master)\b`, Reason: "force-push to main/master"},
	{Regex: `\bgit\s+push\s+--force\s+origin\s+(main|master)\b`, Reason: "force-push to origin main/master"},
	{Regex: `\bcurl\s+.*\|\s*(sh|bash|zsh)\b`, Reason: "curl-piped-to-shell — pipes remote code straight into a shell"},
	{Regex: `\bsudo\s+rm\s+-rf\b`, Reason: "sudo rm -rf — same problem as rm -rf but with the safety off"},
	{Regex: `\b(dd\s+.*of=/dev/)`, Reason: "dd to a raw device — one typo destroys a disk"},
	{Regex: `\bchmod\s+-R\s+777\b`, Reason: "chmod -R 777 — grants world-write on the whole tree"},
}

// WatchSweeper (8db7) tails each project's Claude session jsonl files
// and raises URGENT-urgency flags for tool_use events matching
// defaultUrgentPatterns + any per-project `watch:` rules in
// .chief/lint.yaml. Same file-discovery + Bash-command-extract
// machinery as LintSweeper — this differs only in cadence + urgency
// tier + baked-in defaults.
//
// Coalescing: attention.Manager's agg-key collapses N hits on the
// same (project, kind, first-tokens) into a single flag; a session
// spamming `rm -rf` gets one urgent notification, not one per hit.
type WatchSweeper struct {
	Store     *store.Store
	Attention *attention.Manager
	Interval  time.Duration
	Clock     func() time.Time

	mu       sync.Mutex
	lastSeen map[string]time.Time // per-project cursor
}

// Run blocks until ctx cancelled. First sweep at boot + 30s (letting
// reattach finish), then every Interval.
func (w *WatchSweeper) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = DefaultWatchInterval
	}
	select {
	case <-time.After(30 * time.Second):
	case <-ctx.Done():
		return
	}
	if err := w.Sweep(ctx); err != nil {
		slog.Warn("watch sweeper: first sweep failed", "err", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.Sweep(ctx); err != nil {
				slog.Warn("watch sweeper: tick failed", "err", err)
			}
		}
	}
}

// Sweep runs one pass across every registered project.
func (w *WatchSweeper) Sweep(ctx context.Context) error {
	if w.Store == nil || w.Attention == nil {
		return fmt.Errorf("watch sweeper: missing dependency")
	}
	projs, err := w.Store.ListProjects(ctx)
	if err != nil {
		return err
	}
	now := w.now()
	for _, p := range projs {
		if err := w.sweepOne(ctx, p, now); err != nil {
			slog.Warn("watch sweep for project failed", "project", p.Path, "err", err)
		}
	}
	return nil
}

func (w *WatchSweeper) sweepOne(ctx context.Context, p store.Project, now time.Time) error {
	// Merge defaults + user `watch:` overlay from .chief/lint.yaml.
	// User can disable the sweeper for a project by setting disable:true
	// on the top-level Rules — same knob as the lint sweeper, matches
	// the "one file, two rule types" mental model.
	rules, _, err := constitution.LoadRules(p.Path)
	if err != nil {
		return err
	}
	if rules.Disable {
		return nil
	}
	merged := constitution.Rules{Forbidden: append([]constitution.ForbiddenRule{}, defaultUrgentPatterns...)}
	merged.Forbidden = append(merged.Forbidden, rules.Watch...)

	c, compileErrs := constitution.Compile(merged)
	for _, e := range compileErrs {
		slog.Warn("watch: bad regex", "project", p.Path, "err", e)
	}
	if len(c.Patterns) == 0 {
		return nil
	}

	since := w.lastFor(p.ID)
	if since.IsZero() || now.Sub(since) > DefaultWatchLookback {
		since = now.Add(-DefaultWatchLookback)
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
	w.recordFor(p.ID, now)
	if len(vs) == 0 {
		return nil
	}
	for _, v := range vs {
		body := fmt.Sprintf(
			"⚠ Urgent watch hit (%s): `%s`\nRule: %s\nSession: %s",
			v.Pattern, v.Command, v.Reason, v.SessionID,
		)
		_, err := w.Attention.Raise(ctx, attention.RaiseOpts{
			ProjectID: p.ID,
			Kind:      store.FlagKindQuestion,
			Urgency:   store.UrgencyUrgent,
			Question:  body,
		})
		if err != nil {
			slog.Warn("watch: raise flag failed", "project", p.Path, "err", err)
		}
	}
	slog.Info("watch sweeper: raised urgent violations", "project", p.Name, "count", len(vs))
	return nil
}

func (w *WatchSweeper) lastFor(pid string) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lastSeen == nil {
		return time.Time{}
	}
	return w.lastSeen[pid]
}

func (w *WatchSweeper) recordFor(pid string, t time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lastSeen == nil {
		w.lastSeen = map[string]time.Time{}
	}
	w.lastSeen[pid] = t
}

func (w *WatchSweeper) now() time.Time {
	if w.Clock != nil {
		return w.Clock()
	}
	return time.Now().UTC()
}
