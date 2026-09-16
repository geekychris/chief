// Package config loads Chief's persistent config from
// ~/Library/Application Support/Chief/config.yaml. Small on purpose — only
// cross-cutting settings that many components need (e.g., the cmux socket
// password) live here. Per-project settings belong in .chief/project.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/geekychris/chief/internal/ipc"
	"gopkg.in/yaml.v3"
)

// Filename inside DataDir.
const Filename = "config.yaml"

// Config is the persistent, cross-cutting configuration for Chief.
type Config struct {
	// Cmux authentication + optional overrides for cmux socket location.
	Cmux CmuxConfig `yaml:"cmux"`
	// Events retention + archival.
	Events EventsConfig `yaml:"events,omitempty"`
	// DND (do-not-disturb) — suppresses macOS notifications during
	// configured windows. Flags still land in the inbox + menu-bar
	// count; only the noisy channel is muted.
	DND DNDConfig `yaml:"dnd,omitempty"`
	// Messaging backends + routing rules. Backends define plugins
	// (ntfy, Pushover, Telegram, Slack, local-log); routing decides
	// which fire for which urgency.
	Messaging MessagingConfig `yaml:"messaging,omitempty"`
}

// MessagingConfig is the plugin + routing configuration for outbound
// side channels. See internal/messaging.
type MessagingConfig struct {
	// Backends is the list of side-channel configurations. Each entry
	// becomes one internal/messaging.Backend at chiefd boot. `type`
	// selects the implementation; `name` is the label routing rules
	// reference. Extra fields are backend-specific (e.g., topic for
	// ntfy, path for log, token for pushover) — kept as a raw map to
	// avoid one struct per backend variant.
	Backends []BackendConfig `yaml:"backends,omitempty"`
	// Routing: default backends per urgency, at the global level.
	// Per-project overrides live in .chief/project.yaml under
	// `messaging:`.
	Routing RoutingConfig `yaml:"routing,omitempty"`
	// Digest: scheduled rollup notification.
	Digest DigestConfig `yaml:"digest,omitempty"`
	// Briefing: opinionated morning message with "top N tasks to focus
	// on today". More prescriptive than Digest — see MorningBriefer.
	Briefing BriefingConfig `yaml:"briefing,omitempty"`
}

// BriefingConfig configures the morning briefing (12da). Disabled by
// default; set enabled=true + pick a schedule.
type BriefingConfig struct {
	Enabled bool `yaml:"enabled,omitempty"`
	// Time to fire in local time, HH:MM. Default: "08:30".
	At string `yaml:"at,omitempty"`
	// Backends: which messaging backends to fan out to. Empty →
	// falls back to routing rules for urgency=attention.
	Backends []string `yaml:"backends,omitempty"`
	// TopN: how many tasks to highlight. Default 3.
	TopN int `yaml:"top_n,omitempty"`
	// WindowHours: activity window for "recent completions". Default 24.
	WindowHours int `yaml:"window_hours,omitempty"`
}

// DigestConfig configures the daily/weekly rollup notification.
// Disabled by default; set enabled=true + pick a schedule.
type DigestConfig struct {
	Enabled bool   `yaml:"enabled,omitempty"`
	// Time to fire in local time, HH:MM. Default: "09:00".
	At string `yaml:"at,omitempty"`
	// Cadence: "daily" or "weekly". Default: "daily". Weekly fires
	// only on Monday at `at`.
	Cadence string `yaml:"cadence,omitempty"`
	// Backends: which messaging backends to fan out to. Empty →
	// falls back to the routing rules for urgency=attention.
	Backends []string `yaml:"backends,omitempty"`
	// WindowHours: how far back to look for "recent completions".
	// Default: 24 for daily, 168 for weekly.
	WindowHours int `yaml:"window_hours,omitempty"`
}

// BackendConfig is one entry under messaging.backends.
type BackendConfig struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"` // "log" | "ntfy" | "pushover" | "telegram" | "slack"
	// Type-specific fields flattened into the top level for readability.
	Path     string `yaml:"path,omitempty"`     // log: JSONL file path
	Topic    string `yaml:"topic,omitempty"`    // ntfy: subscription topic
	Server   string `yaml:"server,omitempty"`   // ntfy: base URL (defaults to ntfy.sh); pushover: unused
	Token    string `yaml:"token,omitempty"`    // pushover: app token; telegram: bot token
	User     string `yaml:"user,omitempty"`     // pushover: user key
	ChatID   string `yaml:"chat_id,omitempty"`  // telegram: destination chat
	Webhook  string `yaml:"webhook,omitempty"`  // slack: incoming webhook URL
	Priority string `yaml:"priority,omitempty"` // ntfy/pushover: mapping override (rarely used)
}

// RoutingConfig is the global routing table. Empty PerUrgency means
// every urgency uses Default. Set to nil for a specific urgency
// (yaml: `attention: null`) to opt that urgency out of all backends.
type RoutingConfig struct {
	Default    []string              `yaml:"default,omitempty"`
	PerUrgency map[string][]string   `yaml:"per_urgency,omitempty"`
	Disable    bool                  `yaml:"disable,omitempty"`
}

// DNDConfig is the global DND window. Per-project overrides live in
// .chief/project.yaml under `dnd:`.
//
// Times are "HH:MM" in local time (24h). Windows may wrap midnight
// ("22:00" → "07:00" = 9 hours of quiet). Empty start/end disables.
type DNDConfig struct {
	Start string `yaml:"start,omitempty"` // e.g. "22:00"
	End   string `yaml:"end,omitempty"`   // e.g. "07:00"
	// Weekdays: if set, DND only applies on these days (0=Sunday..6=Saturday).
	// Empty means every day.
	Weekdays []int `yaml:"weekdays,omitempty"`
}

// EventsConfig tunes the events-table retention worker.
type EventsConfig struct {
	// RetentionDays: events older than this get archived to
	// ~/Library/Logs/Chief/archive/YYYY-MM.jsonl.gz then deleted from
	// SQLite. 0 uses the default (90). Set to a negative value to
	// disable archival entirely (rows never expire).
	RetentionDays int `yaml:"retention_days,omitempty"`
	// SweepEvery is how often the retention worker runs. 0 uses 24h.
	// Kept mostly for tests; production defaults are fine.
	SweepEveryHours int `yaml:"sweep_every_hours,omitempty"`
}

// CmuxConfig is the cmux-side of Chief config.
type CmuxConfig struct {
	// SocketPassword is passed as --password on every cmux CLI invocation.
	// Required when cmux's automation.socketControlMode is "password"
	// (recommended for chiefd, which is not spawned inside cmux).
	SocketPassword string `yaml:"socket_password,omitempty"`
	// SocketPath overrides cmux's default socket location. Rarely needed
	// (cmux 501-suffixes by uid on macOS; the CLI auto-discovers).
	SocketPath string `yaml:"socket_path,omitempty"`
}

// Path returns the absolute path to config.yaml. Does not create the file.
func Path() (string, error) {
	dir, err := ipc.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, Filename), nil
}

// Load reads config.yaml. If the file does not exist, returns a zero-value
// Config and no error (Chief runs fine without one). Any other read/parse
// error is surfaced.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return c, nil
}

// Save writes config.yaml atomically with 0600 permissions.
func Save(c Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
