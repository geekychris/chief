package installer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// LogSearchRepoURL is the upstream repo cloned when installing.
const LogSearchRepoURL = "https://github.com/geekychris/local_log_search.git"

// InstallLogSearch clones local_log_search into the chief cache dir,
// then runs its self-installer (`scripts/install.sh`) which builds the
// service + desktop JARs, invokes jpackage to produce a self-contained
// "Little Log Peep.app" (with bundled JRE), and copies it into
// ~/Applications. Same shape as InstallCodeGraphSearch — a fresh cache
// gets built from scratch; a stale cache does `git pull` then rebuilds.
//
// Prereqs (fail-fast so users don't wait through a doomed build):
//   - git
//   - java + jpackage (JDK 21+ — mac users can `brew install --cask temurin@21`)
//   - mvn (a JDK-bundled mvn isn't shipped everywhere, so a system mvn is
//     required unless the repo has a mvnw wrapper; upstream doesn't yet)
//
// This build is Maven-based end-to-end (no npm needed — the UI is
// server-rendered from static resources in log-search-service).
func InstallLogSearch(ctx context.Context) InstallResult {
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

	for _, req := range []struct {
		bin, hint string
	}{
		{"git", "install via `brew install git`"},
		{"java", "install via `brew install --cask temurin@21` (needs JDK 21+ with jpackage)"},
		{"jpackage", "install a JDK 21+ (any Temurin/OpenJDK 21+ ships jpackage)"},
		{"mvn", "install via `brew install maven`"},
	} {
		if _, err := exec.LookPath(req.bin); err != nil {
			return finish(fmt.Errorf("%s not on PATH — %s", req.bin, req.hint))
		}
	}

	cacheDir, err := CacheDir()
	if err != nil {
		return finish(fmt.Errorf("cache dir: %w", err))
	}
	repoDir := filepath.Join(cacheDir, "local_log_search")

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
		res.Steps = append(res.Steps, "git pull (local_log_search)")
		log("$ git -C %s pull --ff-only", repoDir)
		if out, err := runCmd(ctx, "", "git", "-C", repoDir, "pull", "--ff-only"); err != nil {
			log("%s", out)
			return finish(fmt.Errorf("git pull failed: %w", err))
		} else {
			log("%s", out)
		}
	} else {
		res.Steps = append(res.Steps, "git clone (local_log_search)")
		log("$ git clone %s %s", LogSearchRepoURL, repoDir)
		if out, err := runCmd(ctx, "", "git", "clone", "--depth", "1", LogSearchRepoURL, repoDir); err != nil {
			log("%s", out)
			return finish(fmt.Errorf("git clone failed: %w", err))
		} else {
			log("%s", out)
		}
	}

	// scripts/install.sh does: mvn package (desktop + service) → make app → make install.
	// Slow on a cold cache (Spring Boot pulls hundreds of deps + jpackage
	// bundles a JRE — ~3-6 minutes total). chiefd passes 15min for this handler.
	res.Steps = append(res.Steps, "scripts/install.sh (mvn + jpackage)")
	log("$ scripts/install.sh")
	if out, err := runCmd(ctx, repoDir, "bash", "scripts/install.sh"); err != nil {
		log("%s", out)
		return finish(fmt.Errorf("install.sh failed: %w", err))
	} else {
		// jpackage logs are large; keep the last ~4KB.
		if len(out) > 4096 {
			out = "…" + out[len(out)-4096:]
		}
		log("%s", out)
	}

	home, _ := os.UserHomeDir()
	appPath := filepath.Join(home, "Applications", "Little Log Peep.app")
	if _, err := os.Stat(appPath); err != nil {
		return finish(fmt.Errorf("install finished but .app missing at %s: %w", appPath, err))
	}
	res.CLIPath = appPath
	log("✔ installed: %s", appPath)
	return finish(nil)
}
