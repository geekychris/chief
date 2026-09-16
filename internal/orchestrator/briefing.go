package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/geekychris/chief/internal/messaging"
	"github.com/geekychris/chief/internal/store"
)

// MorningBriefer is a more opinionated variant of DigestSweeper (12da).
// Where the digest reports raw activity, the briefing tells the user
// which tasks to focus on TODAY — chosen by (a) recent activity in the
// project + (b) priority + (c) doc order. Same At-time ticker pattern
// as DigestSweeper.
//
// Motivation: "you got 7 things done" is nice but "here are the 3
// things to work on first" is what actually reduces morning
// context-switch overhead.
type MorningBriefer struct {
	Store    *store.Store
	Router   *messaging.Router
	Enabled  bool
	At       string        // "HH:MM" local; default 08:30
	Backends []string      // explicit backend names; empty = routing rules
	TopN     int           // # of tasks to highlight; default 3
	Window   time.Duration // activity window; default 24h
	Clock    func() time.Time

	lastFired time.Time
}

// Run blocks until ctx is cancelled.
func (b *MorningBriefer) Run(ctx context.Context) {
	if !b.Enabled {
		slog.Info("morning briefer: disabled")
		return
	}
	b.maybeFire(ctx)
	tk := time.NewTicker(time.Minute)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			b.maybeFire(ctx)
		}
	}
}

func (b *MorningBriefer) maybeFire(ctx context.Context) {
	now := b.now()
	if !b.shouldFire(now) {
		return
	}
	if !b.lastFired.IsZero() && now.Sub(b.lastFired) < 30*time.Minute {
		return
	}
	if err := b.Fire(ctx); err != nil {
		slog.Warn("morning briefer: fire failed", "err", err)
		return
	}
	b.lastFired = now
}

func (b *MorningBriefer) shouldFire(now time.Time) bool {
	at := b.At
	if at == "" {
		at = "08:30"
	}
	parts := strings.SplitN(at, ":", 2)
	if len(parts) != 2 {
		return false
	}
	var h, m int
	if _, err := fmt.Sscanf(parts[0], "%d", &h); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &m); err != nil {
		return false
	}
	return now.Hour() == h && now.Minute() == m
}

// Fire composes the briefing and dispatches via the router. Exported
// so `chief briefing send` can trigger it manually.
func (b *MorningBriefer) Fire(ctx context.Context) error {
	if b.Store == nil || b.Router == nil {
		return fmt.Errorf("briefing: missing dependency (store/router)")
	}
	body, err := b.Compose(ctx)
	if err != nil {
		return err
	}
	msg := messaging.Message{
		Urgency: messaging.UrgencyAttention,
		Title:   "Chief morning briefing",
		Body:    body,
		Kind:    "briefing",
		Ts:      b.now(),
	}
	if len(b.Backends) > 0 {
		for _, name := range b.Backends {
			back, ok := b.Router.Backends[name]
			if !ok {
				slog.Warn("briefing: unknown backend; skipping", "name", name)
				continue
			}
			if err := back.Send(ctx, msg); err != nil {
				slog.Warn("briefing: backend send failed", "backend", name, "err", err)
			}
		}
		return nil
	}
	_ = b.Router.Dispatch(ctx, msg)
	return nil
}

// Compose builds the briefing body. Format prioritizes glance-reading
// on phones — leads with a one-line summary, then the top-N tasks,
// then a rolled-up activity line.
func (b *MorningBriefer) Compose(ctx context.Context) (string, error) {
	window := b.Window
	if window <= 0 {
		window = 24 * time.Hour
	}
	topN := b.TopN
	if topN <= 0 {
		topN = 3
	}
	now := b.now()
	since := now.Add(-window)

	projs, err := b.Store.ListProjects(ctx)
	if err != nil {
		return "", err
	}

	// Gather counters across the window.
	var completions int64
	openFlags, _ := b.Store.CountOpenFlags(ctx, "")
	perProjectActivity := map[string]int64{}
	activeProjects := 0
	for _, p := range projs {
		perProj, _ := b.Store.CountEventsByKindSince(ctx, p.ID, since)
		recent := perProj["rescan.completed"]
		completions += recent
		if recent > 0 {
			activeProjects++
			perProjectActivity[p.ID] = recent
		}
	}

	// Collect all pending tasks + score them for "should this be
	// today's focus". Score is: priority (higher wins) + a small
	// bonus for projects that had activity in the window (momentum).
	type scored struct {
		project store.Project
		task    store.Task
		score   int
	}
	var candidates []scored
	for _, p := range projs {
		tasks, _ := b.Store.ListTasks(ctx, store.TaskFilter{
			ProjectID: p.ID, Status: store.TaskPending,
		})
		for _, t := range tasks {
			score := t.Priority * 10
			if perProjectActivity[p.ID] > 0 {
				score += 3
			}
			candidates = append(candidates, scored{project: p, task: t, score: score})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		// Tie-breaker: doc order (source_line ASC) then project name.
		if candidates[i].task.SourceLine != candidates[j].task.SourceLine {
			return candidates[i].task.SourceLine < candidates[j].task.SourceLine
		}
		return candidates[i].project.Name < candidates[j].project.Name
	})
	if len(candidates) > topN {
		candidates = candidates[:topN]
	}

	var b2 strings.Builder
	fmt.Fprintf(&b2, "Since yesterday: %d completions across %d projects, %d open flags.\n\n",
		completions, activeProjects, openFlags)
	if len(candidates) == 0 {
		b2.WriteString("Nothing pending — backlog empty across every registered project. Rare!")
		return b2.String(), nil
	}
	fmt.Fprintf(&b2, "Top %d tasks to focus on today:\n", len(candidates))
	for i, c := range candidates {
		fmt.Fprintf(&b2, "  %d. [%s] %s  ({id:%s} · %s)\n",
			i+1, c.project.Name, c.task.Title, c.task.ID, priorityLabel(c.task.Priority))
	}
	return strings.TrimRight(b2.String(), "\n"), nil
}

func priorityLabel(p int) string {
	switch {
	case p >= 5:
		return "prio: high"
	case p <= -1:
		return "prio: low"
	default:
		return fmt.Sprintf("prio: %d", p)
	}
}

func (b *MorningBriefer) now() time.Time {
	if b.Clock != nil {
		return b.Clock()
	}
	return time.Now()
}
