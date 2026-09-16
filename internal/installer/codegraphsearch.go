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

// CodeGraphSearchRepoURL is the upstream repo cloned when installing.
const CodeGraphSearchRepoURL = "https://github.com/geekychris/code_graph_search.git"

// CodeGraphSearchJarRel is the JAR path inside the repo after build.
const CodeGraphSearchJarRel = "app/target/code-graph-search.jar"

// InstallCodeGraphSearch clones the repo into the chief cache dir + runs
// ./build.sh (which shells to `mvn package -DskipTests`). Unlike the
// history_viewer and analyzer installers, there's no Homebrew formula —
// this is a Java+Maven project so source build is the only path.
//
// Prereqs are checked up front so the user gets a fast fail with the
// right install-hint rather than a garbled mvn error 90s in:
//   - java 21 (with preview features, per the project README)
//   - mvn
//   - git
//   - node/npm (the fat JAR bundles a React frontend)
//
// tree-sitter is optional (only needed for Go/Rust/C/C++/TS parsing)
// and NOT checked here — user gets a runtime warning if missing.
func InstallCodeGraphSearch(ctx context.Context) InstallResult {
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

	// Prereq checks — fast fail with a clear install hint.
	for _, req := range []struct {
		bin, hint string
	}{
		{"git", "install via `brew install git`"},
		{"java", "install via `brew install openjdk@21` (needs Java 21+ with preview)"},
		{"mvn", "install via `brew install maven`"},
		{"npm", "install via `brew install node`"},
	} {
		if _, err := exec.LookPath(req.bin); err != nil {
			return finish(fmt.Errorf("%s not on PATH — %s", req.bin, req.hint))
		}
	}

	cacheDir, err := CacheDir()
	if err != nil {
		return finish(fmt.Errorf("cache dir: %w", err))
	}
	repoDir := filepath.Join(cacheDir, "code_graph_search")

	// Clone or pull.
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
		res.Steps = append(res.Steps, "git pull (code_graph_search)")
		log("$ git -C %s pull --ff-only", repoDir)
		if out, err := runCmd(ctx, "", "git", "-C", repoDir, "pull", "--ff-only"); err != nil {
			log("%s", out)
			return finish(fmt.Errorf("git pull failed: %w", err))
		} else {
			log("%s", out)
		}
	} else {
		res.Steps = append(res.Steps, "git clone (code_graph_search)")
		log("$ git clone %s %s", CodeGraphSearchRepoURL, repoDir)
		if out, err := runCmd(ctx, "", "git", "clone", "--depth", "1", CodeGraphSearchRepoURL, repoDir); err != nil {
			log("%s", out)
			return finish(fmt.Errorf("git clone failed: %w", err))
		} else {
			log("%s", out)
		}
	}

	// Build: `./build.sh` runs `mvn package -DskipTests`. Slow (~2-5min
	// depending on cold cache), so this needs a big context deadline;
	// chiefd passes 10min for this handler.
	res.Steps = append(res.Steps, "./build.sh (mvn package)")
	log("$ ./build.sh")
	if out, err := runCmd(ctx, repoDir, "bash", "./build.sh"); err != nil {
		log("%s", out)
		// Detect the "JDK > 21 rejects source=21 with --enable-preview"
		// build failure that older code_graph_search checkouts hit +
		// give the exact fix. Upstream commit 7b99b05 removes the
		// preview flag entirely, so a `git pull` in the cache dir
		// fixes it too — the retry loop lands that on the next
		// install run.
		if strings.Contains(out, "--enable-preview") &&
			strings.Contains(out, "invalid source release 21") {
			return finish(fmt.Errorf(
				"code_graph_search build rejected because your JDK is newer than 21 " +
					"and the checkout has an older pom that pins source=21 with " +
					"--enable-preview. Fix: `git -C ~/Library/Caches/Chief/code_graph_search " +
					"pull` to pick up upstream commit 7b99b05 (drops the preview flag), " +
					"or install `brew install openjdk@21` and export JAVA_HOME to it " +
					"before rerunning"))
		}
		return finish(fmt.Errorf("build.sh failed: %w", err))
	} else {
		// mvn output is enormous; keep the last ~4KB.
		if len(out) > 4096 {
			out = "…" + out[len(out)-4096:]
		}
		log("%s", out)
	}

	jarPath := filepath.Join(repoDir, CodeGraphSearchJarRel)
	if _, err := os.Stat(jarPath); err != nil {
		return finish(fmt.Errorf("build finished but JAR missing at %s: %w", jarPath, err))
	}

	// Write wrapper scripts to ~/.local/bin so `code-graph-search` +
	// `code-graph-mcp` are callable without knowing the JAR path.
	// Mirrors what upstream scripts/install.sh does — either install
	// path produces the same CLI surface.
	binDir, err := UserBinDir()
	if err != nil {
		log("⚠ could not create ~/.local/bin (%v); JAR is still usable directly", err)
	} else {
		wrappers := []struct {
			name, extraFlag string
		}{
			{"code-graph-search", ""},
			{"code-graph-mcp", "--mcp-stdio"},
		}
		for _, w := range wrappers {
			target := filepath.Join(binDir, w.name)
			body := "#!/usr/bin/env bash\n" +
				"# Auto-generated by chief installer.\n" +
				"exec java --enable-preview -jar \"" + jarPath + "\" " + w.extraFlag + " \"$@\"\n"
			if err := os.WriteFile(target, []byte(body), 0o755); err != nil {
				log("⚠ could not write %s: %v", target, err)
			} else {
				log("wrote %s", target)
			}
		}
	}

	res.CLIPath = jarPath
	log("✔ built JAR: %s", jarPath)
	return finish(nil)
}
