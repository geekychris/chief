package gitshim

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlugAndBranchName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Wire up auth callback route!", "wire-up-auth-callback-route"},
		{"", ""},
		{"---weird!!!---", "weird"},
		// unicode.IsLetter matches non-ASCII too — git branch names support UTF-8.
		{"日本語 title", "日本語-title"},
		{"!!!", ""}, // pure punctuation → empty
	}
	for _, c := range cases {
		if got := slug(c.in); got != c.want {
			t.Errorf("slug(%q) = %q want %q", c.in, got, c.want)
		}
	}
	// Branch name uses id-only when slug is empty.
	if got := BranchName("abcd", "!!!"); got != "chief/abcd" {
		t.Errorf("empty-slug branch: got %q", got)
	}
	if got := BranchName("abcd", "Add API"); got != "chief/abcd-add-api" {
		t.Errorf("BranchName: got %q", got)
	}
}

func TestIsChiefBranch(t *testing.T) {
	if !IsChiefBranch("chief/abcd-thing") {
		t.Error("chief/abcd-thing should match")
	}
	if IsChiefBranch("main") {
		t.Error("main should not match")
	}
	if IsChiefBranch("feature/x") {
		t.Error("feature/x should not match")
	}
}

// TestEnsureBranch_SkipsWhenNotGitRepo — no .git dir → "not a git repo".
func TestEnsureBranch_SkipsWhenNotGitRepo(t *testing.T) {
	dir := t.TempDir()
	res, err := EnsureBranch(context.Background(), dir, "abcd", "some title")
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != "not a git repo" {
		t.Errorf("Skipped: got %q want 'not a git repo'", res.Skipped)
	}
	if res.Created {
		t.Error("Created should be false")
	}
}

// TestEnsureBranch_SkipsWhenDirty — repo with uncommitted changes.
func TestEnsureBranch_SkipsWhenDirty(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	mustRunGit(t, dir, "init", "-q", "-b", "main")
	mustRunGit(t, dir, "config", "user.email", "t@t")
	mustRunGit(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, dir, "add", "-A")
	mustRunGit(t, dir, "commit", "-q", "-m", "init")
	// Now dirty it up.
	if err := os.WriteFile(filepath.Join(dir, "dirty"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := EnsureBranch(context.Background(), dir, "abcd", "some title")
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != "working tree dirty" {
		t.Errorf("Skipped: got %q want 'working tree dirty'", res.Skipped)
	}
}

// TestEnsureBranch_CreatesAndSwitches — clean repo → branch created.
func TestEnsureBranch_CreatesAndSwitches(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	mustRunGit(t, dir, "init", "-q", "-b", "main")
	mustRunGit(t, dir, "config", "user.email", "t@t")
	mustRunGit(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, dir, "add", "-A")
	mustRunGit(t, dir, "commit", "-q", "-m", "init")

	res, err := EnsureBranch(context.Background(), dir, "abcd", "add auth")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created {
		t.Error("Created should be true on first call")
	}
	if res.Branch != "chief/abcd-add-auth" {
		t.Errorf("branch: got %q", res.Branch)
	}
	// Verify HEAD points at the new branch.
	cur := strings.TrimSpace(mustRunGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD"))
	if cur != "chief/abcd-add-auth" {
		t.Errorf("HEAD: got %q", cur)
	}
	// Second call: idempotent.
	res2, err := EnsureBranch(context.Background(), dir, "abcd", "add auth")
	if err != nil {
		t.Fatal(err)
	}
	if res2.Skipped != "already on branch" {
		t.Errorf("second call: got Skipped=%q want 'already on branch'", res2.Skipped)
	}
	if res2.Created {
		t.Error("second call should not report Created")
	}
}

func mustRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
