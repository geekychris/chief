package backlog

import (
	"strings"
	"testing"
)

func TestAppendToEmptyFile(t *testing.T) {
	out, err := AppendToFile("", NewTaskInput{ID: "abcd", Title: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "- [ ] {id:abcd} hello") {
		t.Fatalf("append missing line:\n%s", out)
	}
	// Empty file should NOT produce a leading blank line.
	if strings.HasPrefix(out, "\n") {
		t.Fatalf("output has stray leading newline:\n%q", out)
	}
	// Should seed a `## Backlog` header by default.
	if !strings.Contains(out, "## Backlog") {
		t.Fatalf("empty file should seed ## Backlog header:\n%s", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("output should end with newline: %q", out)
	}
}

func TestAppendToEmptyFileWithCategory(t *testing.T) {
	out, err := AppendToFile("", NewTaskInput{ID: "aa", Title: "first", Category: "Ship"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "## Ship") {
		t.Fatalf("category header should be created:\n%s", out)
	}
	// Task must appear AFTER the header.
	if strings.Index(out, "{id:aa}") < strings.Index(out, "## Ship") {
		t.Fatalf("task should be under Ship section:\n%s", out)
	}
}

func TestAppendDoesNotStealPriorTasksBody(t *testing.T) {
	// Regression: a new task appended into a section that ends with a task
	// having a body used to be inserted BETWEEN the previous checkbox and
	// its body. On the next re-parse the orphaned body lines got attached
	// to the new task. Fixed by inserting after the last task's full block.
	src := `## Backlog

- [ ] {id:aa} first task
  body of first task line 1
  body of first task line 2
`
	out, err := AppendToFile(src, NewTaskInput{ID: "bb", Title: "second task", Category: "Backlog"})
	if err != nil {
		t.Fatal(err)
	}
	// After the append, the parser must see two distinct tasks, and the
	// first task's body must still belong to aa (not to bb).
	tasks := ParseFile(out)
	if len(tasks) != 2 {
		t.Fatalf("want 2 tasks, got %d\n---content---\n%s", len(tasks), out)
	}
	if tasks[0].ID != "aa" {
		t.Fatalf("task order changed; first is now %s", tasks[0].ID)
	}
	if !strings.Contains(tasks[0].Body, "body of first task line 1") {
		t.Fatalf("first task lost its body:\nfirst=%+v\nsecond=%+v\n---content---\n%s", tasks[0], tasks[1], out)
	}
	if tasks[1].ID != "bb" {
		t.Fatalf("second task is %s, want bb", tasks[1].ID)
	}
	if tasks[1].Body != "" {
		t.Fatalf("second task should have empty body, got %q\n---content---\n%s", tasks[1].Body, out)
	}
}

func TestAppendCreatesMissingSectionOnExistingFile(t *testing.T) {
	src := "## Auth\n- [ ] {id:aa} auth thing\n"
	out, err := AppendToFile(src, NewTaskInput{ID: "bb", Title: "new area", Category: "Backend"})
	if err != nil {
		t.Fatal(err)
	}
	// Auth section preserved, Backend section created.
	if !strings.Contains(out, "## Auth") {
		t.Fatalf("original section lost:\n%s", out)
	}
	if !strings.Contains(out, "## Backend") {
		t.Fatalf("missing section not created:\n%s", out)
	}
	if strings.Index(out, "{id:bb}") < strings.Index(out, "## Backend") {
		t.Fatalf("task not under new section:\n%s", out)
	}
}

func TestAppendUnderBacklogSection(t *testing.T) {
	src := `# Repo notes

## Backlog
- [ ] {id:aaaa} existing

## Deferred
- [~] {id:bbbb} old
`
	out, err := AppendToFile(src, NewTaskInput{ID: "cccc", Title: "new one", Priority: 5})
	if err != nil {
		t.Fatal(err)
	}
	// Must appear inside the Backlog section (before Deferred header).
	backlogIdx := strings.Index(out, "## Backlog")
	deferredIdx := strings.Index(out, "## Deferred")
	newIdx := strings.Index(out, "{id:cccc}")
	if newIdx < backlogIdx || newIdx > deferredIdx {
		t.Fatalf("new task placed outside Backlog section:\n%s", out)
	}
	if !strings.Contains(out, "[priority:5]") {
		t.Fatalf("priority tag missing:\n%s", out)
	}
}

func TestAppendUnderNamedCategory(t *testing.T) {
	src := `## Auth
- [ ] {id:aa} first

## Backend
- [ ] {id:bb} db work
`
	out, err := AppendToFile(src, NewTaskInput{ID: "cc", Title: "under Backend", Category: "Backend"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(out, "{id:cc}") < strings.Index(out, "## Backend") {
		t.Fatalf("new task not under Backend:\n%s", out)
	}
	if strings.Index(out, "{id:cc}") < strings.Index(out, "{id:bb}") {
		t.Fatalf("new task should appear AFTER existing Backend task:\n%s", out)
	}
}

func TestAppendWithBody(t *testing.T) {
	src := "## Backlog\n- [ ] {id:aa} old\n"
	out, err := AppendToFile(src, NewTaskInput{ID: "bb", Title: "new", Body: "line 1\nline 2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "  line 1") || !strings.Contains(out, "  line 2") {
		t.Fatalf("body not indented as sub-bullets:\n%s", out)
	}
}

func TestAppendRoundTripsThroughParser(t *testing.T) {
	src := "## Backlog\n- [ ] {id:aa} old\n"
	out, err := AppendToFile(src, NewTaskInput{
		ID: "bb", Title: "new", Priority: 5,
		RequiredResources: []string{"iphone_sim", "gpu"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tasks := ParseFile(out)
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks after append, got %d", len(tasks))
	}
	last := tasks[1]
	if last.ID != "bb" || last.Priority != 5 || len(last.RequiredResources) != 2 {
		t.Fatalf("appended task did not round-trip through parser: %+v", last)
	}
}
