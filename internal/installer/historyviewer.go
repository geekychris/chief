package installer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// HistoryViewerRepoURL is the git URL used by the source-install fallback.
const HistoryViewerRepoURL = "https://github.com/geekychris/history_viewer.git"

// HistoryViewerBrewTap is the Homebrew tap; installed with `brew tap` then
// `brew install history-viewer`.
const HistoryViewerBrewTap = "geekychris/history-viewer"
const HistoryViewerBrewFormula = "history-viewer"

// InstallHistoryViewer installs geekychris/history_viewer using the fastest
// available method:
//
//   1. Homebrew (if `brew` on PATH) — tap + install, pulls a prebuilt binary.
//   2. Source clone + `go build` — falls back to ~/Library/Caches/Chief/
//      cache dir + writes the binary to ~/.local/bin.
//
// Both paths are re-runnable to update.
func InstallHistoryViewer(ctx context.Context) InstallResult {
	start := time.Now()
	res := InstallResult{}
	logBuf := &bytes.Buffer{}
	log := func(format string, args ...any) { fmt.Fprintf(logBuf, format+"\n", args...) }
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

	// Path 1: Homebrew (preferred — prebuilt binary, ~5-15s).
	if _, err := exec.LookPath("brew"); err == nil {
		res.Steps = append(res.Steps, "brew tap")
		log("$ brew tap %s", HistoryViewerBrewTap)
		if out, err := runCmd(ctx, "", "brew", "tap", HistoryViewerBrewTap); err != nil {
			// tap failure is non-fatal if it was already tapped; log and continue.
			log("%s", out)
			log("⚠ brew tap returned error; will try `brew install` anyway (may still succeed if already tapped)")
		} else {
			log("%s", out)
		}
		res.Steps = append(res.Steps, "brew install")
		log("$ brew install %s", HistoryViewerBrewFormula)
		if out, err := runCmd(ctx, "", "brew", "install", HistoryViewerBrewFormula); err != nil {
			// If it was already installed, brew errors with "already installed" — try upgrade.
			combined := out + " " + err.Error()
			if strings.Contains(combined, "already installed") || strings.Contains(combined, "up-to-date") {
				log("%s", out)
				log("(already installed; attempting `brew upgrade`)")
				if upgOut, upgErr := runCmd(ctx, "", "brew", "upgrade", HistoryViewerBrewFormula); upgErr == nil {
					log("%s", upgOut)
				} else {
					log("%s", upgOut)
					log("(brew upgrade returned an error; existing install is still usable)")
				}
			} else {
				log("%s", out)
				log("⚠ brew install failed; falling back to source build")
				return installHistoryViewerFromSource(ctx, log, finish, res)
			}
		} else {
			log("%s", out)
		}
		if bin, err := exec.LookPath("history_viewer"); err == nil {
			res.CLIPath = bin
		} else {
			res.CLIPath = "/opt/homebrew/bin/history_viewer" // Apple Silicon default
		}
		log("✔ installed via Homebrew: %s", res.CLIPath)
		return finish(nil)
	}

	// Path 2: source build.
	return installHistoryViewerFromSource(ctx, log, finish, res)
}

func installHistoryViewerFromSource(
	ctx context.Context,
	log func(string, ...any),
	finish func(error) InstallResult,
	res InstallResult,
) InstallResult {
	if _, err := exec.LookPath("git"); err != nil {
		return finish(fmt.Errorf("git not on PATH — install git (brew install git) to fall back to source build"))
	}
	if _, err := exec.LookPath("go"); err != nil {
		return finish(fmt.Errorf("go not on PATH — install go (brew install go) to fall back to source build"))
	}
	cacheDir, err := CacheDir()
	if err != nil {
		return finish(fmt.Errorf("cache dir: %w", err))
	}
	repoDir := filepath.Join(cacheDir, "history_viewer")

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
		res.Steps = append(res.Steps, "git pull (history_viewer)")
		log("$ git -C %s pull --ff-only", repoDir)
		if out, err := runCmd(ctx, "", "git", "-C", repoDir, "pull", "--ff-only"); err != nil {
			log("%s", out)
			return finish(fmt.Errorf("git pull failed: %w", err))
		} else {
			log("%s", out)
		}
	} else {
		res.Steps = append(res.Steps, "git clone (history_viewer)")
		log("$ git clone %s %s", HistoryViewerRepoURL, repoDir)
		if out, err := runCmd(ctx, "", "git", "clone", "--depth", "1", HistoryViewerRepoURL, repoDir); err != nil {
			log("%s", out)
			return finish(fmt.Errorf("git clone failed: %w", err))
		} else {
			log("%s", out)
		}
	}

	binDir, err := UserBinDir()
	if err != nil {
		return finish(fmt.Errorf("~/.local/bin: %w", err))
	}
	cliOut := filepath.Join(binDir, "history_viewer")
	res.Steps = append(res.Steps, "go build (history_viewer)")
	log("$ go build -o %s .  (in %s)", cliOut, repoDir)
	// history_viewer is a single-package repo at root — build "." not "./cmd/..."
	if out, err := runCmd(ctx, repoDir, "go", "build", "-o", cliOut, "."); err != nil {
		log("%s", out)
		return finish(fmt.Errorf("go build history_viewer failed: %w", err))
	} else {
		log("%s", out)
	}
	res.CLIPath = cliOut
	log("✔ installed from source: %s", cliOut)
	return finish(nil)
}
