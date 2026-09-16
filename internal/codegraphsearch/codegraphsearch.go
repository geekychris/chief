// Package codegraphsearch probes for geekychris/code_graph_search and
// launches it against a specific project directory.
//
// Discovery: the installer clones the repo into
// ~/Library/Caches/Chief/code_graph_search and builds the fat JAR at
// app/target/code-graph-search.jar. That's the one path we look at.
//
// Per-project launch flow:
//  1. Generate .chief/code-graph.yaml under the project — pins dataDir
//     to a project-specific subdir under ~/Library/Caches/Chief/
//     code_graph_search-data/<slug> so different projects don't share
//     indices.
//  2. Pick a stable port derived from the project path hash so
//     re-launches for the same project reuse the same port (browser
//     tab stays put).
//  3. Spawn `java --enable-preview -jar <jar> --config <yaml>` unless
//     an instance is already listening on the port.
//  4. Return the URL for chiefd to open.
//
// The Wails wrapper (project-side, deferred) will bypass the browser
// entirely; for now we open the default browser.
package codegraphsearch

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// RepoURL is the canonical git URL — kept here alongside the probe so
// callers can quote it in "click Install" hints.
const RepoURL = "https://github.com/geekychris/code_graph_search"

// JarPath returns the absolute path where the installer drops the JAR.
// Returns "" if not built yet.
func JarPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, "Library", "Caches", "Chief",
		"code_graph_search", "app", "target", "code-graph-search.jar")
	if info, err := os.Stat(p); err == nil && !info.IsDir() {
		return p
	}
	return ""
}

// IsInstalled reports whether the JAR exists.
func IsInstalled() bool { return JarPath() != "" }

// AppBundlePath returns the absolute path to the installed
// Code Graph Search.app bundle, or "" if not found. Checks
// ~/Applications first (default install target), then /Applications.
// See upstream commit 6e27e4c for the desktop wrapper — the .app
// bundles a trimmed JRE via jpackage so end users don't need Java
// on PATH.
func AppBundlePath() string {
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, "Applications", "Code Graph Search.app"),
		"/Applications/Code Graph Search.app",
	} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return ""
}

