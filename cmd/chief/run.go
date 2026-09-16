package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/geekychris/chief/internal/methods"
	"github.com/spf13/cobra"
)

// runCmd groups one-shot orchestration verbs that walk every registered
// project and do something with each: check git state, run tests, look
// for stale deps, snapshot to a tarball, etc.
//
// Design choice: these live on the CLI, not chiefd. They shell out to
// git / go / npm / cargo / gh — chiefd stays a thin coordinator and
// heavyweight scanning happens on the user's terminal where progress
// is visible.
func runCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "One-shot verbs that walk every registered project.",
	}
	cmd.AddCommand(
		runEnsureCleanCmd(),
		runTestsCmd(),
		runPRStatusCmd(),
		runDepsFreshCmd(),
		runReviewDiffCmd(),
		runBackupCmd(),
		runRestoreCmd(),
		runRefreshCmd(),
		runFocusCmd(),
	)
	return cmd
}

// listProjectsForRun is a small helper that pulls every registered
// project via chiefd. All run subcommands need this.
func listProjectsForRun() ([]methods.ProjectSummary, error) {
	c, err := dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var resp methods.ProjectListResponse
	if err := c.Call("project.list", methods.ProjectListRequest{}, &resp); err != nil {
		return nil, err
	}
	return resp.Projects, nil
}

// ---------- 850d: run ensure-clean ----------

