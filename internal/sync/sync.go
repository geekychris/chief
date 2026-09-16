// Package sync provides machine-to-machine sync of chief-managed
// markdown files (backlog.md, completedlog.md, dropped.md, and
// .chief/project.yaml) via the user's existing git remote.
//
// Design choice: no separate sync repo. If the project is already a
// git repo (most are), chief just calls `git add + commit + push` for
// the chief-managed subset, and `git pull` to bring another machine's
// changes over. Auth uses the user's own git config (SSH keys, PATs,
// keychain-cached credentials) — chief doesn't touch that.
//
// Conflicts are handled the way git normally handles them: if push
// rejects (non-fast-forward) or pull produces a merge conflict, the
// caller is told and expected to resolve at the git level. Chief
// raises an info-urgency flag so the user notices next time they
// glance at the inbox.
//
// Not synced: chief.db (per-machine ephemeral state — events,
// sessions, flags). The markdown files are the source of truth; the
// database is a per-machine index built from them.
package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SyncedFiles is the set of files chief owns + will manage across
// machines. Kept as a fixed list — if the user adds their own files
// they can commit those via plain git, chief only touches these.
var SyncedFiles = []string{
	"backlog.md",
	"completedlog.md",
	"dropped.md",
	"constitution.md",
	"PROJECT.md",
	".chief/project.yaml",
}

// Status is the current sync state for one project.
type Status struct {
	IsGitRepo      bool     `json:"is_git_repo"`
	HasRemote      bool     `json:"has_remote"`
	Branch         string   `json:"branch,omitempty"`
	RemoteURL      string   `json:"remote_url,omitempty"`
	AheadCount     int      `json:"ahead_count"`      // commits local has that remote doesn't
	BehindCount    int      `json:"behind_count"`     // commits remote has that local doesn't
	DirtyFiles     []string `json:"dirty_files"`      // chief-managed files with uncommitted changes
	LastSyncRFC3339 string  `json:"last_sync,omitempty"`
}

// GetStatus inspects the project's git state without mutating anything.
func GetStatus(ctx context.Context, projectPath string) (Status, error) {
	st := Status{}
	if _, err := exec.LookPath("git"); err != nil {
		return st, fmt.Errorf("git not on PATH")
	}
	if _, err := runGit(ctx, projectPath, "rev-parse", "--is-inside-work-tree"); err != nil {
		return st, nil // not a git repo — return zero-value status
	}
	st.IsGitRepo = true
	if b, err := runGit(ctx, projectPath, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		st.Branch = strings.TrimSpace(b)
	}
	if remote, err := runGit(ctx, projectPath, "remote", "get-url", "origin"); err == nil {
		st.RemoteURL = strings.TrimSpace(remote)
		st.HasRemote = st.RemoteURL != ""
	}
	// Ahead/behind — needs a fetched remote-tracking branch. Best-
	// effort; missing remote-tracking silently reports 0/0.
	if st.HasRemote && st.Branch != "" {
		if out, err := runGit(ctx, projectPath, "rev-list", "--left-right", "--count",
			"HEAD..."+st.Branch+"@{upstream}"); err == nil {
			var ahead, behind int
			fmt.Sscanf(strings.TrimSpace(out), "%d\t%d", &ahead, &behind)
			st.AheadCount = ahead
			st.BehindCount = behind
		}
	}
	// Dirty chief-managed files. porcelain output format is
	// "XY <path>\n" where X = staged, Y = worktree. Trimming the whole
	// output eats the leading space on the first line (staged bit),
	// which shifts the path substring — split on \n first, then
	// slice each line at fixed offset 3.
	if out, err := runGit(ctx, projectPath, "status", "--porcelain", "--"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if len(line) < 4 {
				continue
			}
			p := strings.TrimSpace(line[3:])
			for _, tracked := range SyncedFiles {
				if p == tracked {
					st.DirtyFiles = append(st.DirtyFiles, p)
				}
			}
		}
	}
	return st, nil
}

// PushResult carries what Push did.
type PushResult struct {
	Committed  bool   `json:"committed"`   // true if we made a commit
	Pushed     bool   `json:"pushed"`      // true if push succeeded
	CommitSha  string `json:"commit_sha,omitempty"`
	Rejected   bool   `json:"rejected"`    // push rejected (non-ff etc.)
	Message    string `json:"message"`     // human-readable outcome
}

