// Package claudetrace exposes helpers for integrating Chief with
// geekychris/claude-session-analyzer (aka claude-trace, CLI `ct`).
//
// Claude Code writes session JSONL files to
// ~/.claude/projects/<slug>/<session-uuid>.jsonl, where <slug> is the working
// directory path with '/' and '_' both replaced by '-' and a leading '-'
// added. This package computes the slug, lists sessions for a project, and
// probes whether the analyzer is installed.
package claudetrace

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SlugForPath returns the ~/.claude/projects/<slug> directory name for a
// given working directory. Both '/' and '_' are replaced with '-', so
//
//	/Users/chris/code/claude_world/chief
//
// becomes
//
//	-Users-chris-code-claude-world-chief
//
// The encoding is lossy — 'foo/bar' and 'foo_bar' produce the same slug —
// so callers that need certainty should list ~/.claude/projects/ and match
// on existence.
func SlugForPath(path string) string {
	cleaned := filepath.Clean(path)
	replaced := strings.NewReplacer("/", "-", "_", "-").Replace(cleaned)
	// filepath.Clean("/foo") = "/foo" → "-foo" already has the leading dash.
	// Bare "foo" would become "foo" — force the leading dash to match
	// Claude Code's convention.
	if !strings.HasPrefix(replaced, "-") {
		replaced = "-" + replaced
	}
	return replaced
}

// SessionsDir returns ~/.claude/projects/<slug> for the given cwd. Does not
// verify the directory exists.
func SessionsDir(cwd string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects", SlugForPath(cwd)), nil
}

// Session is one Claude Code session file.
type Session struct {
	ID       string    `json:"id"`        // filename without .jsonl (the session UUID)
	Path     string    `json:"path"`      // absolute path to the .jsonl
	Size     int64     `json:"size"`      // bytes
	Modified time.Time `json:"modified"`  // file mtime
}

// ListSessions returns all *.jsonl session files under
// ~/.claude/projects/<slug-of-cwd>. Returned sorted newest-first. If the
// directory does not exist yet, returns an empty slice (not an error).
func ListSessions(cwd string) ([]Session, error) {
	dir, err := SessionsDir(cwd)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Session{}, nil
		}
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Session{
			ID:       strings.TrimSuffix(e.Name(), ".jsonl"),
			Path:     filepath.Join(dir, e.Name()),
			Size:     info.Size(),
			Modified: info.ModTime(),
		})
	}
	// Sort newest first.
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// AnalyzerInstalled probes whether claude-trace is available in any of the
// common shapes: `ct` binary on PATH, or claude-trace.app in /Applications
// or ~/Applications. Returns the best invocation string (e.g., "ct" or
// "open -a claude-trace") or empty if not found.
func AnalyzerInstalled() (bool, string) {
	if p, err := lookPath("ct"); err == nil && p != "" {
		return true, "ct"
	}
	if p := AnalyzerAppPath(); p != "" {
		return true, "open -a " + filepath.Base(p)
	}
	return false, ""
}

// AnalyzerAppPath returns the absolute path to the installed claude-trace.app
// bundle, or empty string if not found. Checks user-local first, then system.
func AnalyzerAppPath() string {
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, "Applications", "claude-trace.app"),
		"/Applications/claude-trace.app",
		filepath.Join(home, "Applications", "Claude Trace.app"),
		"/Applications/Claude Trace.app",
	} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return ""
}

// AnalyzerAppBinary returns the path to the inner Mach-O executable inside
// claude-trace.app (Contents/MacOS/claude_trace), or empty if not found.
// This is what Chief invokes directly to reliably forward the --project flag —
// `open -n -a claude-trace --args --project X` doesn't consistently forward
// flags through LaunchServices, so we bypass it.
func AnalyzerAppBinary() string {
	app := AnalyzerAppPath()
	if app == "" {
		return ""
	}
	// Wails 2 uses the app name (with '_') as the Mach-O executable name.
	// Try the well-known name first, then fall back to whatever's in MacOS/.
	candidate := filepath.Join(app, "Contents", "MacOS", "claude_trace")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	// Fallback: pick the first executable in Contents/MacOS/.
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

// lookPath is exec.LookPath as a package-local func so the file has no other
// exec dependency (helps keep the graph tight during tests).
func lookPath(bin string) (string, error) {
	// Import inside function to avoid an unused-import blip if only tests
	// call this. In practice we call it at runtime — keeping the import
	// inline via a wrapper isn't possible in Go, so just do it here.
	return execLookPath(bin)
}
