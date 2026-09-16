package attention

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/geekychris/chief/internal/notify"
	"github.com/geekychris/chief/internal/store"
)

func TestDNDWindow_InWindow(t *testing.T) {
	// 22:00 → 07:00 wraps midnight.
	night := DNDWindow{Start: "22:00", End: "07:00"}
	cases := []struct {
		hm   string
		want bool
	}{
		{"21:59", false},
		{"22:00", true},
		{"23:30", true},
		{"00:00", true},
		{"06:59", true},
		{"07:00", false},
		{"12:00", false},
	}
	for _, c := range cases {
		got := night.InWindow(clockAt(c.hm))
		if got != c.want {
			t.Errorf("night %s: got %v want %v", c.hm, got, c.want)
		}
	}
	// 12:00 → 13:00 lunch window (no wrap).
	lunch := DNDWindow{Start: "12:00", End: "13:00"}
	if !lunch.InWindow(clockAt("12:30")) {
		t.Error("12:30 should be in lunch window")
	}
	if lunch.InWindow(clockAt("13:00")) {
		t.Error("13:00 is the end boundary; should not be in window")
	}
	// Weekday filter — only weekdays.
	weekdays := DNDWindow{Start: "22:00", End: "07:00", Weekdays: []int{1, 2, 3, 4, 5}}
	weds := time.Date(2026, 3, 4, 23, 0, 0, 0, time.Local) // 2026-03-04 is a Wednesday
	if !weekdays.InWindow(weds) {
		t.Error("Wed 23:00 should be in weekday window")
	}
	sun := time.Date(2026, 3, 8, 23, 0, 0, 0, time.Local) // Sunday
	if weekdays.InWindow(sun) {
		t.Error("Sun 23:00 should NOT be in weekday-only window")
	}
	// Disable overrides everything.
	off := DNDWindow{Start: "00:00", End: "23:59", Disable: true}
	if off.InWindow(clockAt("12:00")) {
		t.Error("Disable=true should always be out of window")
	}
	// Empty window is always false.
	empty := DNDWindow{}
	if empty.InWindow(clockAt("12:00")) {
		t.Error("empty DNDWindow should never match")
	}
}

// clockAt returns "today at HH:MM" in the local zone. Used by table
// tests so the day/month/year don't matter.
func clockAt(hm string) time.Time {
	m, _ := parseHM(hm)
	now := time.Now().Local()
	return time.Date(now.Year(), now.Month(), now.Day(), m/60, m%60, 0, 0, time.Local)
}

// TestManager_DNDSuppressesNotification — end-to-end: DNDWindowFor
// hooked to a window that covers "now" → flag persists but no
// notification fires.
func TestManager_DNDSuppressesNotification(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateProject(context.Background(), store.Project{
		ID: "p", Path: dir, Name: "p", SpawnMode: "attach", State: "idle",
	}); err != nil {
		t.Fatal(err)
	}
	rec := &notify.RecordingNotifier{}
	m := &Manager{
		Store: st, Notifier: rec,
		DNDWindowFor: func(_ string) DNDWindow {
			// Any-time window that swallows every "now".
			return DNDWindow{Start: "00:00", End: "23:59"}
		},
	}
	res, err := m.Raise(context.Background(), RaiseOpts{ProjectID: "p", Question: "quiet please"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.InDND {
		t.Error("expected InDND=true")
	}
	if res.NotifiedMacOS {
		t.Error("expected NotifiedMacOS=false in DND window")
	}
	if len(rec.Sent) != 0 {
		t.Errorf("expected 0 notifications, got %d", len(rec.Sent))
	}
	// Flag still persisted for the inbox.
	flags, _ := st.ListFlags(context.Background(), store.FlagFilter{ProjectID: "p", OpenOnly: true})
	if len(flags) != 1 {
		t.Errorf("expected 1 open flag, got %d", len(flags))
	}
}
