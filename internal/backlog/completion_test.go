package backlog

import (
	"strings"
	"testing"
)

func TestPrependCompletionFromEmpty(t *testing.T) {
	out := PrependCompletion("", CompletionInput{
		ID: "aa", Title: "first thing", Priority: 3, Category: "Auth",
		DoneAt: "2026-09-15 15:02",
	})
	if !strings.HasPrefix(out, "# Completed") {
		t.Fatalf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "- [x] {id:aa} first thing [priority:3] [category:Auth] (done 2026-09-15 15:02)") {
		t.Fatalf("line missing:\n%s", out)
	}
}

func TestPrependIsNewestFirst(t *testing.T) {
	c := PrependCompletion("", CompletionInput{ID: "aa", Title: "older", DoneAt: "2026-09-14 09:00"})
	c = PrependCompletion(c, CompletionInput{ID: "bb", Title: "newer", DoneAt: "2026-09-15 15:02"})
	// newer must appear first
	if strings.Index(c, "{id:bb}") > strings.Index(c, "{id:aa}") {
		t.Fatalf("newest-first not preserved:\n%s", c)
	}
}

func TestPrependDedupes(t *testing.T) {
	c := PrependCompletion("", CompletionInput{ID: "aa", Title: "once", DoneAt: "2026-09-15 15:02"})
	c2 := PrependCompletion(c, CompletionInput{ID: "aa", Title: "once", DoneAt: "2026-09-15 15:03"})
	if c != c2 {
		t.Fatalf("duplicate id was appended anyway:\n%s", c2)
	}
}

func TestRemoveTasksByIDsRemovesBodyToo(t *testing.T) {
	src := `## Backlog

- [ ] {id:aa} first
  body line for aa
  - indented sub
- [ ] {id:bb} second
- [x] {id:cc} done thing (done 2026-09-15)
  body for cc
`
	tasks := ParseFile(src)
	out := RemoveTasksByIDs(src, tasks, map[string]bool{"cc": true})
	if strings.Contains(out, "{id:cc}") {
		t.Fatalf("cc still present:\n%s", out)
	}
	if strings.Contains(out, "body for cc") {
		t.Fatalf("cc body still present:\n%s", out)
	}
	if !strings.Contains(out, "{id:aa}") || !strings.Contains(out, "body line for aa") {
		t.Fatalf("aa should still be present:\n%s", out)
	}
	if !strings.Contains(out, "{id:bb}") {
		t.Fatalf("bb should still be present:\n%s", out)
	}
}

func TestCategoryTagRoundTrip(t *testing.T) {
	src := "- [x] {id:aa} done thing [priority:5] [category:Auth] (done 2026-09-15 15:02)\n"
	tasks := ParseFile(src)
	if len(tasks) != 1 {
		t.Fatalf("want 1 task, got %d", len(tasks))
	}
	if tasks[0].Category != "Auth" {
		t.Fatalf("category not read from tag: %+v", tasks[0])
	}
	if tasks[0].Priority != 5 {
		t.Fatalf("priority not read: %+v", tasks[0])
	}
	if tasks[0].Status != StatusDone {
		t.Fatalf("status: %v", tasks[0].Status)
	}
}