func runEnsureCleanCmd() *cobra.Command {
	var autoCommit bool
	var autoMessage string
	cmd := &cobra.Command{
		Use:   "ensure-clean",
		Short: "Walk every project; report uncommitted changes / unpushed commits.",
		Long: `For each registered project:
  - list chief-managed files with uncommitted changes
  - count commits ahead of origin
  - warn on projects that lack a remote

With --autocommit, chief calls sync.push per dirty project (uses the
same guarded machinery as ` + "`chief sync push`" + `; only touches the
chief-managed markdown files, never rando source edits).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var listResp methods.ProjectListResponse
			if err := c.Call("project.list", methods.ProjectListRequest{}, &listResp); err != nil {
				return err
			}
			anyDirty := false
			for _, p := range listResp.Projects {
				var st methods.SyncStatusResponse
				if err := c.Call("sync.status", methods.SyncStatusRequest{IDOrPath: p.ID}, &st); err != nil {
					fmt.Printf("%-32s ERROR %v\n", p.Name, err)
					continue
				}
				if !st.IsGitRepo {
					fmt.Printf("%-32s (not a git repo)\n", p.Name)
					continue
				}
				parts := []string{}
				if len(st.DirtyFiles) > 0 {
					parts = append(parts, fmt.Sprintf("%d dirty (%s)",
						len(st.DirtyFiles), strings.Join(st.DirtyFiles, ",")))
				}
				if st.AheadCount > 0 {
					parts = append(parts, fmt.Sprintf("%d ahead", st.AheadCount))
				}
				if st.BehindCount > 0 {
					parts = append(parts, fmt.Sprintf("%d behind", st.BehindCount))
				}
				if !st.HasRemote {
					parts = append(parts, "no remote")
				}
				if len(parts) == 0 {
					fmt.Printf("%-32s clean\n", p.Name)
					continue
				}
				anyDirty = true
				fmt.Printf("%-32s %s\n", p.Name, strings.Join(parts, " · "))
				if autoCommit && (len(st.DirtyFiles) > 0 || st.AheadCount > 0) && st.HasRemote {
					var pr methods.SyncPushResponse
					if err := c.Call("sync.push", methods.SyncPushRequest{
						IDOrPath: p.ID, Message: autoMessage,
					}, &pr); err != nil {
						fmt.Printf("  autocommit failed: %v\n", err)
					} else {
						fmt.Printf("  autocommit → committed=%v pushed=%v rejected=%v (%s)\n",
							pr.Committed, pr.Pushed, pr.Rejected, pr.Message)
					}
				}
			}
			if !anyDirty {
				fmt.Println("\nall registered projects are clean + up to date")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&autoCommit, "autocommit", false, "commit + push chief-managed files for every dirty project")
	cmd.Flags().StringVar(&autoMessage, "message", "chief: autocommit chief-managed files", "commit message used when --autocommit is set")
	return cmd
}

// ---------- 86cc: run tests ----------

// detectTestCmd inspects a project directory and returns the language
// name + argv to run its tests, or empty argv if none detected.
// Order matters: go.mod first (Go projects often have package.json for tooling).
func detectTestCmd(projectPath string) (string, []string) {
	if fi, err := os.Stat(filepath.Join(projectPath, "go.mod")); err == nil && !fi.IsDir() {
		return "go", []string{"go", "test", "./..."}
	}
	if fi, err := os.Stat(filepath.Join(projectPath, "Cargo.toml")); err == nil && !fi.IsDir() {
		return "rust", []string{"cargo", "test"}
	}
	if fi, err := os.Stat(filepath.Join(projectPath, "pyproject.toml")); err == nil && !fi.IsDir() {
		return "python", []string{"pytest"}
	}
	if fi, err := os.Stat(filepath.Join(projectPath, "package.json")); err == nil && !fi.IsDir() {
		return "node", []string{"npm", "test", "--silent"}
	}
	return "", nil
}

type testResult struct {
	Project  methods.ProjectSummary
	Language string
	Command  []string
	OK       bool
	Skipped  bool
	Duration time.Duration
	TailLog  string
	Err      error
}

func runTestsCmd() *cobra.Command {
	var timeoutSec int
	var maxParallel int
	var raise bool
	cmd := &cobra.Command{
		Use:   "tests",
		Short: "Detect test framework per project + run tests in parallel.",
		Long: `Per project, detects the test runner by file presence:
  go.mod         → go test ./...
  Cargo.toml     → cargo test
  pyproject.toml → pytest
  package.json   → npm test --silent

Runs across projects in parallel with a per-project timeout. On failure,
optionally raises an attention-urgency flag against the failing project.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			projs, err := listProjectsForRun()
			if err != nil {
				return err
			}
			sem := make(chan struct{}, maxParallel)
			var wg sync.WaitGroup
			resCh := make(chan testResult, len(projs))
			for _, p := range projs {
				lang, cmdArgs := detectTestCmd(p.Path)
				if lang == "" {
					resCh <- testResult{Project: p, Skipped: true}
					continue
				}
				wg.Add(1)
				sem <- struct{}{}
				go func(p methods.ProjectSummary, lang string, argv []string) {
					defer wg.Done()
					defer func() { <-sem }()
					start := time.Now()
					ctx, cancel := context.WithTimeout(context.Background(),
						time.Duration(timeoutSec)*time.Second)
					defer cancel()
					out, err := runTestExec(ctx, p.Path, argv)
					resCh <- testResult{
						Project: p, Language: lang, Command: argv,
						OK: err == nil, Duration: time.Since(start),
						TailLog: tailN(out, 20), Err: err,
					}
				}(p, lang, cmdArgs)
			}
			go func() { wg.Wait(); close(resCh) }()

			pass, fail, skip := 0, 0, 0
			var failures []testResult
			for r := range resCh {
				if r.Skipped {
					skip++
					fmt.Printf("%-32s (no known test framework)\n", r.Project.Name)
					continue
				}
				status := "PASS"
				if !r.OK {
					status = "FAIL"
					fail++
					failures = append(failures, r)
				} else {
					pass++
				}
				fmt.Printf("%-32s %-4s %-6s %5.1fs  %s\n",
					r.Project.Name, r.Language, status,
					r.Duration.Seconds(), strings.Join(r.Command, " "))
			}
			fmt.Printf("\n%d passed · %d failed · %d skipped\n", pass, fail, skip)
			if raise && fail > 0 {
				c, err := dial()
				if err == nil {
					defer c.Close()
					for _, f := range failures {
						body := fmt.Sprintf("`%s` failed in %.1fs.\n\nTail:\n```\n%s\n```",
							strings.Join(f.Command, " "), f.Duration.Seconds(), f.TailLog)
						var resp methods.FlagRaiseResponse
						_ = c.Call("flag.raise", methods.FlagRaiseRequest{
							ProjectID: f.Project.ID,
							Kind:      "question",
							Urgency:   "attention",
							Question:  body,
						}, &resp)
					}
					fmt.Printf("raised %d attention flags for failing projects\n", len(failures))
				}
			}
			if fail > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&timeoutSec, "timeout", 300, "per-project timeout in seconds")
	cmd.Flags().IntVar(&maxParallel, "parallel", 4, "max concurrent test runs")
	cmd.Flags().BoolVar(&raise, "flag-failures", true, "raise attention flags for failing projects")
	return cmd
}

