package backlog

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseBasic(t *testing.T) {
	src := `# Backlog

## Auth
- [ ] {id:a3f1} Wire up auth callback [priority:high]
  Details as sub-bullet.
  - More detail.
- [ ] Refactor DB retry logic [resources:test-db,foo]
- [x] {id:c11d} Add loading state (done 2026-09-11)

## Deferred
- [~] {id:e5f3} Rewrite CSS system (deferred: waiting on design review)
`
	tasks := ParseFile(src)
	if len(tasks) != 4 {
		t.Fatalf("want 4 tasks, got %d", len(tasks))
	}

	// Task 1: has id, pending, high priority, body captured
	if got := tasks[0]; got.ID != "a3f1" || got.Status != StatusPending ||
		got.Title != "Wire up auth callback" || got.Priority != 5 ||
		got.Category != "Auth" {
		t.Fatalf("task[0] wrong: %+v", got)
	}
	if !strings.Contains(tasks[0].Body, "Details as sub-bullet.") {
		t.Fatalf("task[0] body missing details: %q", tasks[0].Body)
	}
	if !strings.Contains(tasks[0].Body, "- More detail.") {
		t.Fatalf("task[0] body missing sub-bullet: %q", tasks[0].Body)
	}

	// Task 2: no id (will be minted), resources list captured
	if got := tasks[1]; got.ID != "" || got.Title != "Refactor DB retry logic" ||
		len(got.RequiredResources) != 2 || got.RequiredResources[0] != "test-db" ||
		got.RequiredResources[1] != "foo" {
		t.Fatalf("task[1] wrong: %+v", got)
	}

	// Task 3: done, has completion date
	if got := tasks[2]; got.ID != "c11d" || got.Status != StatusDone ||
		got.CompletedAt != "2026-09-11" || got.Title != "Add loading state" {
		t.Fatalf("task[2] wrong: %+v", got)
	}

	// Task 4: deferred, has reason, category Deferred
	if got := tasks[3]; got.ID != "e5f3" || got.Status != StatusDeferred ||
		got.DeferredReason != "waiting on design review" || got.Category != "Deferred" ||
		got.Title != "Rewrite CSS system" {
		t.Fatalf("task[3] wrong: %+v", got)
	}
}

func TestAssignIDsAndRewrite(t *testing.T) {
	src := `## X
- [ ] {id:aaaa} first
- [ ] second
- [ ] third
`
	tasks := ParseFile(src)
	minted := []string{"1234", "5678"}
	i := 0
	mint := func() (string, error) {
		id := minted[i]
		i++
		return id, nil
	}
	_, edits, err := AssignIDs(tasks, mint)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 2 {
		t.Fatalf("want 2 edits, got %d", len(edits))
	}
	out := Rewrite(src, edits)
	want := `## X
- [ ] {id:aaaa} first
- [ ] {id:1234} second
- [ ] {id:5678} third
`
	if out != want {
		t.Fatalf("Rewrite mismatch:\n---got---\n%s\n---want---\n%s", out, want)
	}
}

func TestRewritePreservesUnchangedLines(t *testing.T) {
	src := `# H
plain para

- [ ] {id:aaaa} a
  body 1
  body 2

leftover
`
	// Round-trip with no edits should return original bytes exactly.
	if out := Rewrite(src, nil); out != src {
		t.Fatalf("empty edits altered content:\n---got---\n%s---src---\n%s", out, src)
	}
}

func TestMarkDone(t *testing.T) {
	edit, err := MarkDone("- [ ] {id:aa} foo bar", "2026-09-12")
	if err != nil {
		t.Fatal(err)
	}
	want := "- [x] {id:aa} foo bar (done 2026-09-12)"
	if edit.New != want {
		t.Fatalf("MarkDone: got %q want %q", edit.New, want)
	}
}

func TestMarkDoneIdempotent(t *testing.T) {
	line := "- [x] {id:aa} foo (done 2026-01-01)"
	edit, err := MarkDone(line, "2026-09-12")
	if err != nil {
		t.Fatal(err)
	}
	// checkbox already 'x', already has "(done ...)": should not duplicate
	if strings.Count(edit.New, "(done") != 1 {
		t.Fatalf("MarkDone duplicated the done suffix: %q", edit.New)
	}
}

func TestIndentedCheckboxIsBodyNotTask(t *testing.T) {
	src := `- [ ] outer
  - [ ] this is body, not a task
- [ ] next
`
	tasks := ParseFile(src)
	if len(tasks) != 2 {
		t.Fatalf("want 2 tasks (outer, next); got %d: %+v", len(tasks), tasks)
	}
	if !strings.Contains(tasks[0].Body, "- [ ] this is body") {
		t.Fatalf("indented checkbox missing from body: %q", tasks[0].Body)
	}
}

func TestLineHashStable(t *testing.T) {
	a := LineHash("- [ ] foo")
	b := LineHash("- [ ] foo")
	if a != b {
		t.Fatalf("LineHash unstable: %s vs %s", a, b)
	}
	if len(a) != 16 { // hex of 8 bytes
		t.Fatalf("LineHash length: want 16, got %d (%s)", len(a), a)
	}
}

// Sanity: mint failure propagates.
func TestAssignIDsMintError(t *testing.T) {
	src := "- [ ] foo\n"
	tasks := ParseFile(src)
	_, _, err := AssignIDs(tasks, func() (string, error) { return "", fmt.Errorf("boom") })
	if err == nil {
		t.Fatal("expected error from mint")
	}
}
