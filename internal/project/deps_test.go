package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/geekychris/chief/internal/store"
)

// TestRescan_BlocksBy_TurnsPendingIntoBlocked — the core behaviour.
// Task B has [blocked-by:aaaa]. Until A is done, B is blocked.
func TestRescan_BlocksBy_TurnsPendingIntoBlocked(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mgr := New(st)

	backlog := `## Backlog
- [ ] {id:aaaa} First task
- [ ] {id:bbbb} Depends on first [blocked-by:aaaa]
`
	_ = os.WriteFile(filepath.Join(dir, "backlog.md"), []byte(backlog), 0o644)
	p, _ := mgr.Register(context.Background(), RegisterOpts{Path: dir, Name: "t"})
	if _, err := mgr.Rescan(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	// bbbb should be blocked; aaaa pending.
	a, _ := st.GetTask(context.Background(), "aaaa")
	b, _ := st.GetTask(context.Background(), "bbbb")
	if a.Status != store.TaskPending {
		t.Errorf("aaaa: want pending, got %s", a.Status)
	}
	if b.Status != store.TaskBlocked {
		t.Errorf("bbbb: want blocked (blocker not done), got %s", b.Status)
	}
	if len(b.BlockedBy) != 1 || b.BlockedBy[0] != "aaaa" {
		t.Errorf("bbbb BlockedBy: want [aaaa], got %v", b.BlockedBy)
	}
}

// TestRescan_BlocksBy_ClearsAfterBlockerDone — mark A done, B should
// go back to pending on the next rescan.
func TestRescan_BlocksBy_ClearsAfterBlockerDone(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "test.db"))
	defer st.Close()
	mgr := New(st)

	before := `## Backlog
- [ ] {id:aaaa} First
- [ ] {id:bbbb} Depends [blocked-by:aaaa]
`
	_ = os.WriteFile(filepath.Join(dir, "backlog.md"), []byte(before), 0o644)
	p, _ := mgr.Register(context.Background(), RegisterOpts{Path: dir, Name: "t"})
	_, _ = mgr.Rescan(context.Background(), p.ID)

	// User marks A done. Rescan sweeps it to completedlog; B's blocker
	// is now in completedlog. B should return to pending.
	after := `## Backlog
- [x] {id:aaaa} First
- [ ] {id:bbbb} Depends [blocked-by:aaaa]
`
	_ = os.WriteFile(filepath.Join(dir, "backlog.md"), []byte(after), 0o644)
	_, _ = mgr.Rescan(context.Background(), p.ID)

	b, _ := st.GetTask(context.Background(), "bbbb")
	if b.Status != store.TaskPending {
		t.Errorf("after blocker done, bbbb: want pending, got %s", b.Status)
	}
}

// TestRescan_BlocksBy_MultipleBlockers — task blocked by A AND B; still
// blocked until BOTH are done.
func TestRescan_BlocksBy_MultipleBlockers(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "test.db"))
	defer st.Close()
	mgr := New(st)

	backlog := `## Backlog
- [ ] {id:aaaa} A
- [x] {id:bbbb} B
- [ ] {id:cccc} Depends on both [blocked-by:aaaa,bbbb]
`
	_ = os.WriteFile(filepath.Join(dir, "backlog.md"), []byte(backlog), 0o644)
	p, _ := mgr.Register(context.Background(), RegisterOpts{Path: dir, Name: "t"})
	_, _ = mgr.Rescan(context.Background(), p.ID)

	c, _ := st.GetTask(context.Background(), "cccc")
	if c.Status != store.TaskBlocked {
		t.Errorf("cccc: want blocked (A still pending), got %s", c.Status)
	}
	if len(c.BlockedBy) != 2 {
		t.Errorf("cccc BlockedBy: want 2 ids, got %v", c.BlockedBy)
	}
}

// TestRescan_BlocksTag_PropagatesToBlocksField — the reverse direction.
// Task A has [blocks:bbbb]; that tag just gets stored (doesn't imply
// symmetry — you'd typically use only one side for cleanliness).
func TestRescan_BlocksTag_PropagatesToBlocksField(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "test.db"))
	defer st.Close()
	mgr := New(st)

	_ = os.WriteFile(filepath.Join(dir, "backlog.md"),
		[]byte("## Backlog\n- [ ] {id:aaaa} A [blocks:bbbb]\n- [ ] {id:bbbb} B\n"), 0o644)
	p, _ := mgr.Register(context.Background(), RegisterOpts{Path: dir, Name: "t"})
	_, _ = mgr.Rescan(context.Background(), p.ID)

	a, _ := st.GetTask(context.Background(), "aaaa")
	if len(a.Blocks) != 1 || a.Blocks[0] != "bbbb" {
		t.Errorf("aaaa Blocks: want [bbbb], got %v", a.Blocks)
	}
	// B without [blocked-by:aaaa] stays pending — Blocks is a hint, not a
	// two-way derivation. Chief could infer this later but v1 keeps the
	// two directions independent.
	b, _ := st.GetTask(context.Background(), "bbbb")
	if b.Status != store.TaskPending {
		t.Errorf("bbbb: want pending (no blocked-by), got %s", b.Status)
	}
}