func runTestExec(ctx context.Context, dir string, argv []string) (string, error) {
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	return string(out), err
}

func tailN(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// ---------- b153: run pr-status ----------

func runPRStatusCmd() *cobra.Command {
	var stallDays int
	cmd := &cobra.Command{
		Use:   "pr-status",
		Short: "gh pr status per completed autobranch task + stale PR sweep.",
		Long: `For every task marked done that lives in a project with a git
remote, checks whether the chief-autogenerated branch has a PR. Missing
PRs get a suggested ` + "`gh pr create`" + ` recipe.

Also lists open PRs (across every project) with no update in >N days
(--stall-days, default 7) so long-forgotten PRs surface.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := exec.LookPath("gh"); err != nil {
				return fmt.Errorf("gh not on PATH — install via `brew install gh`")
			}
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var listResp methods.ProjectListResponse
			if err := c.Call("project.list", methods.ProjectListRequest{}, &listResp); err != nil {
				return err
			}

			for _, p := range listResp.Projects {
				// Stalled PRs check first — always want that summary regardless
				// of whether we found any autobranch tasks.
				stallList := stalePRs(p.Path, stallDays)
				var doneTasks methods.BacklogListResponse
				if err := c.Call("backlog.list", methods.BacklogListRequest{
					ProjectID: p.ID, Status: "done",
				}, &doneTasks); err != nil {
					continue
				}
				missing := 0
				for _, t := range doneTasks.Tasks {
					branch := "chief/" + t.ID + "-" + slugForTitle(t.Title)
					if !branchExists(p.Path, branch) {
						continue
					}
					if hasPR(p.Path, branch) {
						continue
					}
					if missing == 0 {
						fmt.Printf("\n=== %s ===\n", p.Name)
					}
					missing++
					fmt.Printf("  no PR for %s (%s)\n", t.ID, t.Title)
					fmt.Printf("    gh -R %s pr create --head %s \\\n",
						guessRepoSlug(p.Path), branch)
					fmt.Printf("       --title \"chief/%s: %s\" --body ...\n", t.ID, t.Title)
				}
				for _, s := range stallList {
					if missing == 0 {
						fmt.Printf("\n=== %s ===\n", p.Name)
					}
					missing++
					fmt.Printf("  stale PR: %s\n", s)
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&stallDays, "stall-days", 7, "flag open PRs with no update in more than N days")
	return cmd
}

// slugForTitle matches gitshim.slug so we regenerate the same branch
// name from just the task id + title. Kept local to avoid a public API
// bump on gitshim.
func slugForTitle(title string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

func branchExists(dir, branch string) bool {
	c := exec.Command("git", "-C", dir, "show-ref", "--verify", "--quiet",
		"refs/heads/"+branch)
	return c.Run() == nil
}

func hasPR(dir, branch string) bool {
	out, err := exec.Command("gh", "-R", guessRepoSlug(dir),
		"pr", "list", "--head", branch, "--json", "number", "-q", ".[0].number").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

func guessRepoSlug(dir string) string {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	u := strings.TrimSpace(string(out))
	// git@github.com:owner/repo.git → owner/repo
	// https://github.com/owner/repo.git → owner/repo
	u = strings.TrimSuffix(u, ".git")
	if i := strings.Index(u, "github.com"); i >= 0 {
		rest := u[i+len("github.com"):]
		rest = strings.TrimPrefix(rest, ":")
		rest = strings.TrimPrefix(rest, "/")
		return rest
	}
	return u
}

func stalePRs(dir string, days int) []string {
	slug := guessRepoSlug(dir)
	if slug == "" {
		return nil
	}
	out, err := exec.Command("gh", "-R", slug, "pr", "list",
		"--state", "open", "--json", "number,title,updatedAt,url").Output()
	if err != nil {
		return nil
	}
	type pr struct {
		Number    int       `json:"number"`
		Title     string    `json:"title"`
		UpdatedAt time.Time `json:"updatedAt"`
		URL       string    `json:"url"`
	}
	var prs []pr
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	var stale []string
	for _, p := range prs {
		if p.UpdatedAt.Before(cutoff) {
			stale = append(stale, fmt.Sprintf("#%d %s (last updated %s ago) %s",
				p.Number, p.Title, humanDur(time.Since(p.UpdatedAt)), p.URL))
		}
	}
	return stale
}

func humanDur(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// ---------- 9760: run deps-fresh ----------

func runDepsFreshCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deps-fresh",
		Short: "Report stale dependencies per project (go/npm/cargo).",
		RunE: func(cmd *cobra.Command, args []string) error {
			projs, err := listProjectsForRun()
			if err != nil {
				return err
			}
			for _, p := range projs {
				checks := depsChecksFor(p.Path)
				if len(checks) == 0 {
					continue
				}
				fmt.Printf("\n=== %s ===\n", p.Name)
				for _, ch := range checks {
					n, tail := ch.run(p.Path)
					fmt.Printf("  %-6s %2d stale\n", ch.name, n)
					if n > 0 && tail != "" {
						fmt.Printf("%s\n", indent(tail, "    "))
					}
				}
			}
			fmt.Println()
			fmt.Println("Suggested follow-ups: `chief task add \"update <ecosystem> deps in <project>\"`")
			return nil
		},
	}
	return cmd
}

