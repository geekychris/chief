package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/geekychris/chief/internal/attention"
	"github.com/geekychris/chief/internal/notify"
	"github.com/geekychris/chief/internal/store"
)

// TestRescan_RaisesNextTaskFlagOnCompletion — the end-to-end signal chain
// for task 2c8f: user marks task [x] → Rescan sweeps it → Rescan raises a
// next-task flag suggesting the top-priority pending task.
func TestRescan_RaisesNextTaskFlagOnCompletion(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	rec := &notify.RecordingNotifier{}
	att := &attention.Manager{Store: st, Notifier: rec}
	mgr := New(st)
	mgr.Attention = att

	// Seed a project + backlog with two tasks: one to be completed, one
	// pending as the "next" suggestion.
	backlog := `## Backlog
- [ ] {id:aaaa} Complete me [priority:5]
- [ ] {id:bbbb} Suggest me next [priority:3]
`
	if err := os.WriteFile(filepath.Join(dir, "backlog.md"), []byte(backlog), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := mgr.Register(context.Background(), RegisterOpts{Path: dir, Name: "test"})
	if err != nil {
		t.Fatal(err)
	}

	// Initial rescan (should not raise — nothing transitioned).
	if _, err := mgr.Rescan(context.Background(), proj.ID); err != nil {
		t.Fatal(err)
	}
	flags, _ := st.ListFlags(context.Background(), store.FlagFilter{ProjectID: proj.ID})
	if len(flags) != 0 {
		t.Fatalf("expected 0 flags after boot rescan, got %d", len(flags))
	}

	// User marks the first task done — write updated backlog.md.
	after := `## Backlog
- [x] {id:aaaa} Complete me [priority:5]
- [ ] {id:bbbb} Suggest me next [priority:3]
`
	if err := os.WriteFile(filepath.Join(dir, "backlog.md"), []byte(after), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Rescan(context.Background(), proj.ID); err != nil {
		t.Fatal(err)
	}

	// Exactly one next-task flag should now exist, pointing at the
	// pending task.
	flags, _ = st.ListFlags(context.Background(), store.FlagFilter{ProjectID: proj.ID, OpenOnly: true})
	if len(flags) != 1 {
		t.Fatalf("expected 1 next-task flag, got %d", len(flags))
	}
	f := flags[0]
	if f.Kind != store.FlagKindNextTask {
		t.Errorf("kind: got %q want %q", f.Kind, store.FlagKindNextTask)
	}
	if f.SuggestedID == nil || *f.SuggestedID != "bbbb" {
		t.Errorf("suggested_id: got %v want bbbb", f.SuggestedID)
	}
	if len(rec.Sent) != 1 {
		t.Errorf("expected 1 macOS notification, got %d", len(rec.Sent))
	}
}

// TestRescan_NoFlagWhenNoPendingRemain — user does the last task; no next-task
// flag because there's nothing to suggest.
func TestRescan_NoFlagWhenNoPendingRemain(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "test.db"))
	defer st.Close()
	att := &attention.Manager{Store: st, Notifier: notify.NoopNotifier{}}
	mgr := New(st)
	mgr.Attention = att

	_ = os.WriteFile(filepath.Join(dir, "backlog.md"),
		[]byte("## Backlog\n- [ ] {id:zzzz} last one\n"), 0o644)
	proj, _ := mgr.Register(context.Background(), RegisterOpts{Path: dir, Name: "test"})
	_, _ = mgr.Rescan(context.Background(), proj.ID)

	_ = os.WriteFile(filepath.Join(dir, "backlog.md"),
		[]byte("## Backlog\n- [x] {id:zzzz} last one\n"), 0o644)
	_, _ = mgr.Rescan(context.Background(), proj.ID)

	flags, _ := st.ListFlags(context.Background(), store.FlagFilter{ProjectID: proj.ID, OpenOnly: true})
	if len(flags) != 0 {
		t.Errorf("expected 0 flags when nothing pending remains, got %d", len(flags))
	}
}

// TestRescan_NoFlagOnBootWithPreExistingDone — a project whose backlog.md
// has [x] items before chief ever saw it shouldn't trigger a spurious
// next-task notification.
func TestRescan_NoFlagOnBootWithPreExistingDone(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "test.db"))
	defer st.Close()
	att := &attention.Manager{Store: st, Notifier: notify.NoopNotifier{}}
	mgr := New(st)
	mgr.Attention = att

	// [x] and [ ] present at first registration.
	_ = os.WriteFile(filepath.Join(dir, "backlog.md"),
		[]byte("## Backlog\n- [x] {id:aaaa} already done\n- [ ] {id:bbbb} pending\n"), 0o644)
	proj, _ := mgr.Register(context.Background(), RegisterOpts{Path: dir, Name: "test"})
	_, _ = mgr.Rescan(context.Background(), proj.ID)

	flags, _ := st.ListFlags(context.Background(), store.FlagFilter{ProjectID: proj.ID, OpenOnly: true})
	if len(flags) != 0 {
		t.Errorf("expected 0 flags at first-time boot, got %d (spurious next-task)", len(flags))
	}
}
