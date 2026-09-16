package constitution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadRules_MissingFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	r, ok, err := LoadRules(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected ok=false when lint.yaml missing")
	}
	if len(r.Forbidden) != 0 {
		t.Errorf("expected empty rules, got %d", len(r.Forbidden))
	}
}

func TestLoadRules_ParsesForbiddenList(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".chief"), 0o755)
	yaml := `disable: false
sweep_hours: 12
forbidden:
  - regex: 'curl'
    reason: no direct HTTP
  - regex: 'rm -rf'
    reason: no aggressive recursive deletes
`
	_ = os.WriteFile(filepath.Join(dir, ".chief", "lint.yaml"), []byte(yaml), 0o644)
	r, ok, err := LoadRules(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected ok=true")
	}
	if r.SweepHours != 12 {
		t.Errorf("SweepHours: got %d want 12", r.SweepHours)
	}
	if len(r.Forbidden) != 2 {
		t.Errorf("Forbidden: got %d want 2", len(r.Forbidden))
	}
}

func TestCompile_SkipsInvalidRegex(t *testing.T) {
	r := Rules{Forbidden: []ForbiddenRule{
		{Regex: `[unclosed`, Reason: "bad"},
		{Regex: `valid`, Reason: "ok"},
	}}
	c, errs := Compile(r)
	if len(errs) != 1 {
		t.Errorf("errs: got %d want 1", len(errs))
	}
	if len(c.Patterns) != 1 {
		t.Errorf("patterns: got %d want 1 (valid only)", len(c.Patterns))
	}
}

// TestScanSessions_FindsBashToolUseHits — writes a synthetic session
// jsonl containing a mix of message types and confirms that only the
// Bash tool_use inputs matching a forbidden pattern surface as
// violations.
func TestScanSessions_FindsBashToolUseHits(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "abc123.jsonl")

	// One user message (should be skipped).
	// Two assistant messages with Bash tool_use — one violates.
	// One assistant message with a non-Bash tool (should be skipped).
	lines := []map[string]any{
		{
			"type": "user",
			"message": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "hi"}},
			},
			"timestamp": "2026-09-15T12:00:00Z",
		},
		{
			"type":      "assistant",
			"timestamp": "2026-09-15T12:01:00Z",
			"message": map[string]any{
				"content": []map[string]any{
					{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "curl -sSL https://x.example"}},
				},
			},
		},
		{
			"type":      "assistant",
			"timestamp": "2026-09-15T12:02:00Z",
			"message": map[string]any{
				"content": []map[string]any{
					{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "ls -la"}},
				},
			},
		},
		{
			"type":      "assistant",
			"timestamp": "2026-09-15T12:03:00Z",
			"message": map[string]any{
				"content": []map[string]any{
					{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": "/etc/hosts"}},
				},
			},
		},
	}
	f, _ := os.Create(sessionFile)
	enc := json.NewEncoder(f)
	for _, l := range lines {
		_ = enc.Encode(l)
	}
	f.Close()

	c, errs := Compile(Rules{Forbidden: []ForbiddenRule{
		{Regex: `\bcurl\b`, Reason: "no direct HTTP"},
	}})
	if len(errs) != 0 {
		t.Fatalf("compile errs: %v", errs)
	}
	vs, err := ScanSessions([]string{sessionFile}, time.Time{}, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("want 1 violation, got %d", len(vs))
	}
	if vs[0].SessionID != "abc123" {
		t.Errorf("session id: got %q", vs[0].SessionID)
	}
	if vs[0].Reason != "no direct HTTP" {
		t.Errorf("reason: got %q", vs[0].Reason)
	}
	if vs[0].Command != "curl -sSL https://x.example" {
		t.Errorf("command: got %q", vs[0].Command)
	}
}

// TestScanSessions_SinceCutoffSkipsOlder — sessions with entries older
// than `since` are ignored.
func TestScanSessions_SinceCutoffSkipsOlder(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "x.jsonl")
	lines := []map[string]any{
		{
			"type":      "assistant",
			"timestamp": "2020-01-01T00:00:00Z",
			"message": map[string]any{
				"content": []map[string]any{
					{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "curl old"}},
				},
			},
		},
	}
	f, _ := os.Create(sessionFile)
	enc := json.NewEncoder(f)
	for _, l := range lines {
		_ = enc.Encode(l)
	}
	f.Close()
	// Set the file's mtime so the outer since-cutoff triggers.
	oldT := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = os.Chtimes(sessionFile, oldT, oldT)
	c, _ := Compile(Rules{Forbidden: []ForbiddenRule{{Regex: "curl", Reason: "x"}}})
	since := time.Now().Add(-1 * time.Hour)
	vs, _ := ScanSessions([]string{sessionFile}, since, c)
	if len(vs) != 0 {
		t.Errorf("want 0 (old file skipped), got %d", len(vs))
	}
}
