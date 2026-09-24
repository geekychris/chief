// Package logsearch probes for geekychris/local_log_search and
// launches its self-contained "Little Log Peep.app" bundle.
//
// Unlike codegraph search, log-search is a singleton — users register
// log sources once via the app UI and the app maintains its own state
// under ~/Library/Application Support/LittleLogPeep/. There's no
// per-project config file to generate; chief just opens the .app.
//
// Discovery: the installer clones the repo into
// ~/Library/Caches/Chief/local_log_search and runs
// `desktop/make app install`, producing "Little Log Peep.app"
// under ~/Applications/.
package logsearch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RepoURL is the canonical git URL — kept here alongside the probe so
// callers can quote it in "click Install" hints.
const RepoURL = "https://github.com/geekychris/local_log_search"

// AppBundlePath returns the absolute path to the installed
// "Little Log Peep.app" bundle, or "" if not found. Checks
// ~/Applications first (default install target), then /Applications.
func AppBundlePath() string {
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, "Applications", "Little Log Peep.app"),
		"/Applications/Little Log Peep.app",
	} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return ""
}

// AppBundleInnerBinary returns the path to the Mach-O executable
// inside the .app bundle, or "" if not found. Chief invokes this
// directly (rather than `open -a`) so any future --port / --data-dir
// flags forward reliably — `open --args` doesn't consistently forward
// flags through LaunchServices to jpackage-produced launchers.
func AppBundleInnerBinary() string {
	app := AppBundlePath()
	if app == "" {
		return ""
	}
	candidate := filepath.Join(app, "Contents", "MacOS", "Little Log Peep")
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
		return candidate
	}
	entries, err := os.ReadDir(filepath.Join(app, "Contents", "MacOS"))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(app, "Contents", "MacOS", e.Name())
		if info, err := os.Stat(p); err == nil && info.Mode().Perm()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// IsInstalled reports whether the .app bundle is present.
func IsInstalled() bool { return AppBundlePath() != "" }

// IsRunning reports whether a Little Log Peep process is currently
// alive. Uses pgrep since jpackage bundles don't leave a PID file.
// The .app's Mach-O launcher shows up as "Little Log Peep" in ps.
func IsRunning() bool {
	out, err := exec.Command("pgrep", "-f", "Little Log Peep.app/Contents/MacOS/Little Log Peep").Output()
	if err != nil {
		return false
	}
	return len(out) > 0
}

// FocusRunning brings the running app forward via `open -a`.
// LaunchServices reactivates the existing instance instead of
// spawning a duplicate.
func FocusRunning() error {
	return exec.Command("open", "-a", "Little Log Peep").Start()
}

// Launch starts a fresh .app instance. Callers should check
// IsRunning() + FocusRunning() first for singleton semantics.
func Launch() error {
	bin := AppBundleInnerBinary()
	if bin == "" {
		return fmt.Errorf("Little Log Peep.app not installed — see %s", RepoURL)
	}
	cmd := exec.Command(bin)
	cmd.Env = os.Environ()
	return cmd.Start()
}

// PortFile is where the desktop wrapper publishes its resolved
// loopback port after boot. Kept in the same app-support dir the
// service uses for state so it moves with the install.
func PortFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "LittleLogPeep", "port")
}

// RunningPort reads the port file and returns the number, or 0 when
// the file isn't there / can't be parsed. A 0 return means the
// service isn't up (or is running a pre-port-file version — callers
// should handle both by treating it as "unavailable").
func RunningPort() int {
	b, err := os.ReadFile(PortFile())
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return n
}

// WaitForPort polls the port file until it appears (or timeout).
// Used right after Launch() so callers don't race the service's
// startup when they immediately want to hit the REST API.
func WaitForPort(timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p := RunningPort(); p > 0 {
			return p
		}
		time.Sleep(200 * time.Millisecond)
	}
	return 0
}

// BaseURL returns the http://127.0.0.1:<port> base for the running
// service, or empty when nothing's up.
func BaseURL() string {
	if p := RunningPort(); p > 0 {
		return fmt.Sprintf("http://127.0.0.1:%d", p)
	}
	return ""
}

// LogSource mirrors LogSourceConfig on the wire. Only the fields chief
// cares about are declared; extras from the server round-trip through
// json.RawMessage-agnostic decoding.
type LogSource struct {
	ID           string            `json:"id"`
	FilePath     string            `json:"filePath"`
	IndexName    string            `json:"indexName"`
	ParserType   string            `json:"parserType"`
	ParserConfig map[string]string `json:"parserConfig,omitempty"`
	Enabled      bool              `json:"enabled"`
}

// UpsertSource idempotently registers (or updates) a log source in the
// running Little Log Peep. If a source with the same id already
// exists, it's updated in place (PUT); otherwise created (POST).
// Returns an error if the service isn't running or the REST call fails.
func UpsertSource(src LogSource) error {
	base := BaseURL()
	if base == "" {
		return fmt.Errorf("Little Log Peep is not running (no port file at %s) — start it with `chief logsearch open`", PortFile())
	}
	if src.ID == "" {
		return fmt.Errorf("UpsertSource: id required")
	}
	client := &http.Client{Timeout: 5 * time.Second}

	// Try GET first — if 200, the source exists → PUT to update.
	getResp, err := client.Get(base + "/api/sources/" + src.ID)
	if err != nil {
		return fmt.Errorf("GET %s: %w", base, err)
	}
	_, _ = io.Copy(io.Discard, getResp.Body)
	getResp.Body.Close()

	body, err := json.Marshal(src)
	if err != nil {
		return err
	}
	var req *http.Request
	if getResp.StatusCode == http.StatusOK {
		req, err = http.NewRequest(http.MethodPut, base+"/api/sources/"+src.ID, bytes.NewReader(body))
	} else {
		req, err = http.NewRequest(http.MethodPost, base+"/api/sources", bytes.NewReader(body))
	}
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("register source failed (%d): %s", resp.StatusCode, string(b))
	}
	return nil
}

// ChiefLogPath returns the canonical location of chiefd's structured
// JSON log — where setupLogger() in cmd/chiefd writes.
func ChiefLogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "Chief", "chiefd.log")
}

// RegisterChiefLog is a convenience: register chiefd.log with a stable
// id + index name so calling it repeatedly is a no-op. Best-effort —
// callers should log-and-continue if it errors (e.g. the log-search
// app isn't installed / running).
func RegisterChiefLog() error {
	return UpsertSource(LogSource{
		ID:         "chief-daemon",
		FilePath:   ChiefLogPath(),
		IndexName:  "chief",
		ParserType: "json",
		Enabled:    true,
	})
}
