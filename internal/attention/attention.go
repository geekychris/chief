// Package attention manages the "flag for human" lifecycle: raising flags,
// coalescing near-duplicates within a window, per-project rate-limiting so
// a chatty session doesn't flood macOS notifications, ack/reply, and
// (opt-in) DND / snooze cursors.
//
// The store is the source of truth; this package holds no in-memory
// mutable state that would be lost across chiefd restarts. The Manager
// composes a store.Store (persistence), a notify.Notifier (side channel
// to macOS), and a Clock (test seam).
package attention

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/geekychris/chief/internal/messaging"
	"github.com/geekychris/chief/internal/notify"
	"github.com/geekychris/chief/internal/store"
)

// DefaultRateLimit is the number of notifications a single project can
// emit per RateWindow. Extra flags still create rows in the flags table
// (they show up in the inbox and menu-bar count); we just skip firing
// the macOS notification. This preserves auditability while keeping the
// notification stream signal-heavy.
const (
	DefaultRateLimit = 5
	DefaultRateWindow = 10 * time.Minute
	DefaultAggWindow  = 2 * time.Minute
)

// Manager coordinates flag persistence + notification delivery.
type Manager struct {
	Store    *store.Store
	Notifier notify.Notifier
	// Clock is the time source; defaults to time.Now.
	Clock func() time.Time
	// RateLimit / RateWindow / AggWindow are exposed for tuning; zero
	// values fall back to the Default* constants.
	RateLimit  int
	RateWindow time.Duration
	AggWindow  time.Duration

	// DNDWindowFor returns the resolved DND schedule for a project. If
	// nil, no DND is applied. Wired at chiefd boot from global config
	// + per-project overrides; kept as a callback so this package
	// doesn't need to import config/project (avoids an import cycle).
	DNDWindowFor func(projectID string) DNDWindow

	// Router, when non-nil, fans out each flag to configured messaging
	// backends (log file, ntfy, Pushover, Telegram, Slack, etc.). DND
	// and snooze still apply — the router only runs when a real macOS
	// notification would have been fired. `info` urgency is delivered
	// only when the router's per-urgency rules include it (default
	// routing excludes info).
	Router *messaging.Router

	// snoozeUntil tracks per-project "hold notifications" cursors set via
	// Snooze(). Reset on chiefd restart, which is fine — snoozes are
	// meant to be short (minutes to hours). Not persisted to avoid a
	// migration for what's semantically ephemeral.
	mu          sync.Mutex
	snoozeUntil map[string]time.Time
}

// RaiseOpts describes a new flag. ProjectID is required; everything else
// has a sensible default.
type RaiseOpts struct {
	ProjectID   string
	SessionID   string
	Kind        store.FlagKind
	Urgency     store.FlagUrgency
	Question    string
	SuggestedID string // for kind=next_task
}

// RaiseResult carries the resulting flag row plus a hint about what
// notification path was taken.
type RaiseResult struct {
	Flag           store.Flag
	Coalesced      bool // merged into an existing open flag with same agg_key
	NotifiedMacOS  bool
	RateLimited    bool // flag stored, notification skipped
	SnoozedProject bool // project is currently snoozed; notification skipped
	InDND          bool // scheduled DND window suppressed the notification
	// BackendsFired lists names of messaging backends the router
	// successfully dispatched to (info urgency + no rules → empty).
	BackendsFired []string
}

