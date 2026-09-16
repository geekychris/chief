package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/geekychris/chief/internal/attention"
	"github.com/geekychris/chief/internal/cmux"
	"github.com/geekychris/chief/internal/notify"
	"github.com/geekychris/chief/internal/project"
	"github.com/geekychris/chief/internal/store"
)

// fakeCmux returns a fixed surface list — enough for the sweeper's needs.
type fakeCmux struct{ surfaces []cmux.Surface }

func (f *fakeCmux) ListSurfaces(_ context.Context) ([]cmux.Surface, error) {
	return f.surfaces, nil
}

// setup makes a project, binds a surface to it, wires the sweeper, and
// returns everything the tests need to advance the clock deterministically.
func setup(t *testing.T, surfaceRef, surfaceTitle string) (*IdleSweeper, *store.Store, *project.Manager, string, *[]cmux.Surface, *clock) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mgr := project.New(st)
	p, err := mgr.Register(context.Background(), project.RegisterOpts{Path: dir, Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	// Bind a cmux surface via the YAML mutator.
	_, err = mgr.WriteYAML(context.Background(), p.ID, func(pf *project.ProjectFile) {
		pf.Cmux.SurfaceID = surfaceRef
	})
	if err != nil {
		t.Fatal(err)
	}
	surfaces := []cmux.Surface{{Ref: surfaceRef, Title: surfaceTitle, IsClaude: true}}
	fc := &fakeCmux{surfaces: surfaces}
	clk := &clock{now: time.Unix(1_000_000, 0)}
	sw := &IdleSweeper{
		Store:     st,
		Projects:  mgr,
		Cmux:      fc,
		Attention: &attention.Manager{Store: st, Notifier: notify.NoopNotifier{}},
		IdleTimeout: 30 * time.Minute,
		Clock:     clk.Now,
	}
	// Return the slice pointer so tests can mutate cmux state between sweeps.
	return sw, st, mgr, p.ID, &fc.surfaces, clk
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time     { return c.now }
func (c *clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// TestSweep_RaisesStaleBindingFlagWhenSurfaceGone — the core "cmux was
// restarted and now your binding is dead" signal.
func TestSweep_RaisesStaleBindingFlagWhenSurfaceGone(t *testing.T) {
	sw, st, _, pid, surfaces, _ := setup(t, "surface:42", "working…")
	if err := sw.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Nothing should fire yet — surface is alive.
	flags, _ := st.ListFlags(context.Background(), store.FlagFilter{ProjectID: pid, OpenOnly: true})
	if len(flags) != 0 {
		t.Fatalf("no flag expected while surface alive, got %d", len(flags))
	}
	// Simulate cmux restart — the surface disappears.
	*surfaces = nil
	if err := sw.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	flags, _ = st.ListFlags(context.Background(), store.FlagFilter{ProjectID: pid, OpenOnly: true})
	if len(flags) != 1 {
		t.Fatalf("expected 1 stale-binding flag, got %d", len(flags))
	}
	if flags[0].Urgency != store.UrgencyInfo {
		t.Errorf("stale-binding flag urgency: got %q want %q", flags[0].Urgency, store.UrgencyInfo)
	}
}

// TestSweep_RaisesIdleFlagAfterTimeout — the title-unchanged heuristic.
func TestSweep_RaisesIdleFlagAfterTimeout(t *testing.T) {
	sw, st, _, pid, _, clk := setup(t, "surface:42", "steady title")
	// First sweep: observes the title. No flag.
	if err := sw.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Advance 20min — under threshold. No flag.
	clk.Advance(20 * time.Minute)
	if err := sw.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	flags, _ := st.ListFlags(context.Background(), store.FlagFilter{ProjectID: pid, OpenOnly: true})
	if len(flags) != 0 {
		t.Fatalf("no flag expected under 30min, got %d", len(flags))
	}
	// Advance to 35min — over threshold. Flag should fire.
	clk.Advance(15 * time.Minute)
	if err := sw.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	flags, _ = st.ListFlags(context.Background(), store.FlagFilter{ProjectID: pid, OpenOnly: true})
	if len(flags) != 1 {
		t.Fatalf("expected 1 idle flag after 35min, got %d", len(flags))
	}
}

// TestSweep_TitleChangeResetsIdleTimer — activity in the pane should
// clear the idle countdown.
func TestSweep_TitleChangeResetsIdleTimer(t *testing.T) {
	sw, st, _, pid, surfaces, clk := setup(t, "surface:42", "step 1")
	_ = sw.Sweep(context.Background())
	clk.Advance(25 * time.Minute)
	// Pane got a new title — activity happened.
	(*surfaces)[0].Title = "step 2"
	_ = sw.Sweep(context.Background())
	clk.Advance(20 * time.Minute)
	_ = sw.Sweep(context.Background())
	// Total time since first sweep: 45min. But we saw activity at 25min,
	// so effective idle time is only 20min — no flag should fire.
	flags, _ := st.ListFlags(context.Background(), store.FlagFilter{ProjectID: pid, OpenOnly: true})
	if len(flags) != 0 {
		t.Errorf("expected 0 flags after title change, got %d", len(flags))
	}
}

// TestSweep_RespectsPerProjectDisable — Idle.Disable in .chief/project.yaml.
func TestSweep_RespectsPerProjectDisable(t *testing.T) {
	sw, st, mgr, pid, surfaces, clk := setup(t, "surface:42", "quiet")
	_, err := mgr.WriteYAML(context.Background(), pid, func(pf *project.ProjectFile) {
		pf.Idle.Disable = true
	})
	if err != nil {
		t.Fatal(err)
	}
	// Even after title-gone + long idle, no flag.
	*surfaces = nil
	clk.Advance(2 * time.Hour)
	_ = sw.Sweep(context.Background())
	flags, _ := st.ListFlags(context.Background(), store.FlagFilter{ProjectID: pid, OpenOnly: true})
	if len(flags) != 0 {
		t.Errorf("expected 0 flags when project has Idle.Disable, got %d", len(flags))
	}
}

// TestSweep_UnboundProjectIsSkipped — no cmux surface bound = no work.
func TestSweep_UnboundProjectIsSkipped(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "test.db"))
	defer st.Close()
	mgr := project.New(st)
	p, _ := mgr.Register(context.Background(), project.RegisterOpts{Path: dir, Name: "unbound"})

	sw := &IdleSweeper{
		Store: st, Projects: mgr, Cmux: &fakeCmux{},
		Attention: &attention.Manager{Store: st, Notifier: notify.NoopNotifier{}},
		Clock: (&clock{now: time.Unix(1_000_000, 0)}).Now,
	}
	_ = sw.Sweep(context.Background())
	flags, _ := st.ListFlags(context.Background(), store.FlagFilter{ProjectID: p.ID, OpenOnly: true})
	if len(flags) != 0 {
		t.Errorf("expected 0 flags for unbound project, got %d", len(flags))
	}
}
