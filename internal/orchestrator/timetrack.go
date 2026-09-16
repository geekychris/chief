package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/geekychris/chief/internal/cmux"
	"github.com/geekychris/chief/internal/store"
)

const (
	// DefaultTimeTrackInterval is the sample cadence. 60s balances
	// resolution against DB write volume; project_time_samples grows
	// at ~1440 rows/day/project which is fine.
	DefaultTimeTrackInterval = 60 * time.Second
)

// TimeTracker (3206) samples cmux surfaces periodically and attributes
// each sample to the project whose Path is a prefix of the surface's
// CWD. Only counts surfaces that are Claude sessions (IsClaude=true)
// so idle terminal panes don't inflate the numbers.
//
// Not exact — a session sitting quietly still counts. The alternative
// (only count while the model is actively responding) would need a
// cmux status API we don't yet have. For "$/hour spent" ballparks this
// is close enough.
type TimeTracker struct {
	Store    *store.Store
	Cmux     *cmux.Client
	Interval time.Duration
	Clock    func() time.Time
}

func (t *TimeTracker) Run(ctx context.Context) {
	interval := t.Interval
	if interval <= 0 {
		interval = DefaultTimeTrackInterval
	}
	// Skip if cmux unavailable — no point sampling with nothing to sample.
	if t.Cmux == nil {
		slog.Info("time tracker: cmux nil, disabled")
		return
	}
	// Delay first sample so re-attach finishes.
	select {
	case <-time.After(30 * time.Second):
	case <-ctx.Done():
		return
	}
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		if err := t.Sample(ctx, int64(interval.Seconds())); err != nil {
			slog.Debug("time tracker: sample failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
	}
}

// Sample runs one attribution pass and inserts one row per attributed
// (project, source) pair.
func (t *TimeTracker) Sample(ctx context.Context, seconds int64) error {
	surfaces, err := t.Cmux.ListSurfaces(ctx)
	if err != nil {
		return err
	}
	projs, err := t.Store.ListProjects(ctx)
	if err != nil {
		return err
	}
	// Sort projects by path length DESC so nested projects win over their parents.
	type pp struct {
		id, path string
	}
	pps := make([]pp, 0, len(projs))
	for _, p := range projs {
		pps = append(pps, pp{id: p.ID, path: strings.TrimRight(p.Path, "/")})
	}
	// selection-sort-ish; project counts are small
	for i := 0; i < len(pps); i++ {
		for j := i + 1; j < len(pps); j++ {
			if len(pps[j].path) > len(pps[i].path) {
				pps[i], pps[j] = pps[j], pps[i]
			}
		}
	}

	// Deduplicate: one sample per project even if multiple Claude surfaces
	// share the same cwd tree.
	touched := map[string]struct{}{}
	for _, s := range surfaces {
		if !s.IsClaude || s.CWD == "" {
			continue
		}
		cwd := strings.TrimRight(s.CWD, "/")
		for _, p := range pps {
			if cwd == p.path || strings.HasPrefix(cwd, p.path+"/") {
				touched[p.id] = struct{}{}
				break
			}
		}
	}
	now := t.now()
	for pid := range touched {
		_ = t.Store.InsertTimeSample(ctx, store.TimeSample{
			ProjectID: pid, Ts: now, Seconds: seconds, Source: "cmux",
		})
	}
	return nil
}

func (t *TimeTracker) now() time.Time {
	if t.Clock != nil {
		return t.Clock()
	}
	return time.Now().UTC()
}