// Raise records a new attention flag. It:
//   1. Computes an aggregation key (project + kind + question-shape hash).
//   2. Looks up any recent open flag with the same agg_key; if found within
//      AggWindow, returns that existing flag (Coalesced=true) and skips
//      creating a duplicate. This is how "3 questions about auth in Project X"
//      becomes one notification.
//   3. Otherwise inserts a new flag row.
//   4. If not rate-limited and not snoozed, dispatches a macOS notification.
func (m *Manager) Raise(ctx context.Context, opts RaiseOpts) (RaiseResult, error) {
	if opts.ProjectID == "" {
		return RaiseResult{}, errors.New("attention: project_id required")
	}
	if opts.Kind == "" {
		opts.Kind = store.FlagKindQuestion
	}
	if opts.Urgency == "" {
		opts.Urgency = store.UrgencyAttention
	}
	now := m.now()
	aggKey := AggregationKey(opts.ProjectID, opts.Kind, opts.Question)

	// Coalescing: look for a matching open flag inside AggWindow. If we
	// find one, don't create a duplicate — just re-notify (subject to
	// rate limit) referencing the count.
	if existing, err := m.Store.FindOpenFlagByAggKey(ctx, opts.ProjectID, aggKey); err == nil {
		if now.Sub(existing.CreatedAt) < m.aggWindow() {
			return m.notify(ctx, existing, true /*coalesced*/, opts, now)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return RaiseResult{}, fmt.Errorf("find open flag: %w", err)
	}

	// Insert a fresh row.
	f := store.Flag{
		ID:        store.NewFlagID(),
		ProjectID: opts.ProjectID,
		Kind:      opts.Kind,
		Urgency:   opts.Urgency,
		Question:  opts.Question,
		AggKey:    aggKey,
		CreatedAt: now,
	}
	if opts.SessionID != "" {
		s := opts.SessionID
		f.SessionID = &s
	}
	if opts.SuggestedID != "" {
		s := opts.SuggestedID
		f.SuggestedID = &s
	}
	if err := m.Store.InsertFlag(ctx, f); err != nil {
		return RaiseResult{}, fmt.Errorf("insert flag: %w", err)
	}
	return m.notify(ctx, f, false, opts, now)
}

// notify handles the macOS side channel with rate-limit + snooze checks.
// Never returns an error for notification failures — flag persistence is
// the source of truth; a swallowed notification just means the user needs
// to open the inbox to see it.
func (m *Manager) notify(ctx context.Context, f store.Flag, coalesced bool, opts RaiseOpts, now time.Time) (RaiseResult, error) {
	res := RaiseResult{Flag: f, Coalesced: coalesced}

	// Snooze check.
	if until, ok := m.snoozedUntil(opts.ProjectID); ok && now.Before(until) {
		res.SnoozedProject = true
		return res, nil
	}
	// DND check: scheduled quiet-hours window. Flag row is already
	// persisted so it shows in the inbox and badge count — we just
	// don't fire the OS notification. `now` here is UTC (via m.now());
	// convert to local time to compare against user's HH:MM window.
	if m.DNDWindowFor != nil {
		if w := m.DNDWindowFor(opts.ProjectID); w.InWindow(now.Local()) {
			res.InDND = true
			return res, nil
		}
	}
	// Rate limit: count flags in this project within the window.
	count, err := m.Store.CountOpenFlagsSince(ctx, opts.ProjectID, now.Add(-m.rateWindow()))
	if err != nil {
		// Non-fatal: notification best-effort.
		count = 0
	}
	if count > m.rateLimit() {
		res.RateLimited = true
		return res, nil
	}

	proj, _ := m.Store.GetProject(ctx, opts.ProjectID)
	title := "Chief"
	if proj.Name != "" {
		title = "Chief · " + proj.Name
	}
	subtitle := notificationSubtitle(f.Kind, coalesced, count)
	body := f.Question
	if body == "" {
		body = "Attention requested"
	}

	// Messaging backends (ntfy/Pushover/Telegram/log) — the router
	// consults per-urgency rules so info flags CAN still reach a phone
	// if the user opts them in, even though macOS notification stays
	// silent for info.
	if m.Router != nil {
		msg := messaging.Message{
			ProjectID:   opts.ProjectID,
			ProjectName: proj.Name,
			Urgency:     messaging.Urgency(f.Urgency),
			Title:       title + " — " + subtitle,
			Body:        body,
			Kind:        "flag." + string(f.Kind),
			Ts:          f.CreatedAt,
		}
		fired := m.Router.Dispatch(ctx, msg)
		for _, r := range fired {
			if r.Error == nil && r.Skipped == "" {
				res.BackendsFired = append(res.BackendsFired, r.Backend)
			}
		}
	}

	// Urgency=info: no macOS notification, badge only. Router (above)
	// already ran because info flags may still be routed to phone push
	// if the user opts them in.
	if f.Urgency == store.UrgencyInfo {
		return res, nil
	}

	if m.Notifier == nil {
		return res, nil
	}
	sound := ""
	if f.Urgency == store.UrgencyUrgent {
		sound = "Sosumi"
	}
	_ = m.Notifier.Send(ctx, notify.Notification{
		Title: title, Subtitle: subtitle, Body: body, Sound: sound,
	})
	res.NotifiedMacOS = true
	return res, nil
}

func notificationSubtitle(kind store.FlagKind, coalesced bool, count int) string {
	switch kind {
	case store.FlagKindNextTask:
		if coalesced {
			return "Next-task suggestion (updated)"
		}
		return "Next-task suggestion"
	default:
		if coalesced && count > 1 {
			return fmt.Sprintf("%d attention flags", count)
		}
		return "Attention needed"
	}
}

// Answer marks a flag resolved with the reply text. Resolution = "answered".
func (m *Manager) Answer(ctx context.Context, flagID, reply string) (store.Flag, error) {
	if err := m.Store.AckFlag(ctx, flagID, reply, store.ResolutionAnswered); err != nil {
		return store.Flag{}, err
	}
	return m.Store.GetFlag(ctx, flagID)
}

// Ack marks a flag resolved with an explicit resolution code (used by
// approve/skip/snooze on next-task flags).
func (m *Manager) Ack(ctx context.Context, flagID, reply string, resolution store.FlagResolution) (store.Flag, error) {
	if err := m.Store.AckFlag(ctx, flagID, reply, resolution); err != nil {
		return store.Flag{}, err
	}
	return m.Store.GetFlag(ctx, flagID)
}

// Snooze suppresses macOS notifications for a project until `until`.
// In-memory only — resets on chiefd restart.
func (m *Manager) Snooze(projectID string, until time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.snoozeUntil == nil {
		m.snoozeUntil = map[string]time.Time{}
	}
	m.snoozeUntil[projectID] = until
}

// snoozedUntil returns the snooze cursor for a project, if any.
func (m *Manager) snoozedUntil(projectID string) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.snoozeUntil[projectID]
	return t, ok
}

