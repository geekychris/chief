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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
