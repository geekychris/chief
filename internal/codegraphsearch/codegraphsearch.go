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
	if _, err := os.Stat(path); err == nil {
		return path, nil // preserve user edits
	}
	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, "Library", "Caches", "Chief",
		"code_graph_search-data", filepath.Base(projectPath))

	cfg := map[string]any{
		"repos": []map[string]any{
			{
				"id":        filepath.Base(projectPath),
				"name":      filepath.Base(projectPath),
				"path":      projectPath,
				"languages": []string{"java", "go", "rust", "typescript", "javascript", "c", "cpp", "python"},
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

// Spawn starts `java --enable-preview -jar <jar> --config <config>`
// as a detached process. Returns the started cmd (Wait not called —
// the process runs until the user quits it via `pkill` or shutdown).
func Spawn(jarPath, configPath string) (*exec.Cmd, error) {
	if _, err := exec.LookPath("java"); err != nil {
		return nil, fmt.Errorf("java not on PATH — install openjdk 21+ (`brew install openjdk@21`)")
	}
	cmd := exec.Command("java", "--enable-preview", "-jar", jarPath, "--config", configPath)
	// Detach: give the child its own stdout/stderr (dropped) + own
	// pgid so a chiefd restart doesn't kill it.
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}