func (m *Manager) now() time.Time {
	if m.Clock != nil {
		return m.Clock()
	}
	return time.Now().UTC()
}

func (m *Manager) rateLimit() int {
	if m.RateLimit <= 0 {
		return DefaultRateLimit
	}
	return m.RateLimit
}

func (m *Manager) rateWindow() time.Duration {
	if m.RateWindow <= 0 {
		return DefaultRateWindow
	}
	return m.RateWindow
}

func (m *Manager) aggWindow() time.Duration {
	if m.AggWindow <= 0 {
		return DefaultAggWindow
	}
	return m.AggWindow
}

// AggregationKey produces a stable key for coalescing near-duplicate flags.
// Same project + kind + first-N-tokens-of-question → same key. Case-folded
// and whitespace-normalized, with all non-alphanumeric characters treated
// as separators so punctuation differences (`sessions?` vs `sessions??`)
// don't split otherwise-identical questions into distinct buckets.
func AggregationKey(projectID string, kind store.FlagKind, question string) string {
	var b strings.Builder
	b.Grow(len(question))
	for _, r := range strings.ToLower(question) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	toks := strings.Fields(b.String())
	if len(toks) > 8 {
		toks = toks[:8]
	}
	h := sha1.Sum([]byte(strings.Join(toks, " ")))
	return fmt.Sprintf("%s|%s|%s", projectID, kind, hex.EncodeToString(h[:6]))
}
