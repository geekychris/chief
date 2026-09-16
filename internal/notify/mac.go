// Package notify sends macOS user notifications via osascript.
//
// Deliberately kept minimal: no bundle-identifier tricks, no external
// dependencies. The current interface has no click-to-focus action —
// osascript's `display notification` doesn't support user-defined
// activation handlers without a signed helper app. The menu-bar badge
// is the click surface for now; deep-link on-click is a future task.
package notify

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Notifier is the seam so chiefd can inject a fake in tests and a no-op on
// non-macOS builds. Send returns nil for a swallowed no-op (e.g., osascript
// missing) so notification failures don't cascade into RPC errors.
type Notifier interface {
	Send(ctx context.Context, n Notification) error
}

// Notification is one macOS user-notification payload. Empty Subtitle/Sound
// omit those elements from the AppleScript.
type Notification struct {
	Title    string
	Subtitle string
	Body     string
	Sound    string // e.g. "Ping", "Sosumi"; empty = default
}

// New returns a MacNotifier on darwin, a NoopNotifier elsewhere. Callers can
// still override in tests by constructing MacNotifier{} directly.
func New() Notifier {
	if runtime.GOOS != "darwin" {
		return NoopNotifier{}
	}
	return MacNotifier{Timeout: 3 * time.Second}
}

// MacNotifier shells out to osascript.
type MacNotifier struct {
	// Timeout for the osascript exec. Notifications should be instant;
	// anything slower is likely a hung osascript we should abandon.
	Timeout time.Duration
}

// Send runs `osascript -e 'display notification "..." with title "..."'`.
// Body is required; Title/Subtitle/Sound are optional and omitted from the
// script when empty.
func (m MacNotifier) Send(ctx context.Context, n Notification) error {
	if n.Body == "" {
		return errors.New("notify: Body required")
	}
	// Silently no-op if osascript isn't on PATH — happens in CI, in nix
	// dev shells, and on Linux. Better than hard-failing every flag.raise.
	if _, err := exec.LookPath("osascript"); err != nil {
		return nil
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// AppleScript strings need escaping for `"` and `\`. Both are common
	// in error messages and code snippets, so this matters in practice.
	script := `display notification "` + escapeAS(n.Body) + `"`
	if n.Title != "" {
		script += ` with title "` + escapeAS(n.Title) + `"`
	}
	if n.Subtitle != "" {
		script += ` subtitle "` + escapeAS(n.Subtitle) + `"`
	}
	if n.Sound != "" {
		script += ` sound name "` + escapeAS(n.Sound) + `"`
	}
	return exec.CommandContext(ctx, "osascript", "-e", script).Run()
}

// escapeAS escapes AppleScript double-quoted string content.
func escapeAS(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	// Newlines in AppleScript strings are legal but osascript renders them
	// literally in the notification body — replace with " · " for compactness.
	s = strings.ReplaceAll(s, "\n", " · ")
	return s
}

// NoopNotifier discards all sends. Used on non-darwin and in tests.
type NoopNotifier struct{}

func (NoopNotifier) Send(_ context.Context, _ Notification) error { return nil }

// RecordingNotifier captures all sends in memory. Handy for unit tests.
type RecordingNotifier struct {
	Sent []Notification
}

func (r *RecordingNotifier) Send(_ context.Context, n Notification) error {
	r.Sent = append(r.Sent, n)
	return nil
}