type depsCheck struct {
	name string
	run  func(dir string) (int, string)
}

func depsChecksFor(dir string) []depsCheck {
	var out []depsCheck
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		out = append(out, depsCheck{"go", checkGoDeps})
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		out = append(out, depsCheck{"npm", checkNpmDeps})
	}
	if _, err := os.Stat(filepath.Join(dir, "Cargo.toml")); err == nil {
		out = append(out, depsCheck{"cargo", checkCargoDeps})
	}
	return out
}

func checkGoDeps(dir string) (int, string) {
	if _, err := exec.LookPath("go"); err != nil {
		return 0, ""
	}
	out, _ := exec.Command("go", "list", "-u", "-m", "-json", "all").Output()
	if len(out) == 0 {
		// Fall back to text output when the module tree is broken.
		return 0, ""
	}
	// Each JSON object represents one module; count those with an Update.
	// Streaming decode instead of a giant slice since deps can number 500+.
	dec := json.NewDecoder(strings.NewReader(string(out)))
	type mod struct {
		Path    string
		Version string
		Update  *struct {
			Version string
		}
	}
	var stale []string
	for {
		var m mod
		if err := dec.Decode(&m); err != nil {
			break
		}
		if m.Update != nil {
			stale = append(stale, fmt.Sprintf("%s %s → %s", m.Path, m.Version, m.Update.Version))
		}
	}
	sort.Strings(stale)
	return len(stale), strings.Join(topN(stale, 5), "\n")
}

func checkNpmDeps(dir string) (int, string) {
	if _, err := exec.LookPath("npm"); err != nil {
		return 0, ""
	}
	// `npm outdated` exits non-zero when it finds outdated deps —
	// combined output includes what we want either way.
	c := exec.Command("npm", "outdated")
	c.Dir = dir
	out, _ := c.CombinedOutput()
	txt := strings.TrimRight(string(out), "\n")
	if txt == "" {
		return 0, ""
	}
	lines := strings.Split(txt, "\n")
	// Header line ("Package  Current  Wanted  Latest") counts as 1; subtract.
	n := len(lines) - 1
	if n < 0 {
		n = 0
	}
	return n, strings.Join(topN(lines, 6), "\n")
}

