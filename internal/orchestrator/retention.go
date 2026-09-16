package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/geekychris/chief/internal/archive"
	"github.com/geekychris/chief/internal/store"
)

const (
	// DefaultRetentionDays is how long we keep events in SQLite before
	// they're archived + deleted.
	DefaultRetentionDays = 90
	// DefaultSweepInterval matches the daily-ticker cadence: retention
	// doesn't need to be minute-precise, and running less often keeps
	// idle CPU near zero.
	DefaultSweepInterval = 24 * time.Hour
	// batchSize caps how many events we pull + archive per sweep so a
	// huge backlog (e.g., first sweep after enabling retention on a
	// long-lived DB) doesn't spike memory.
	batchSize = 5000
)

// RetentionSweeper runs the daily retention pass: events with ts <
// (now - RetentionDays) get archived to gzipped JSONL then deleted.
type RetentionSweeper struct {
	Store   *store.Store
	Archive *archive.Writer
	// RetentionDays: overrides default when > 0. A negative value
	// disables retention (Sweep is a no-op).
	RetentionDays int
	// Interval: overrides default when > 0.
	Interval time.Duration
	// Clock: test seam.
	Clock func() time.Time
}

// Run blocks until ctx is cancelled, ticking through Sweep on Interval.
// A first sweep runs 60s after boot so the daemon isn't racing schema
// migrations or re-attach.
func (r *RetentionSweeper) Run(ctx context.Context) {
	if r.RetentionDays < 0 {
		slog.Info("retention sweeper: disabled by config (retention_days < 0)")
		return
	}
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultSweepInterval
	}
	select {
	case <-time.After(60 * time.Second):
	case <-ctx.Done():
		return
	}
	if err := r.Sweep(ctx); err != nil {
		slog.Warn("retention sweeper: first sweep failed", "err", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.Sweep(ctx); err != nil {
				slog.Warn("retention sweeper: tick failed", "err", err)
			}
		}
	}
}

// Sweep runs one archive+delete pass. Loops in batches until nothing
// old remains (or an error surfaces). Exported so tests + a future
// `chief events sweep-now` CLI can drive it.
func (r *RetentionSweeper) Sweep(ctx context.Context) error {
	if r.Store == nil || r.Archive == nil {
		return fmt.Errorf("retention sweeper: missing dependency (store/archive)")
	}
	days := r.RetentionDays
	if days <= 0 {
		days = DefaultRetentionDays
	}
	cutoff := r.now().Add(-time.Duration(days) * 24 * time.Hour)

	total := 0
	for {
		events, err := r.Store.EventsOlderThan(ctx, cutoff, batchSize)
		if err != nil {
			return fmt.Errorf("query old events: %w", err)
		}
		if len(events) == 0 {
			break
		}
		written, werr := r.Archive.Append(events)
		if werr != nil && len(written) == 0 {
			// Archive failed entirely — keep the rows so a later sweep retries.
			return fmt.Errorf("archive: %w", werr)
		}
		if err := r.Store.DeleteEventsByIDs(ctx, written); err != nil {
			return fmt.Errorf("delete archived events: %w", err)
		}
		total += len(written)
		if werr != nil {
			// Partial: some rows written, then error. Log + retry next tick.
			slog.Warn("retention sweeper: partial archive", "written", len(written), "err", werr)
			return werr
		}
		if len(events) < batchSize {
			break
		}
	}
	if total > 0 {
		slog.Info("retention sweeper: archived + purged", "count", total, "cutoff", cutoff.Format(time.RFC3339))
	}
	return nil
}

func (r *RetentionSweeper) now() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now().UTC()
}
