// Package installer builds/installs external tools Chief integrates with.
//
// Today: claude-session-analyzer (aka claude-trace, CLI `ct`). We can't rely
// on `go install github.com/geekychris/claude-session-analyzer/cmd/ct@latest`
// because the upstream go.mod declares `module claude-trace`, so `go install`
// against the GitHub URL fails with "module declares its path as: claude-trace".
//
// Workaround: clone the repo to a Chief-owned cache dir, `git pull` on re-
// runs, `go build -o ~/.local/bin/ct ./cmd/ct`. If Wails is installed, also
// attempt `wails build` to produce the .app and copy it into ~/Applications.
package installer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// AnalyzerRepoURL is the git URL Chief clones. Keep in one place.
const AnalyzerRepoURL = "https://github.com/geekychris/claude-session-analyzer.git"

// AnalyzerCLI is the resulting CLI binary name.
const AnalyzerCLI = "ct"

// InstallResult is returned by InstallAnalyzer. Log contains the concatenated
// command output (git + go build + optionally wails) so the UI can display it.
type InstallResult struct {
	OK         bool     `json:"ok"`
	CLIPath    string   `json:"cli_path,omitempty"`      // absolute path to the installed binary
	AppPath    string   `json:"app_path,omitempty"`      // .app bundle path if built
	Log        string   `json:"log"`
	Steps      []string `json:"steps"`                    // one entry per command run
	DurationMS int64    `json:"duration_ms"`
	Error      string   `json:"error,omitempty"`
}

// CacheDir returns ~/Library/Caches/Chief/, creating it if missing.
func CacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Library", "Caches", "Chief")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// UserBinDir returns ~/.local/bin (already on chris's PATH), creating it if missing.
func UserBinDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// UserAppsDir returns ~/Applications (does not create it — that's a user-level
// decision).
func UserAppsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Applications")
}

// InstallAnalyzer clones (or updates) the analyzer repo in the Chief cache
// dir, builds the ct CLI, and installs it to ~/.local/bin/ct. If `wails`
// is on PATH, it also attempts to build the .app bundle and copy it to
// ~/Applications.
//
// Returns a non-nil error only on hard failures (git or go-build); missing
// wails is treated as a soft-warning that appears in the log.
func InstallAnalyzer(ctx context.Context) InstallResult {
	start := time.Now()
	res := InstallResult{}
	logBuf := &bytes.Buffer{}
	log := func(format string, args ...any) {
		fmt.Fprintf(logBuf, format+"\n", args...)
	}
	finish := func(err error) InstallResult {
		res.DurationMS = time.Since(start).Milliseconds()
		res.Log = logBuf.String()
		if err != nil {
			res.Error = err.Error()
			res.OK = false
		} else {
			res.OK = true
		}
		return res
	}

	if _, err := exec.LookPath("git"); err != nil {
		return finish(fmt.Errorf("git not found on PATH — install it (brew install git) and retry"))
	}
	if _, err := exec.LookPath("go"); err != nil {
		return finish(fmt.Errorf("go not found on PATH — install it (brew install go) and retry"))
	}

	cacheDir, err := CacheDir()
	if err != nil {
		return finish(fmt.Errorf("cache dir: %w", err))
	}
	repoDir := filepath.Join(cacheDir, "claude-session-analyzer")

	// Step 1: clone or pull.
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
		res.Steps = append(res.Steps, "git pull")
		log("$ git -C %s pull --ff-only", repoDir)
		if out, err := runCmd(ctx, "", "git", "-C", repoDir, "pull", "--ff-only"); err != nil {
			log("%s", out)
			return finish(fmt.Errorf("git pull failed: %w", err))
		} else {
			log("%s", out)
		}
	} else {
		res.Steps = append(res.Steps, "git clone")
		log("$ git clone %s %s", AnalyzerRepoURL, repoDir)
		if out, err := runCmd(ctx, "", "git", "clone", "--depth", "1", AnalyzerRepoURL, repoDir); err != nil {
			log("%s", out)
			return finish(fmt.Errorf("git clone failed: %w", err))
		} else {
			log("%s", out)
		}
	}

	// Step 2: build the CLI.
	binDir, err := UserBinDir()
	if err != nil {
		return finish(fmt.Errorf("~/.local/bin: %w", err))
	}
	cliOut := filepath.Join(binDir, AnalyzerCLI)
	res.Steps = append(res.Steps, "go build ct")
	log("$ go build -o %s ./cmd/ct  (in %s)", cliOut, repoDir)
	if out, err := runCmd(ctx, repoDir, "go", "build", "-o", cliOut, "./cmd/ct"); err != nil {
		log("%s", out)
		return finish(fmt.Errorf("go build ct failed: %w", err))
	} else {
		log("%s", out)
	}
	res.CLIPath = cliOut
	log("✔ installed %s", cliOut)

	// Step 3 (soft): wails build + copy .app. If wails is missing, skip with a warning.
	if wailsBin, err := exec.LookPath("wails"); err == nil {
		res.Steps = append(res.Steps, "wails build")
		log("$ %s build  (in %s)", wailsBin, repoDir)
		if out, err := runCmd(ctx, repoDir, wailsBin, "build"); err != nil {
			// Non-fatal: log and move on. The CLI is already installed.
			log("%s", out)
			log("⚠ wails build failed — CLI installed but .app was NOT built")
		} else {
			log("%s", out)
			built := filepath.Join(repoDir, "build", "bin", "claude-trace.app")
			if _, err := os.Stat(built); err == nil {
				appsDir := UserAppsDir()
				if err := os.MkdirAll(appsDir, 0o755); err != nil {
					log("⚠ mkdir %s failed: %v", appsDir, err)
				} else {
					destApp := filepath.Join(appsDir, "claude-trace.app")
					_ = os.RemoveAll(destApp)
					if out, err := runCmd(ctx, "", "cp", "-R", built, destApp); err != nil {
						log("%s", out)
						log("⚠ copy .app failed: %v", err)
					} else {
						res.AppPath = destApp
						log("✔ installed %s", destApp)
					}
				}
			} else {
				log("⚠ wails build succeeded but %s not found; skipped .app install", built)
			}
		}
	} else {
		log("(wails not on PATH; skipping .app build — install wails to also get the native UI: `go install github.com/wailsapp/wails/v2/cmd/wails@latest`)")
	}

	return finish(nil)
}

// runCmd runs a command in `cwd` (empty = current dir), captures combined
// stdout+stderr, and returns them. Times out via ctx.
func runCmd(ctx context.Context, cwd string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimRight(buf.String(), "\n"), err
}

// Discard is exposed so callers that don't want a log can pass io.Discard.
var Discard io.Writer = io.Discard
