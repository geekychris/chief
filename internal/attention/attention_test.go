package attention

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/geekychris/chief/internal/notify"
	"github.com/geekychris/chief/internal/store"
)

// mustStore opens a fresh SQLite in tmp with a single seeded project.
func mustStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	pid := "proj_test"
	if err := st.CreateProject(context.Background(), store.Project{
		ID: pid, Path: dir, Name: "test", SpawnMode: "attach", State: "idle",
	}); err != nil {
		t.Fatal(err)
	}
	return st, pid
}

func TestManager_RaiseInsertsFlagAndNotifies(t *testing.T) {
	st, pid := mustStore(t)
	rec := &notify.RecordingNotifier{}
	m := &Manager{Store: st, Notifier: rec}

	res, err := m.Raise(context.Background(), RaiseOpts{
		ProjectID: pid,
		Question:  "Which auth strategy — JWT or sessions?",
		Urgency:   store.UrgencyAttention,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Flag.ID == "" {
		t.Error("expected flag ID to be minted")
	}
	if !res.NotifiedMacOS {
		t.Error("expected NotifiedMacOS=true")
	}
	if len(rec.Sent) != 1 {
		t.Fatalf("want 1 notification, got %d", len(rec.Sent))
	}
	if rec.Sent[0].Body != "Which auth strategy — JWT or sessions?" {
		t.Errorf("body mismatch: %q", rec.Sent[0].Body)
	}
	// Sanity: flag row is queryable.
	got, err := st.GetFlag(context.Background(), res.Flag.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AckAt != nil {
		t.Error("newly-raised flag should be unresolved")
	}
}

func TestManager_Coalesces_NearDuplicateWithinAggWindow(t *testing.T) {
	st, pid := mustStore(t)
	rec := &notify.RecordingNotifier{}
	m := &Manager{Store: st, Notifier: rec, AggWindow: time.Minute}

	// First raise.
	r1, err := m.Raise(context.Background(), RaiseOpts{
		ProjectID: pid, Question: "which auth strategy jwt or sessions",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Near-duplicate — different wording but same key tokens.
	r2, err := m.Raise(context.Background(), RaiseOpts{
		ProjectID: pid, Question: "WHICH auth STRATEGY jwt or sessions??",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Coalesced {
		t.Error("expected second raise to coalesce")
	}
	if r1.Flag.ID != r2.Flag.ID {
		t.Errorf("coalesced flag id mismatch: %s vs %s", r1.Flag.ID, r2.Flag.ID)
	}
	// Only one flag row created.
	all, err := st.ListFlags(context.Background(), store.FlagFilter{ProjectID: pid, OpenOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 open flag, got %d", len(all))
	}
	// But two notifications — the second confirming re-raise.
	if len(rec.Sent) != 2 {
		t.Errorf("expected 2 notifications, got %d", len(rec.Sent))
	}
}

func TestManager_RateLimit_SkipsNotificationButKeepsFlag(t *testing.T) {
	st, pid := mustStore(t)
	rec := &notify.RecordingNotifier{}
	m := &Manager{Store: st, Notifier: rec, RateLimit: 2, RateWindow: time.Minute}

	// Three distinct questions in quick succession.
	for i, q := range []string{"one", "two", "three"} {
		_, err := m.Raise(context.Background(), RaiseOpts{ProjectID: pid, Question: q})
		if err != nil {
			t.Fatalf("raise %d: %v", i, err)
		}
	}
	// All three should be recorded.
	all, _ := st.ListFlags(context.Background(), store.FlagFilter{ProjectID: pid, OpenOnly: true})
	if len(all) != 3 {
		t.Errorf("expected 3 flags, got %d", len(all))
	}
	// But only 2 notifications should have fired (limit=2).
	if len(rec.Sent) != 2 {
		t.Errorf("expected 2 notifications (rate-limited), got %d", len(rec.Sent))
	}
}

func TestManager_Snooze_SuppressesNotifications(t *testing.T) {
	st, pid := mustStore(t)
	rec := &notify.RecordingNotifier{}
	m := &Manager{Store: st, Notifier: rec}
	m.Snooze(pid, time.Now().Add(10*time.Minute))

	res, err := m.Raise(context.Background(), RaiseOpts{ProjectID: pid, Question: "shush"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.SnoozedProject {
		t.Error("expected SnoozedProject=true")
	}
	if res.NotifiedMacOS {
		t.Error("expected NotifiedMacOS=false while snoozed")
	}
	if len(rec.Sent) != 0 {
		t.Errorf("expected 0 notifications, got %d", len(rec.Sent))
	}
	// Flag row is still persisted so it appears in the inbox.
	all, _ := st.ListFlags(context.Background(), store.FlagFilter{ProjectID: pid, OpenOnly: true})
	if len(all) != 1 {
		t.Errorf("expected 1 open flag, got %d", len(all))
	}
}

func TestManager_UrgencyInfo_BadgeOnly(t *testing.T) {
	st, pid := mustStore(t)
	rec := &notify.RecordingNotifier{}
	m := &Manager{Store: st, Notifier: rec}
	res, err := m.Raise(context.Background(), RaiseOpts{
		ProjectID: pid, Question: "fyi", Urgency: store.UrgencyInfo,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.NotifiedMacOS {
		t.Error("info urgency should NOT trigger macOS notification")
	}
	if len(rec.Sent) != 0 {
		t.Errorf("expected 0 notifications for info urgency, got %d", len(rec.Sent))
	}
}

func TestManager_Answer_SetsAck(t *testing.T) {
	st, pid := mustStore(t)
	m := &Manager{Store: st, Notifier: notify.NoopNotifier{}}

	res, _ := m.Raise(context.Background(), RaiseOpts{ProjectID: pid, Question: "q"})
	if _, err := m.Answer(context.Background(), res.Flag.ID, "the answer"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetFlag(context.Background(), res.Flag.ID)
	if got.AckAt == nil {
		t.Error("expected AckAt set after Answer")
	}
	if got.AckReply != "the answer" {
		t.Errorf("AckReply: got %q want %q", got.AckReply, "the answer")
	}
	if got.Resolution != store.ResolutionAnswered {
		t.Errorf("Resolution: got %q want %q", got.Resolution, store.ResolutionAnswered)
	}
	// Open-count should drop.
	n, _ := st.CountOpenFlags(context.Background(), pid)
	if n != 0 {
		t.Errorf("open count: got %d want 0", n)
	}
}

func TestManager_ListFlags_SortByUrgencyThenAge(t *testing.T) {
	st, pid := mustStore(t)
	m := &Manager{Store: st, Notifier: notify.NoopNotifier{}}

	// Info < attention < urgent — but urgent first in output.
	_, _ = m.Raise(context.Background(), RaiseOpts{ProjectID: pid, Question: "info-old", Urgency: store.UrgencyInfo})
	time.Sleep(2 * time.Millisecond)
	_, _ = m.Raise(context.Background(), RaiseOpts{ProjectID: pid, Question: "attention-mid", Urgency: store.UrgencyAttention})
	time.Sleep(2 * time.Millisecond)
	_, _ = m.Raise(context.Background(), RaiseOpts{ProjectID: pid, Question: "urgent-new", Urgency: store.UrgencyUrgent})

	list, err := st.ListFlags(context.Background(), store.FlagFilter{ProjectID: pid, OpenOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("want 3, got %d", len(list))
	}
	if list[0].Urgency != store.UrgencyUrgent {
		t.Errorf("first should be urgent, got %s", list[0].Urgency)
	}
	if list[2].Urgency != store.UrgencyInfo {
		t.Errorf("last should be info, got %s", list[2].Urgency)
	}
}

func TestAggregationKey_StableAcrossWordingChanges(t *testing.T) {
	a := AggregationKey("pid", store.FlagKindQuestion, "Which auth strategy — JWT or sessions?")
	b := AggregationKey("pid", store.FlagKindQuestion, "which AUTH   strategy — jwt or SESSIONS?")
	if a != b {
		t.Errorf("expected same key across wording variations, got %s vs %s", a, b)
	}
	c := AggregationKey("pid", store.FlagKindQuestion, "which db driver postgres or mysql")
	if a == c {
		t.Error("expected different key for different question")
	}
}