func checkCargoDeps(dir string) (int, string) {
	if _, err := exec.LookPath("cargo"); err != nil {
		return 0, ""
	}
	c := exec.Command("cargo", "outdated", "--depth", "1")
	c.Dir = dir
	out, _ := c.CombinedOutput()
	txt := strings.TrimRight(string(out), "\n")
	if txt == "" {
		return 0, ""
	}
	lines := strings.Split(txt, "\n")
	// cargo-outdated is a subcommand — may be missing.
	if strings.Contains(txt, "no such subcommand") {
		return 0, "install `cargo install cargo-outdated` for cargo depth"
	}
	n := 0
	for _, ln := range lines {
		if strings.HasPrefix(ln, "Name") || ln == "" || strings.HasPrefix(ln, "---") {
			continue
		}
		n++
	}
	return n, strings.Join(topN(lines, 8), "\n")
}

func topN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}

// ---------- 5756: run review-diff ----------

func runReviewDiffCmd() *cobra.Command {
	var claudeBin string
	cmd := &cobra.Command{
		Use:   "review-diff",
		Short: "Ask Claude to summarize uncommitted diffs; raise info flags.",
		Long: `For each project with a non-empty ` + "`git diff HEAD`" + `, shells to
` + "`claude -p`" + ` with the diff and asks for a 2-3 sentence summary + risk
callouts. The result is raised as an info-urgency flag on the project.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := exec.LookPath(claudeBin); err != nil {
				return fmt.Errorf("%s not on PATH — pass --claude-bin", claudeBin)
			}
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			projs, err := listProjectsForRun()
			if err != nil {
				return err
			}
			for _, p := range projs {
				diff := gitDiff(p.Path)
				if strings.TrimSpace(diff) == "" {
					continue
				}
				fmt.Printf("=== %s (%d bytes diff) — shelling to Claude…\n",
					p.Name, len(diff))
				summary, err := reviewDiffWithClaude(claudeBin, p.Name, diff)
				if err != nil {
					fmt.Printf("  review failed: %v\n", err)
					continue
				}
				fmt.Println(indent(summary, "  "))
				body := fmt.Sprintf("Uncommitted diff review:\n\n%s", summary)
				var resp methods.FlagRaiseResponse
				if err := c.Call("flag.raise", methods.FlagRaiseRequest{
					ProjectID: p.ID, Kind: "question",
					Urgency: "info", Question: body,
				}, &resp); err != nil {
					fmt.Printf("  flag.raise failed: %v\n", err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&claudeBin, "claude-bin", "claude", "path to the claude CLI")
	return cmd
}

func gitDiff(dir string) string {
	c := exec.Command("git", "-C", dir, "diff", "HEAD")
	out, _ := c.CombinedOutput()
	// Cap at 100KB so we don't blow up Claude's prompt when someone
	// has a huge WIP.
	if len(out) > 100*1024 {
		return string(out[:100*1024]) + "\n\n… (truncated, diff was >100KB)"
	}
	return string(out)
}

func reviewDiffWithClaude(bin, projectName, diff string) (string, error) {
	prompt := "You are reviewing uncommitted changes in the git repo `" + projectName +
		"`. Read the diff below and reply with EXACTLY:\n" +
		"  - a 2-3 sentence summary of what's changing\n" +
		"  - a bulleted list of any RISK callouts (correctness, security, backwards compat) — omit if none\n" +
		"Keep output under 200 words. No preamble.\n\n" +
		"```diff\n" + diff + "\n```"
	c := exec.Command(bin, "-p", prompt)
	out, err := c.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ---------- a0e8: run backup + restore ----------

func runBackupCmd() *cobra.Command {
	var destDir string
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Snapshot chief.db + per-project markdown into a timestamped tarball.",
		Long: `Bundles into a tar.gz under ~/Library/Backups/Chief/:
  - chief.db (from ~/Library/Application Support/Chief/)
  - backlog.md, completedlog.md, dropped.md, constitution.md, PROJECT.md
    (+ .chief/project.yaml) from every registered project

Restore via ` + "`chief run restore <tarball>`" + `.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, _ := os.UserHomeDir()
			if destDir == "" {
				destDir = filepath.Join(home, "Library", "Backups", "Chief")
			}
			if err := os.MkdirAll(destDir, 0o755); err != nil {
				return err
			}
			// Deterministic filename: chief-backup-YYYYMMDDTHHMMSS.tgz
			stamp := time.Now().UTC().Format("20060102T150405")
			out := filepath.Join(destDir, "chief-backup-"+stamp+".tgz")

			projs, err := listProjectsForRun()
			if err != nil {
				return err
			}
			paths := []string{
				filepath.Join(home, "Library", "Application Support", "Chief", "chief.db"),
			}
			perProjectFiles := []string{
				"backlog.md", "completedlog.md", "dropped.md",
				"constitution.md", "PROJECT.md", ".chief/project.yaml",
			}
			for _, p := range projs {
				for _, f := range perProjectFiles {
					candidate := filepath.Join(p.Path, f)
					if _, err := os.Stat(candidate); err == nil {
						paths = append(paths, candidate)
					}
				}
			}

			if err := writeTarball(out, paths); err != nil {
				return err
			}
			fi, _ := os.Stat(out)
			fmt.Printf("wrote %s (%d files, %.1f MB)\n",
				out, len(paths), float64(fi.Size())/1024/1024)
			return nil
		},
	}
	cmd.Flags().StringVar(&destDir, "dest", "", "destination directory (default: ~/Library/Backups/Chief)")
	return cmd
}

func runRestoreCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "restore <tarball>",
		Short: "Restore chief.db + per-project files from a tarball made by `run backup`.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return extractTarball(args[0], dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "list files that would be restored without touching disk")
	return cmd
}

// ---------- 8e4c: run refresh <project> ----------

func runRefreshCmd() *cobra.Command {
	var poke string
	cmd := &cobra.Command{
		Use:   "refresh <project>",
		Short: "git pull + rescan backlog + poke the bound cmux Claude to re-orient.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var pull methods.SyncPullResponse
			if err := c.Call("sync.pull", methods.SyncPullRequest{IDOrPath: args[0]}, &pull); err != nil {
				return fmt.Errorf("pull: %w", err)
			}
			fmt.Printf("git pull: fetched=%v merged=%v (%s)\n", pull.Fetched, pull.Merged, pull.Message)
			if len(pull.Conflicts) > 0 {
				fmt.Printf("  conflicts: %v — resolve then re-run\n", pull.Conflicts)
				return nil
			}
			var rescan methods.ProjectRescanResponse
			if err := c.Call("project.rescan", methods.ProjectRescanRequest{IDOrPath: args[0]}, &rescan); err != nil {
				return fmt.Errorf("rescan: %w", err)
			}
			fmt.Printf("rescan: %d tasks\n", rescan.Tasks)
			// Poke the bound cmux Claude, if any.
			var send methods.CmuxDirectSendResponse
			if err := c.Call("cmux.direct_send", methods.CmuxDirectSendRequest{
				IDOrPath: args[0], Prompt: poke,
			}, &send); err != nil {
				// Non-fatal: no cmux binding means the user drives Claude
				// manually; the git pull + rescan already happened.
				fmt.Printf("cmux poke skipped (%v)\n", err)
				return nil
			}
			fmt.Printf("cmux poked → surface=%s\n", send.SurfaceRef)
			return nil
		},
	}
	cmd.Flags().StringVar(&poke, "poke",
		"Please `git pull` and re-orient — the constitution + backlog may have changed. Then continue with the next backlog item.",
		"prompt to inject into the bound cmux Claude")
	return cmd
}

// ---------- 2b3f: run focus <project> ----------

func runFocusCmd() *cobra.Command {
	var minutes int
	cmd := &cobra.Command{
		Use:   "focus <project>",
		Short: "Anti-context-switch mode: mute every OTHER project's flags for N min.",
		Long: `Snoozes attention flags for every registered project EXCEPT the
focused one, for the specified duration. When focus expires, snoozes
lift naturally (snooze cursor is per-project + time-bounded).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.FocusEnterResponse
			if err := c.Call("focus.enter", methods.FocusEnterRequest{
				IDOrPath: args[0], Minutes: minutes,
			}, &resp); err != nil {
				return err
			}
			fmt.Printf("focus on %s for %d min — snoozed %d other projects until %s\n",
				resp.FocusedProject, resp.Minutes, resp.SnoozedOthers, resp.Until.Format(time.RFC3339))
			return nil
		},
	}
	cmd.Flags().IntVar(&minutes, "mins", 60, "focus duration in minutes")
	return cmd
}