// Push commits any dirty chief-managed files with a canned message
// + pushes to origin/<branch>. If nothing's dirty and we're already
// up to date, no-op. If the push is rejected (non-ff, permission),
// returns Rejected=true and a message the caller can surface as a
// flag.
func Push(ctx context.Context, projectPath, message string) (PushResult, error) {
	res := PushResult{}
	st, err := GetStatus(ctx, projectPath)
	if err != nil {
		return res, err
	}
	if !st.IsGitRepo {
		return res, errors.New("not a git repo — init one first: `cd <project> && git init && git remote add origin <url>`")
	}
	if !st.HasRemote {
		return res, errors.New("no origin remote — add one first: `git remote add origin <url>`")
	}
	// Commit dirty synced files (best-effort: file may not exist yet).
	if len(st.DirtyFiles) > 0 {
		args := append([]string{"add", "--"}, st.DirtyFiles...)
		if _, err := runGit(ctx, projectPath, args...); err != nil {
			return res, fmt.Errorf("git add: %w", err)
		}
		msg := message
		if msg == "" {
			msg = "chief sync: " + strings.Join(st.DirtyFiles, ", ")
		}
		if _, err := runGit(ctx, projectPath, "commit", "-m", msg); err != nil {
			return res, fmt.Errorf("git commit: %w", err)
		}
		res.Committed = true
		if sha, err := runGit(ctx, projectPath, "rev-parse", "HEAD"); err == nil {
			res.CommitSha = strings.TrimSpace(sha)[:8]
		}
	}
	// Push (skips work if nothing changed + already synced).
	pushOut, err := runGit(ctx, projectPath, "push", "origin", "HEAD:"+st.Branch)
	if err != nil {
		if strings.Contains(pushOut, "rejected") || strings.Contains(pushOut, "non-fast-forward") {
			res.Rejected = true
			res.Message = "push rejected — remote has commits you don't; run `chief sync pull` first"
			return res, nil
		}
		return res, fmt.Errorf("git push: %w (output: %s)", err, strings.TrimSpace(pushOut))
	}
	res.Pushed = true
	if res.Committed {
		res.Message = fmt.Sprintf("committed %s + pushed to origin/%s", res.CommitSha, st.Branch)
	} else {
		res.Message = fmt.Sprintf("already up-to-date; pushed 0 new commits to origin/%s", st.Branch)
	}
	return res, nil
}

// PullResult carries what Pull did.
type PullResult struct {
	Fetched      bool     `json:"fetched"`
	Merged       bool     `json:"merged"`
	Conflicts    []string `json:"conflicts,omitempty"`
	MergedFiles  []string `json:"merged_files,omitempty"`
	Message      string   `json:"message"`
}

// Pull fetches + merges origin/<branch>. If chief-managed files
// conflict, returns them in Conflicts so the caller can raise a flag
// and prompt the user to resolve.
func Pull(ctx context.Context, projectPath string) (PullResult, error) {
	res := PullResult{}
	st, err := GetStatus(ctx, projectPath)
	if err != nil {
		return res, err
	}
	if !st.IsGitRepo || !st.HasRemote {
		return res, errors.New("no git remote to pull from")
	}
	// Use rebase so history stays linear on the local side.
	out, err := runGit(ctx, projectPath, "pull", "--rebase", "origin", st.Branch)
	res.Fetched = true
	if err != nil {
		// Look for conflict markers.
		if strings.Contains(out, "CONFLICT") || strings.Contains(out, "conflict") {
			// Parse conflicting files from `git diff --name-only --diff-filter=U`.
			if list, lerr := runGit(ctx, projectPath, "diff", "--name-only", "--diff-filter=U"); lerr == nil {
				for _, p := range strings.Split(strings.TrimSpace(list), "\n") {
					if p == "" {
						continue
					}
					res.Conflicts = append(res.Conflicts, p)
				}
			}
			res.Message = "pull conflict — resolve manually: `git status`, edit the files, then `git rebase --continue`"
			return res, nil
		}
		return res, fmt.Errorf("git pull: %w (output: %s)", err, strings.TrimSpace(out))
	}
	res.Merged = true
	// Files touched by the merge, from FETCH_HEAD..HEAD range.
	if diffOut, err := runGit(ctx, projectPath, "diff", "--name-only", "ORIG_HEAD..HEAD"); err == nil {
		for _, p := range strings.Split(strings.TrimSpace(diffOut), "\n") {
			if p == "" {
				continue
			}
			res.MergedFiles = append(res.MergedFiles, p)
		}
	}
	if len(res.MergedFiles) == 0 {
		res.Message = "already up-to-date"
	} else {
		res.Message = fmt.Sprintf("merged %d file(s)", len(res.MergedFiles))
	}
	return res, nil
}

// runGit shells `git <args>` in dir with a 20s timeout and returns
// combined stdout/stderr so error paths have context.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(sctx, "git", args...)
	cmd.Dir = filepath.Clean(dir)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
