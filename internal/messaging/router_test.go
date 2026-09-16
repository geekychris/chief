package messaging

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// recordingBackend captures every Send call for assertions.
type recordingBackend struct {
	name string
	mu   sync.Mutex
	got  []Message
	err  error // returned by Send when set
}

func (r *recordingBackend) Name() string { return r.name }
func (r *recordingBackend) Send(_ context.Context, m Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, m)
	return r.err
}
func (r *recordingBackend) msgs() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Message, len(r.got))
	copy(out, r.got)
	return out
}

func TestRouter_DispatchesToPerUrgencyBackends(t *testing.T) {
	a := &recordingBackend{name: "a"}
	b := &recordingBackend{name: "b"}
	c := &recordingBackend{name: "c"}
	r := &Router{}
	r.Register(a)
	r.Register(b)
	r.Register(c)
	r.GlobalRules = Rules{
		Default: []string{"a"},
		PerUrgency: map[Urgency][]string{
			UrgencyUrgent: {"a", "b", "c"},
			UrgencyInfo:   {},
		},
	}
	// urgent → all three
	_ = r.Dispatch(context.Background(), Message{Urgency: UrgencyUrgent, Body: "1"})
	// attention → falls back to Default (a only)
	_ = r.Dispatch(context.Background(), Message{Urgency: UrgencyAttention, Body: "2"})
	// info → PerUrgency has an empty slice, meaning "opt out of info"
	_ = r.Dispatch(context.Background(), Message{Urgency: UrgencyInfo, Body: "3"})

	if len(a.msgs()) != 2 {
		t.Errorf("a: want 2 msgs (urgent + attention), got %d", len(a.msgs()))
	}
	if len(b.msgs()) != 1 {
		t.Errorf("b: want 1 msg (urgent only), got %d", len(b.msgs()))
	}
	if len(c.msgs()) != 1 {
		t.Errorf("c: want 1 msg (urgent only), got %d", len(c.msgs()))
	}
}

func TestRouter_PerProjectOverride(t *testing.T) {
	a := &recordingBackend{name: "a"}
	b := &recordingBackend{name: "b"}
	r := &Router{}
	r.Register(a)
	r.Register(b)
	r.GlobalRules = Rules{Default: []string{"a"}}
	// Only project "p2" also fans out to b.
	r.PerProjectRules = func(pid string) Rules {
		if pid == "p2" {
			return Rules{Default: []string{"a", "b"}}
		}
		return Rules{}
	}
	_ = r.Dispatch(context.Background(), Message{ProjectID: "p1", Urgency: UrgencyAttention, Body: "x"})
	_ = r.Dispatch(context.Background(), Message{ProjectID: "p2", Urgency: UrgencyAttention, Body: "y"})

	if len(a.msgs()) != 2 {
		t.Errorf("a: want 2 (both projects), got %d", len(a.msgs()))
	}
	if len(b.msgs()) != 1 {
		t.Errorf("b: want 1 (p2 only), got %d", len(b.msgs()))
	}
}

func TestRouter_DisabledProjectFiresNothing(t *testing.T) {
	a := &recordingBackend{name: "a"}
	r := &Router{}
	r.Register(a)
	r.GlobalRules = Rules{Default: []string{"a"}}
	r.PerProjectRules = func(pid string) Rules {
		if pid == "quiet" {
			return Rules{Disable: true}
		}
		return Rules{}
	}
	_ = r.Dispatch(context.Background(), Message{ProjectID: "quiet", Urgency: UrgencyUrgent, Body: "shush"})
	if got := len(a.msgs()); got != 0 {
		t.Errorf("disabled project should suppress dispatch; got %d msgs", got)
	}
	_ = r.Dispatch(context.Background(), Message{ProjectID: "loud", Urgency: UrgencyUrgent, Body: "hey"})
	if got := len(a.msgs()); got != 1 {
		t.Errorf("non-disabled project should still deliver; got %d msgs", got)
	}
}

func TestRouter_UnknownBackendReported(t *testing.T) {
	r := &Router{GlobalRules: Rules{Default: []string{"ghost"}}}
	results := r.Dispatch(context.Background(), Message{Urgency: UrgencyAttention})
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].Skipped != "unknown" {
		t.Errorf("unknown backend: want Skipped=unknown, got %q", results[0].Skipped)
	}
}

func TestRouter_BackendErrorDoesNotAbortOthers(t *testing.T) {
	bad := &recordingBackend{name: "bad", err: errors.New("boom")}
	good := &recordingBackend{name: "good"}
	r := &Router{GlobalRules: Rules{Default: []string{"bad", "good"}}}
	r.Register(bad)
	r.Register(good)
	results := r.Dispatch(context.Background(), Message{Urgency: UrgencyAttention, Body: "keep going"})
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	if results[0].Error == nil {
		t.Errorf("bad backend: want error propagated in result, got nil")
	}
	if len(good.msgs()) != 1 {
		t.Errorf("good backend: should have delivered despite peer failure, got %d msgs", len(good.msgs()))
	}
}

func TestLogBackend_AppendsJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "messages.jsonl")
	b := &LogBackend{NameStr: "l", Path: path}
	_ = b.Send(context.Background(), Message{Urgency: UrgencyUrgent, Title: "T", Body: "one"})
	_ = b.Send(context.Background(), Message{Urgency: UrgencyInfo, Title: "T2", Body: "two"})
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var lines []map[string]any
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Errorf("bad JSON line: %v (line=%q)", err, sc.Text())
			continue
		}
		lines = append(lines, m)
	}
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
	if lines[0]["urgency"] != "urgent" || lines[1]["urgency"] != "info" {
		t.Errorf("line urgencies wrong: %v %v", lines[0]["urgency"], lines[1]["urgency"])
	}
}