// AppBundleInnerBinary returns the path to the Mach-O executable
// inside the .app bundle, or "" if not found. Chief invokes this
// directly (rather than `open -a`) so --config / --port flags forward
// reliably — `open --args` doesn't consistently forward flags through
// LaunchServices to jpackage-produced launchers.
func AppBundleInnerBinary() string {
	app := AppBundlePath()
	if app == "" {
		return ""
	}
	// jpackage-produced bundles put the launcher at
	// Contents/MacOS/<CFBundleExecutable>. Look for the well-known name
	// first; fall back to picking the first executable in MacOS/.
	candidate := filepath.Join(app, "Contents", "MacOS", "Code Graph Search")
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

// PortForProject returns a stable loopback port derived from the
// project path so relaunching for the same project reuses the same
// port. Range: 30000-39999 (avoids most well-known service ports).
func PortForProject(projectPath string) int {
	h := sha1.Sum([]byte(projectPath))
	// Use the first 4 bytes as an unsigned int, mod 10000, offset to 30000.
	n := binary.BigEndian.Uint32(h[:4])
	return 30000 + int(n%10000)
}

// URL returns the URL for a project's code-graph UI.
func URL(port int) string {
	return fmt.Sprintf("http://localhost:%d", port)
}

// Alive probes whether something is listening on the port. Used by
// the launch flow to skip spawning a duplicate.
func Alive(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// EnsureConfig writes .chief/code-graph.yaml under projectPath if
// missing, pinning dataDir to a project-scoped subdir. Idempotent —
// re-runs don't overwrite user edits (returns nil without touching
// the file if it already exists).
func EnsureConfig(projectPath string, port int) (string, error) {
	dir := filepath.Join(projectPath, ".chief")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "code-graph.yaml")
	if b, err := os.ReadFile(path); err == nil {
		// One-time migration: earlier versions of Chief wrote lowercase
		// language names ("java" instead of "JAVA"). code_graph_search's
		// Jackson-YAML deserializer is case-sensitive on the enum name,
		// so the whole file silently failed to parse and the app
		// defaulted to port 8080 + empty repo list. Regenerate iff the
		// file still has the header line we always emit AND the buggy
		// lowercase pattern — that's a reliable "this is stale Chief
		// output, safe to overwrite" signal.
		if bytes.Contains(b, []byte("# Chief-generated per-project code_graph_search config.")) &&
			bytes.Contains(b, []byte("- java\n")) {
			// fall through to rewrite
		} else {
			return path, nil // preserve user edits
		}
	}
	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, "Library", "Caches", "Chief",
		"code_graph_search-data", filepath.Base(projectPath))

	cfg := map[string]any{
		"repos": []map[string]any{
			{
				"id":   filepath.Base(projectPath),
				"name": filepath.Base(projectPath),
				"path": projectPath,
				// Language names must match code_graph_search's Language
				// enum member names (uppercase). Jackson's default enum
				// deserializer is case-sensitive on the enum NAME, not
				// the `id` field. Note: "python" is not in the enum
				// (unknown language) — skipped rather than causing a
				// silent parse-fail-and-default-config bug.
				"languages": []string{"JAVA", "GO", "RUST", "TYPESCRIPT", "JAVASCRIPT", "C", "CPP"},
				"excludePatterns": []string{
					"**/target/**", "**/build/**", "**/.git/**",
					"**/node_modules/**", "**/dist/**", "**/.next/**",
					"**/vendor/**", "**/__pycache__/**",
				},
			},
		},
		"indexer": map[string]any{
			"dataDir":         dataDir,
			"watchDebounceMs": 500,
			"autoWatch":       true,
			"indexingThreads": 4,
		},
		"server": map[string]any{
			"port":            port,
			"mcpStdioEnabled": false,
			"mcpHttpEnabled":  true,
		},
		"treeSitter": map[string]any{
			"enabled":        true,
			"timeoutSeconds": 30,
		},
	}
	buf, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	// Header the user sees on first open — makes it obvious this is
	// Chief-generated + safe to edit.
	header := "# Chief-generated per-project code_graph_search config.\n" +
		"# Safe to edit. Chief re-uses this file on subsequent launches;\n" +
		"# delete it to regenerate with defaults.\n\n"
	if err := os.WriteFile(path, append([]byte(header), buf...), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Spawn starts `java -jar <jar> --config <config>` as a detached
// process, teeing stdout/stderr into
// ~/Library/Logs/Chief/code-graph-<slug>.log so failures are
// debuggable. Returns the started cmd (Wait not called — the process
// runs until the user quits it via `pkill` or shutdown).
//
// --enable-preview is intentionally NOT passed; upstream commit
// 7b99b05 removed the preview requirement from code_graph_search's
// pom, and passing --enable-preview against a non-preview JAR is
// tolerated but noisy. Older JARs that DO need the flag work too
// because java --enable-preview <jar without preview> is a no-op.
func Spawn(jarPath, configPath string) (*exec.Cmd, error) {
	if _, err := exec.LookPath("java"); err != nil {
		return nil, fmt.Errorf("java not on PATH — install openjdk 21+ (`brew install openjdk@21`)")
	}
	// Log file per project (based on config path hash so multiple
	// projects don't clobber each other's logs).
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, "Library", "Logs", "Chief")
	_ = os.MkdirAll(logDir, 0o755)
	h := sha1.Sum([]byte(configPath))
	logPath := filepath.Join(logDir, fmt.Sprintf("code-graph-%s.log", fmt.Sprintf("%x", h[:4])))
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", logPath, err)
	}
	cmd := exec.Command("java", "-jar", jarPath, "--config", configPath)
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		_ = f.Close()
		return nil, err
	}
	// Don't Close(f) here — the goroutine keeps writing until process exits.
	// OS handles cleanup on process termination; log file grows unbounded
	// until manually rotated, acceptable for now.
	return cmd, nil
}
