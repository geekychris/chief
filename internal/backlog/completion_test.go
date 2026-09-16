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

func TestPrependCompletionExtendedRoundTrip(t *testing.T) {
	out := PrependCompletion("", CompletionInput{
		ID: "e34e", Title: "when adding to completed include full details",
		Category: "Backlog", DoneAt: "2026-09-16 17:45:00",
		CreatedAt: "2026-09-14 10:15:30", StartedAt: "2026-09-15 09:00:00",
		Duration: "1d 8h",
		Body:     "  - detail one\n  - detail two",
	})
	// Header line stays single-line + carries done-at.
	if !strings.Contains(out, "- [x] {id:e34e} when adding to completed include full details [category:Backlog] (done 2026-09-16 17:45:00)") {
		t.Fatalf("header line missing:\n%s", out)
	}
	// Meta line rendered as indented sub-bullet.
	if !strings.Contains(out, "  - added: 2026-09-14 10:15:30 · started: 2026-09-15 09:00:00 · duration: 1d 8h") {
		t.Fatalf("meta line missing:\n%s", out)
	}
	// Body sub-bullets preserved verbatim.
	if !strings.Contains(out, "  - detail one") || !strings.Contains(out, "  - detail two") {
		t.Fatalf("body not preserved:\n%s", out)
	}
	// Round-trip: re-parsing must still yield exactly one Task with the
	// merged body (meta + originals). ParseFile treats every indented
	// line as body — that's the point.
	tasks := ParseFile(out)
	if len(tasks) != 1 {
		t.Fatalf("want 1 task after round-trip, got %d", len(tasks))
	}
	if !strings.Contains(tasks[0].Body, "detail one") {
		t.Fatalf("body lost on re-parse: %q", tasks[0].Body)
	}
	if tasks[0].Status != StatusDone || tasks[0].Category != "Backlog" {
		t.Fatalf("header data lost: %+v", tasks[0])
	}
}

func TestPrependCompletionLegacyFormWhenNoExtras(t *testing.T) {
	// When Body/CreatedAt/StartedAt/Duration are all zero, the entry
	// stays single-line — preserves the historical shape so old data
	// doesn't get "upgraded" against user preference.
	out := PrependCompletion("", CompletionInput{
		ID: "aa", Title: "legacy", DoneAt: "2026-09-15 15:02",
	})
	if strings.Contains(out, "\n  - ") {
		t.Fatalf("legacy entry should stay single-line:\n%s", out)
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
