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
