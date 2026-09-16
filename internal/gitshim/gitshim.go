// Package gitshim wraps the two git/gh CLI operations Chief cares
// about for its per-task automation:
//
//  1. Auto-branch: on task.send, create chief/<id>-<slug> so the
//     work Claude does lives on a scoped branch (guarded by clean-repo
//     check so we never clobber uncommitted work).
//  2. PR gating: on Rescan sweep, ask `gh pr list` whether an open PR
//     references the task id before allowing [x] to archive.
//
// Both operations are best-effort: not-a-git-repo, gh-not-installed,
// or network errors all degrade gracefully to "no-op" rather than
// wedging the caller.
package gitshim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// slug turns a task title into a branch-safe suffix: lowercased,
// alphanumeric only, hyphen-separated, capped at 40 chars. "Wire up
// auth callback route!" → "wire-up-auth-callback-route".
func slug(title string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

// BranchName is the canonical branch we create for a task.
func BranchName(taskID, title string) string {
	sl := slug(title)
	if sl == "" {
		return "chief/" + taskID
	}
	return "chief/" + taskID + "-" + sl
}

// EnsureBranch runs `git switch -c <branch>` in repoDir if:
//   - The dir is a git repo (has .git)
//   - The working tree is clean (no uncommitted or unstaged changes)
//   - The branch doesn't already exist (in which case: switch to it)
//   - We're not already on it
//
// Returns the resolved branch name and a "created" bool indicating
// whether a new branch was minted vs. reused. Any check-failed reason
// is returned as a non-nil skipped-string; the caller treats these
// as informational (they're not errors).
type EnsureResult struct {
	Branch  string
	Created bool
	Skipped string // non-empty when we chose NOT to branch
}

func EnsureBranch(ctx context.Context, repoDir, taskID, taskTitle string) (EnsureResult, error) {
	branch := BranchName(taskID, taskTitle)
	res := EnsureResult{Branch: branch}
	if _, err := exec.LookPath("git"); err != nil {
		res.Skipped = "git not on PATH"
		return res, nil
	}
	if _, err := statDir(filepath.Join(repoDir, ".git")); err != nil {
		res.Skipped = "not a git repo"
		return res, nil
	}
	// Current branch — no-op if we're already on it.
	cur, err := runGit(ctx, repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil && strings.TrimSpace(cur) == branch {
		res.Skipped = "already on branch"
		return res, nil
	}
	// Dirty check.
	dirty, err := runGit(ctx, repoDir, "status", "--porcelain")
	if err != nil {
		return res, fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(dirty) != "" {
		res.Skipped = "working tree dirty"
		return res, nil
	}
	// Does the branch already exist?
	_, err = runGit(ctx, repoDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		// Exists → switch to it.
		if _, err := runGit(ctx, repoDir, "switch", branch); err != nil {
			return res, fmt.Errorf("switch: %w", err)
		}
		return res, nil
	}
	// Doesn't exist → create.
	if _, err := runGit(ctx, repoDir, "switch", "-c", branch); err != nil {
		return res, fmt.Errorf("switch -c: %w", err)
	}
	res.Created = true
	return res, nil
}

// HasOpenPRForTask returns true if `gh pr list` finds any open PR that
// references the task id (in title or branch name). Returns
// (found, checked, err) where checked=false means we couldn't run the
// check at all — caller should treat that as "no gating" rather than
// "block everything". Missing gh, missing auth, non-github remote,
// and network errors all fall into !checked.
func HasOpenPRForTask(ctx context.Context, repoDir, taskID string) (found bool, checked bool, err error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return false, false, nil
	}
	if _, err := statDir(filepath.Join(repoDir, ".git")); err != nil {
		return false, false, nil
	}
	sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(sctx, "gh", "pr", "list",
		"--state", "open",
		"--search", "chief/"+taskID+" in:title,body,head",
		"--json", "number,title",
	)
	cmd.Dir = repoDir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return false, false, nil // treat all failures as "can't gate"
	}
	// Any non-empty JSON array (starting with '[' followed by a '{')
	// means at least one PR was returned.
	body := strings.TrimSpace(out.String())
	if body == "" || body == "[]" {
		return false, true, nil
	}
	if strings.HasPrefix(body, "[") && strings.Contains(body, "{") {
		return true, true, nil
	}
	return false, true, nil
}

// runGit shells `git <args...>` in repoDir with a hard timeout.
func runGit(ctx context.Context, repoDir string, args ...string) (string, error) {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(sctx, "git", args...)
	cmd.Dir = repoDir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", errors.New(strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}

func statDir(path string) (string, error) {
	// os.Stat check inlined to keep imports small. exec.Command is
	// fine to use synchronously via a stat command; but the direct
	// filesystem check is simpler.
	fi, err := statOS(path)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s: not a directory", path)
	}
	return path, nil
}

// statOS is factored out so tests could stub it if needed.
var statOS = defaultStat

var branchNameRE = regexp.MustCompile(`^chief/[0-9a-f]+`)

// IsChiefBranch reports whether name looks like one we minted. Handy
// for UI badges.
func IsChiefBranch(name string) bool {
	return branchNameRE.MatchString(strings.TrimSpace(name))
}
