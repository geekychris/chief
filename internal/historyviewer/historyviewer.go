// Package historyviewer probes for geekychris/history_viewer and launches it
// with the project-focused directory filter.
//
// The viewer is available in two shapes:
//   - Homebrew: `brew tap geekychris/history-viewer && brew install history-viewer`
//     (binary lands at /opt/homebrew/bin/history_viewer on Apple Silicon)
//   - Source: cloned + `go build` (binary lands at ~/.local/bin/history_viewer)
//
// Either way Chief invokes the same binary. Web-UI deep-link uses the
// --filter-dir flag (requires geekychris/history_viewer commit a37b05a+),
// with the equivalent `?dir=<path>` URL param.
package historyviewer

import (
	"os"
	"os/exec"
	"path/filepath"
)

// RepoURL is the canonical Git URL used by the source-install fallback.
const RepoURL = "https://github.com/geekychris/history_viewer.git"

// DefaultPort is the loopback port Chief pins for its launches. Chosen to
// avoid the more common ports (8080, 3000, 8787 already used by the
// claude-trace analyzer).
const DefaultPort = 9910

// BinaryPath returns the absolute path to a discovered `history_viewer`
// executable, or "" if none is found. Search order matches typical install
// layouts: user-local first, then Homebrew (both arches), then $PATH.
func BinaryPath() string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "bin", "history_viewer"),
		"/opt/homebrew/bin/history_viewer",
		"/usr/local/bin/history_viewer",
	}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			return p
		}
	}
	if p, err := exec.LookPath("history_viewer"); err == nil {
		return p
	}
	return ""
}

// IsInstalled is true when BinaryPath resolves. Second return is a short
// human-readable invocation string for the UI ("history_viewer" or
// "/opt/homebrew/bin/history_viewer").
func IsInstalled() (bool, string) {
	p := BinaryPath()
	if p == "" {
		return false, ""
	}
	return true, p
}

// HomebrewAvailable returns true when the `brew` command is on PATH — used
// to prefer the brew install path over the slower clone+build.
func HomebrewAvailable() bool {
	_, err := exec.LookPath("brew")
	return err == nil
}
