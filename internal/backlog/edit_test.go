package backlog

import (
	"strings"
	"testing"
)

func TestUpdateTaskRewritesLinePreservingBody(t *testing.T) {
	src := `## Backlog

- [ ] {id:aa} Old title [priority:1]
  body line 1
  body line 2
- [ ] {id:bb} Second task
`
	tasks := ParseFile(src)
	out, err := UpdateTask(src, tasks, UpdateTaskInput{
		ID: "aa", Title: "New title", Priority: 5, Category: "Auth",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Line for aa must have the new title, priority, and category tag.
	if !strings.Contains(out, "- [ ] {id:aa} New title [priority:5] [category:Auth]") {
		t.Fatalf("checkbox line not rewritten as expected:\n%s", out)
	}
	// Body must be preserved.
	if !strings.Contains(out, "body line 1") || !strings.Contains(out, "body line 2") {
		t.Fatalf("body lost:\n%s", out)
	}
	// bb must be untouched.
	if !strings.Contains(out, "- [ ] {id:bb} Second task") {
		t.Fatalf("second task damaged:\n%s", out)
	}
}

func TestUpdateTaskReplacesBodyWhenProvided(t *testing.T) {
	src := `- [ ] {id:aa} T
  old body
`
	tasks := ParseFile(src)
	newBody := "new body\nsecond line"
	out, err := UpdateTask(src, tasks, UpdateTaskInput{ID: "aa", Title: "T", Body: &newBody})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "old body") {
		t.Fatalf("old body still present:\n%s", out)
	}
	if !strings.Contains(out, "  new body") || !strings.Contains(out, "  second line") {
		t.Fatalf("new body not indented as sub-bullets:\n%s", out)
	}
}

func TestUpdateTaskEmptyBodyClearsBody(t *testing.T) {
	src := `- [ ] {id:aa} T
  body to remove
`
	tasks := ParseFile(src)
	empty := ""
	out, err := UpdateTask(src, tasks, UpdateTaskInput{ID: "aa", Title: "T", Body: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "body to remove") {
		t.Fatalf("body should be cleared:\n%s", out)
	}
}

func TestUpdateTaskPreservesDeferredCheckbox(t *testing.T) {
	src := "- [~] {id:aa} old title (deferred: waiting)\n"
	tasks := ParseFile(src)
	out, err := UpdateTask(src, tasks, UpdateTaskInput{ID: "aa", Title: "new title"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "- [~] {id:aa} new title") {
		t.Fatalf("deferred checkbox not preserved:\n%s", out)
	}
}

func TestMoveTaskDown(t *testing.T) {
	src := `## Backlog

- [ ] {id:aa} first
- [ ] {id:bb} second
- [ ] {id:cc} third
`
	tasks := ParseFile(src)
	out, err := MoveTask(src, tasks, "aa", "down")
	if err != nil {
		t.Fatal(err)
	}
	// After swap: bb, aa, cc
	parsed := ParseFile(out)
	if len(parsed) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(parsed))
	}
	if parsed[0].ID != "bb" || parsed[1].ID != "aa" || parsed[2].ID != "cc" {
		t.Fatalf("wrong order after MoveTask down: %s, %s, %s", parsed[0].ID, parsed[1].ID, parsed[2].ID)
	}
}

func TestMoveTaskUp(t *testing.T) {
	src := `## Backlog

- [ ] {id:aa} first
- [ ] {id:bb} second
- [ ] {id:cc} third
`
	tasks := ParseFile(src)
	out, err := MoveTask(src, tasks, "cc", "up")
	if err != nil {
		t.Fatal(err)
	}
	parsed := ParseFile(out)
	// After swap: aa, cc, bb
	if parsed[0].ID != "aa" || parsed[1].ID != "cc" || parsed[2].ID != "bb" {
		t.Fatalf("wrong order after MoveTask up: %s, %s, %s", parsed[0].ID, parsed[1].ID, parsed[2].ID)
	}
}

func TestMoveTaskAtSectionEdgeIsNoop(t *testing.T) {
	src := `## Backlog
- [ ] {id:aa} first
- [ ] {id:bb} second

## Other
- [ ] {id:cc} in other section
`
	tasks := ParseFile(src)
	// aa is first in section — moving up should be a no-op (header blocks).
	out, err := MoveTask(src, tasks, "aa", "up")
	if err != nil {
		t.Fatal(err)
	}
	if out != src {
		t.Fatalf("MoveTask should be no-op at top of section, but got:\n%s", out)
	}
	// bb is last in Backlog section — moving down should NOT jump into Other.
	out2, err := MoveTask(src, tasks, "bb", "down")
	if err != nil {
		t.Fatal(err)
	}
	if out2 != src {
		t.Fatalf("MoveTask should not cross section boundary, got:\n%s", out2)
	}
}

func TestMoveTaskWithBodiesSwapsWholeBlock(t *testing.T) {
	src := `## Backlog

- [ ] {id:aa} first
  aa body
- [ ] {id:bb} second
  bb body 1
  bb body 2
`
	tasks := ParseFile(src)
	out, err := MoveTask(src, tasks, "aa", "down")
	if err != nil {
		t.Fatal(err)
	}
	// After swap, bb's body must still be with bb, and aa's body with aa.
	parsed := ParseFile(out)
	if len(parsed) != 2 {
		t.Fatalf("want 2 tasks, got %d\n---content---\n%s", len(parsed), out)
	}
	if parsed[0].ID != "bb" || parsed[1].ID != "aa" {
		t.Fatalf("wrong order:\n%s", out)
	}
	if !strings.Contains(parsed[0].Body, "bb body 1") || !strings.Contains(parsed[0].Body, "bb body 2") {
		t.Fatalf("bb body lost after move:\n%+v", parsed[0])
	}
	if !strings.Contains(parsed[1].Body, "aa body") {
		t.Fatalf("aa body lost after move:\n%+v", parsed[1])
	}
}
