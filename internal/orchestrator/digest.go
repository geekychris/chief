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

// DigestSweeper composes and dispatches the periodic rollup message
// ("you got 7 things done, 3 flags outstanding, alpha's been quiet
// for 4 days"). Runs on a wall-clock ticker checking against a
// configured At time (HH:MM local) + cadence (daily/weekly).
type DigestSweeper struct {
	Store   *store.Store
	Router  *messaging.Router
	Enabled bool
	At      string        // "HH:MM" local; default 09:00
	Cadence string        // "daily" | "weekly"; default daily
	Backends []string     // explicit backend names; empty = routing rules
	Window  time.Duration // default 24h for daily, 168h for weekly
	Clock   func() time.Time

	// lastFired guards against double-firing when Run's minute-granular
	// ticker aligns with the At time repeatedly.
	lastFired time.Time
}

// Run blocks until ctx is cancelled. Ticks every minute checking
// whether it's time to fire. Skips if not enabled.
func (d *DigestSweeper) Run(ctx context.Context) {
	if !d.Enabled {
		slog.Info("digest sweeper: disabled")
		return
	}
	// One quick check on boot so an At-time near start-up still fires.
	d.maybeFire(ctx)
	tk := time.NewTicker(time.Minute)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			d.maybeFire(ctx)
		}
	}
}

func (d *DigestSweeper) maybeFire(ctx context.Context) {
	now := d.now()
	if !d.shouldFire(now) {
		return
	}
	if !d.lastFired.IsZero() && now.Sub(d.lastFired) < 30*time.Minute {
		return // debounce
	}
	if err := d.Fire(ctx); err != nil {
		slog.Warn("digest sweeper: fire failed", "err", err)
		return
	}
	d.lastFired = now
}

// shouldFire returns true when `now` matches the configured At time
// (within a minute) AND the cadence permits firing on this day.
func (d *DigestSweeper) shouldFire(now time.Time) bool {
	at := d.At
	if at == "" {
		at = "09:00"
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
	if now.Hour() != h || now.Minute() != m {
		return false
	}
	cadence := d.Cadence
	if cadence == "" {
		cadence = "daily"
	}
	if cadence == "weekly" {
		return now.Weekday() == time.Monday
	}
	return true // daily
}

// Fire composes the digest and dispatches it. Exported so tests and a
// `chief digest --send` CLI can trigger it independently of the timer.
func (d *DigestSweeper) Fire(ctx context.Context) error {
	if d.Store == nil || d.Router == nil {
		return fmt.Errorf("digest: missing dependency (store/router)")
	}
	window := d.Window
	if window <= 0 {
		if d.Cadence == "weekly" {
			window = 7 * 24 * time.Hour
		} else {
			window = 24 * time.Hour
		}
	}
	body, err := d.Compose(ctx, window)
	if err != nil {
		return err
	}
	msg := messaging.Message{
		Urgency: messaging.UrgencyAttention,
		Title:   "Chief digest",
		Body:    body,
		Kind:    "digest",
		Ts:      d.now(),
	}
	// If explicit backends configured, fan out to just those (bypass
	// routing rules). Otherwise let the router pick per urgency.
	if len(d.Backends) > 0 {
		for _, name := range d.Backends {
			b, ok := d.Router.Backends[name]
			if !ok {
				slog.Warn("digest: unknown backend in config; skipping", "name", name)
				continue
			}
			if err := b.Send(ctx, msg); err != nil {
				slog.Warn("digest: backend send failed", "backend", name, "err", err)
			}
		}
		return nil
	}
	_ = d.Router.Dispatch(ctx, msg)
	return nil
}

// Compose builds the digest body from store data. Format is meant for
// glance-reading in Telegram/Slack/ntfy notifications, so it's compact
// with per-project one-liners and totals.
func (d *DigestSweeper) Compose(ctx context.Context, window time.Duration) (string, error) {
	projs, err := d.Store.ListProjects(ctx)
	if err != nil {
		return "", err
	}
	now := d.now()
	since := now.Add(-window)
	lastEvents, _ := d.Store.LastEventPerProject(ctx)
	openFlags, _ := d.Store.CountOpenFlags(ctx, "")

	var (
		totalPending, totalActive int
		completions              int64
		quietProjects            []string
	)
	type projLine struct {
		Name       string
		Pending    int
		Done       int
		Recent     int64 // completions in window
	}
	lines := make([]projLine, 0, len(projs))
	for _, p := range projs {
		tasks, _ := d.Store.ListTasks(ctx, store.TaskFilter{ProjectID: p.ID})
		pl := projLine{Name: p.Name}
		for _, t := range tasks {
			switch t.Status {
			case store.TaskPending:
				pl.Pending++
				totalPending++
			case store.TaskActive:
				totalActive++
			case store.TaskDone:
				pl.Done++
			}
		}
		perProj, _ := d.Store.CountEventsByKindSince(ctx, p.ID, since)
		pl.Recent = perProj["rescan.completed"]
		completions += pl.Recent

		if last, ok := lastEvents[p.ID]; ok {
			if now.Sub(last) > 3*24*time.Hour {
				quietProjects = append(quietProjects, fmt.Sprintf("%s (%s ago)", p.Name, humanAgo(now.Sub(last))))
			}
		}
		lines = append(lines, pl)
	}

	// Sort projects by recent completions DESC, then pending DESC.
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].Recent != lines[j].Recent {
			return lines[i].Recent > lines[j].Recent
		}
		return lines[i].Pending > lines[j].Pending
	})

	var b strings.Builder
	fmt.Fprintf(&b, "Window: last %s\n", humanAgo(window))
	fmt.Fprintf(&b, "Completions: %d · Pending: %d · Active: %d · Open flags: %d\n\n",
		completions, totalPending, totalActive, openFlags)
	if len(lines) > 0 {
		b.WriteString("Projects (by recent activity):\n")
		for _, pl := range lines {
			if pl.Recent == 0 && pl.Pending == 0 {
				continue // skip fully-idle empty projects
			}
			fmt.Fprintf(&b, "  · %s — %d pending / %d done / %d completions in window\n",
				pl.Name, pl.Pending, pl.Done, pl.Recent)
		}
		b.WriteString("\n")
	}
	if len(quietProjects) > 0 {
		b.WriteString("Quiet (>3d no activity): ")
		b.WriteString(strings.Join(quietProjects, ", "))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (d *DigestSweeper) now() time.Time {
	if d.Clock != nil {
		return d.Clock()
	}
	return time.Now()
}

// humanAgo formats a duration compactly ("3h", "2d", "45m").
func humanAgo(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
