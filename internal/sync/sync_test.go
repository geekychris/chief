package sync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGetStatus_NotAGitRepo — clean tempdir returns zero-value.
func TestGetStatus_NotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	st, err := GetStatus(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.IsGitRepo {
		t.Error("expected IsGitRepo=false for empty dir")
	}
}

// TestPushPull_RoundTrip — set up an origin bare repo + two working
// clones; push from one, pull from the other, verify the changes
// propagated.
func TestPushPull_RoundTrip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	mustRun(t, root, "git", "init", "--bare", origin)

	// Clone A (the "push" side). Force branch "main" everywhere so
	// this test doesn't depend on the machine's default (master vs main).
	sideA := filepath.Join(root, "A")
	mustRun(t, root, "git", "clone", origin, sideA)
	mustRun(t, sideA, "git", "config", "user.email", "t@t")
	mustRun(t, sideA, "git", "config", "user.name", "t")
	mustRun(t, sideA, "git", "checkout", "-b", "main")
	// Seed a chief-managed file.
	_ = os.WriteFile(filepath.Join(sideA, "backlog.md"),
		[]byte("## Backlog\n- [ ] {id:abcd} hello\n"), 0o644)
	mustRun(t, sideA, "git", "add", "-A")
	mustRun(t, sideA, "git", "commit", "-m", "initial")
	mustRun(t, sideA, "git", "push", "-u", "origin", "main")

	// Verify status shows clean + up-to-date.
	st, err := GetStatus(context.Background(), sideA)
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsGitRepo || !st.HasRemote {
		t.Errorf("status: git=%v remote=%v want both true", st.IsGitRepo, st.HasRemote)
	}
	if len(st.DirtyFiles) != 0 {
		t.Errorf("dirty: got %v want []", st.DirtyFiles)
	}

	// Modify backlog.md + push via our helper.
	_ = os.WriteFile(filepath.Join(sideA, "backlog.md"),
		[]byte("## Backlog\n- [ ] {id:abcd} hello\n- [ ] {id:efgh} world\n"), 0o644)
	pushRes, err := Push(context.Background(), sideA, "add efgh")
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if !pushRes.Committed || !pushRes.Pushed {
		t.Errorf("push result: committed=%v pushed=%v — want both", pushRes.Committed, pushRes.Pushed)
	}

	// Clone B (the "pull" side). Explicit --branch main because
	// the bare origin's HEAD may still point at the git-default
	// (master) ref that no one ever created.
	sideB := filepath.Join(root, "B")
	mustRun(t, root, "git", "clone", "--branch", "main", origin, sideB)
	mustRun(t, sideB, "git", "config", "user.email", "t@t")
	mustRun(t, sideB, "git", "config", "user.name", "t")
	pullRes, err := Pull(context.Background(), sideB)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !pullRes.Fetched {
		t.Error("expected Fetched=true")
	}
	// Verify file content propagated.
	got, _ := os.ReadFile(filepath.Join(sideB, "backlog.md"))
	if !contains(string(got), "efgh") {
		t.Errorf("pull didn't bring efgh over: %q", string(got))
	}
}

// TestPush_NoRemoteErrors — helpful error when origin's missing.
func TestPush_NoRemoteErrors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	mustRun(t, dir, "git", "init", "-b", "main")
	_, err := Push(context.Background(), dir, "")
	if err == nil || !contains(err.Error(), "no origin remote") {
		t.Errorf("want 'no origin remote' error, got %v", err)
	}
}

func mustRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
