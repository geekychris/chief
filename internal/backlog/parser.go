// Package backlog parses and round-trip-writes the human-authored backlog
// markdown format used by Chief.
//
// Format (see plan/bubbly-dreaming-spring.md → "File format: backlog.md"):
//
//   # Backlog
//
//   ## Auth
//   - [ ] {id:a3f1} Wire up auth callback [priority:high]
//     Indented sub-bullets become task.body.
//     - Multiple lines are fine.
//   - [x] {id:c11d} Add loading state (done 2026-09-11)
//
//   ## Deferred
//   - [~] {id:e5f3} Rewrite CSS system (deferred: waiting on design review)
//
// The parser preserves everything Chief does not directly touch. When Chief
// writes back (to mint an id, flip a checkbox, stamp a completion date), it
// only rewrites the specific lines that changed — the rest of the file is
// passed through byte-for-byte.
package backlog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Status is the parsed checkbox state.
type Status string

const (
	StatusPending  Status = "pending"
	StatusDone     Status = "done"
	StatusDeferred Status = "deferred"
)

// Task is one parsed checkbox item.
type Task struct {
	ID                string
	Status            Status
	Title             string
	Body              string
	Category          string
	Priority          int
	RequiredResources []string
	Due               string
	CompletedAt       string
	DeferredReason    string
	LineNum           int    // 1-based
	RawLine           string // original checkbox line
}

// LineEdit describes a single line replacement in the original file.
type LineEdit struct {
	LineNum int    // 1-based
	New     string
}

// checkboxRE matches "- [ ] rest", "- [x] rest", "- [X] rest", "- [~] rest".
// Leading whitespace is captured so we can distinguish top-level checkboxes
// from indented sub-bullets (only column-0 checkboxes are tasks).
var checkboxRE = regexp.MustCompile(`^(\s*)-\s+\[([ xX~])\]\s+(.*)$`)

// idRE matches a leading "{id:HEX}" prefix on the payload text.
var idRE = regexp.MustCompile(`^\{id:([a-fA-F0-9]+)\}\s*`)

// tagRE matches "[key:value]" bracket tags anywhere in the payload.
var tagRE = regexp.MustCompile(`\[([a-z_]+):([^\]]+)\]`)

// doneParenRE matches "(done ...)" suffix on completed tasks.
var doneParenRE = regexp.MustCompile(`\(done\s+([^)]+)\)`)

// deferredParenRE matches "(deferred: ...)" suffix on deferred tasks.
var deferredParenRE = regexp.MustCompile(`\(deferred:\s*([^)]+)\)`)

// headerRE matches Markdown H1/H2/H3 headers. We take the deepest as Category.
var headerRE = regexp.MustCompile(`^(#{1,3})\s+(.*)$`)

// ParseFile parses backlog markdown into an ordered list of tasks.
// Only column-0 checkboxes count as tasks; indented lines beneath one are
// captured as its body.
func ParseFile(content string) []Task {
	lines := strings.Split(content, "\n")
	var (
		tasks    []Task
		category string
	)
	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// Track category via headers. H2 is the intended anchor; H1 acts as
		// a fallback if there are no H2s (e.g., simple flat backlogs).
		if m := headerRE.FindStringSubmatch(line); m != nil {
			hLevel := len(m[1])
			text := strings.TrimSpace(m[2])
			if hLevel == 1 && category == "" {
				category = text
			} else if hLevel >= 2 {
				category = text
			}
			continue
		}

		m := checkboxRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		indent := m[1]
		if indent != "" {
			continue // indented checkbox is body, not a task line
		}
		checkbox := m[2]
		payload := m[3]

		t := Task{
			LineNum:  i + 1,
			RawLine:  line,
			Category: category,
			Status:   statusFromCheckbox(checkbox),
		}
		parsePayload(&t, payload)

		// Collect body: subsequent indented (non-empty-or-whitespace) lines
		// until the next column-0 checkbox or header.
		var bodyLines []string
		for j := i + 1; j < len(lines); j++ {
			next := lines[j]
			if strings.TrimSpace(next) == "" {
				// Blank line ends body only if the following line isn't
				// still-indented content. Peek ahead.
				if !isBodyContinuation(lines, j+1) {
					break
				}
				bodyLines = append(bodyLines, next)
				continue
			}
			if headerRE.MatchString(next) {
				break
			}
			mm := checkboxRE.FindStringSubmatch(next)
			if mm != nil && mm[1] == "" {
				break // next task
			}
			// Any other indented line (including indented sub-checkboxes) is body.
			if !strings.HasPrefix(next, " ") && !strings.HasPrefix(next, "\t") {
				break
			}
			bodyLines = append(bodyLines, next)
		}
		if len(bodyLines) > 0 {
			// Trim trailing blank lines from body.
			for len(bodyLines) > 0 && strings.TrimSpace(bodyLines[len(bodyLines)-1]) == "" {
				bodyLines = bodyLines[:len(bodyLines)-1]
			}
			t.Body = strings.Join(bodyLines, "\n")
		}
		tasks = append(tasks, t)
	}
	return tasks
}

func isBodyContinuation(lines []string, from int) bool {
	for j := from; j < len(lines); j++ {
		s := lines[j]
		if strings.TrimSpace(s) == "" {
			continue
		}
		if headerRE.MatchString(s) {
			return false
		}
		if mm := checkboxRE.FindStringSubmatch(s); mm != nil && mm[1] == "" {
			return false
		}
		return strings.HasPrefix(s, " ") || strings.HasPrefix(s, "\t")
	}
	return false
}

func statusFromCheckbox(c string) Status {
	switch c {
	case "x", "X":
		return StatusDone
	case "~":
		return StatusDeferred
	default:
		return StatusPending
	}
}

// parsePayload extracts id, tags, and completion/deferral parenthesized
// suffixes from the text after "- [S] ". The remaining text is Title.
func parsePayload(t *Task, payload string) {
	// Strip {id:XXX} prefix if present.
	if m := idRE.FindStringSubmatchIndex(payload); m != nil {
		t.ID = strings.ToLower(payload[m[2]:m[3]])
		payload = payload[m[1]:]
	}

	// Strip (done ...) / (deferred: ...) suffixes based on status.
	if t.Status == StatusDone {
		if m := doneParenRE.FindStringSubmatchIndex(payload); m != nil {
			t.CompletedAt = strings.TrimSpace(payload[m[2]:m[3]])
			payload = strings.TrimRight(payload[:m[0]]+payload[m[1]:], " ")
		}
	}
	if t.Status == StatusDeferred {
		if m := deferredParenRE.FindStringSubmatchIndex(payload); m != nil {
			t.DeferredReason = strings.TrimSpace(payload[m[2]:m[3]])
			payload = strings.TrimRight(payload[:m[0]]+payload[m[1]:], " ")
		}
	}

	// Extract every [key:value] tag; keep the title with tags removed.
	titleBuf := payload
	for _, m := range tagRE.FindAllStringSubmatchIndex(payload, -1) {
		key := payload[m[2]:m[3]]
		val := strings.TrimSpace(payload[m[4]:m[5]])
		applyTag(t, key, val)
	}
	// Remove tag substrings from the title, in reverse order to keep indices valid.
	locs := tagRE.FindAllStringIndex(titleBuf, -1)
	for i := len(locs) - 1; i >= 0; i-- {
		l := locs[i]
		titleBuf = titleBuf[:l[0]] + titleBuf[l[1]:]
	}
	t.Title = strings.TrimSpace(collapseSpaces(titleBuf))
}

func applyTag(t *Task, key, val string) {
	switch key {
	case "priority":
		t.Priority = priorityFromValue(val)
	case "resources":
		for _, s := range strings.Split(val, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				t.RequiredResources = append(t.RequiredResources, s)
			}
		}
	case "due":
		t.Due = val
	case "category", "cat":
		// [category:X] tag lets one-line completedlog entries preserve their
		// original section without needing a per-day header. If both a tag
		// and a real ## header would apply, whichever is set last wins;
		// parsePayload runs after category was assigned from the header, so
		// the tag takes precedence — which is what we want for completedlog.
		t.Category = val
	}
}

// priorityFromValue accepts "0-9" or aliases "low"/"med"/"medium"/"high".
func priorityFromValue(v string) int {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "low":
		return -1
	case "med", "medium":
		return 0
	case "high":
		return 5
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		return n
	}
	return 0
}

func collapseSpaces(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !prevSpace {
				b.WriteRune(' ')
				prevSpace = true
			}
		} else {
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return b.String()
}

// LineHash returns a stable short hash of a raw checkbox line, used as the
// tasks.source_line_hash anchor when a task has no id yet.
func LineHash(rawLine string) string {
	sum := sha256.Sum256([]byte(rawLine))
	return hex.EncodeToString(sum[:8])
}

// AssignIDs mints an id for every task in `tasks` whose ID is empty, and
// returns the set of edits that need to be applied to the original file to
// persist the id annotation. The mint function is responsible for uniqueness
// (typically: NewTaskID + collision-check against SQLite; retry up to N times).
func AssignIDs(tasks []Task, mint func() (string, error)) ([]Task, []LineEdit, error) {
	var edits []LineEdit
	for i := range tasks {
		if tasks[i].ID != "" {
			continue
		}
		id, err := mint()
		if err != nil {
			return nil, nil, fmt.Errorf("mint id for line %d: %w", tasks[i].LineNum, err)
		}
		tasks[i].ID = id
		newLine, err := injectID(tasks[i].RawLine, id)
		if err != nil {
			return nil, nil, err
		}
		tasks[i].RawLine = newLine
		edits = append(edits, LineEdit{LineNum: tasks[i].LineNum, New: newLine})
	}
	return tasks, edits, nil
}

// injectID rewrites a "- [S] payload" line to "- [S] {id:XXXX} payload".
// Safe to call only when the line has no existing {id:...} prefix.
func injectID(rawLine, id string) (string, error) {
	m := checkboxRE.FindStringSubmatchIndex(rawLine)
	if m == nil {
		return "", fmt.Errorf("injectID: line does not match checkbox regex: %q", rawLine)
	}
	// The payload starts at m[6].
	prefix := rawLine[:m[6]]
	payload := rawLine[m[6]:]
	return prefix + "{id:" + id + "} " + payload, nil
}

// Rewrite applies line edits to original content, returning the new content.
// Preserves line separator style (does not force \n if the file had \r\n) and
// preserves a trailing newline if the original had one.
func Rewrite(original string, edits []LineEdit) string {
	if len(edits) == 0 {
		return original
	}
	// Determine line separator: prefer \r\n if any line ends with it.
	sep := "\n"
	if strings.Contains(original, "\r\n") {
		sep = "\r\n"
	}
	lines := strings.Split(strings.TrimRight(original, "\r\n"), sep)
	hadTrailingNewline := len(original) > 0 && (original[len(original)-1] == '\n')

	edited := make(map[int]string, len(edits))
	for _, e := range edits {
		edited[e.LineNum-1] = e.New
	}
	for i := range lines {
		if v, ok := edited[i]; ok {
			lines[i] = v
		}
	}
	out := strings.Join(lines, sep)
	if hadTrailingNewline {
		out += sep
	}
	return out
}

// MarkDone returns a new LineEdit that flips a pending checkbox to done and
// appends "(done YYYY-MM-DD)" if not already present. Idempotent.
func MarkDone(rawLine string, dateISO string) (LineEdit, error) {
	m := checkboxRE.FindStringSubmatchIndex(rawLine)
	if m == nil {
		return LineEdit{}, fmt.Errorf("MarkDone: line does not match checkbox regex: %q", rawLine)
	}
	// Replace the checkbox char (at m[4]..m[5]) with 'x'.
	newLine := rawLine[:m[4]] + "x" + rawLine[m[5]:]
	if !strings.Contains(newLine, "(done ") {
		newLine = strings.TrimRight(newLine, " ") + " (done " + dateISO + ")"
	}
	return LineEdit{New: newLine}, nil
}

// MarkDeferred returns a LineEdit flipping a pending checkbox to deferred with
// "(deferred: reason)".
func MarkDeferred(rawLine, reason string) (LineEdit, error) {
	m := checkboxRE.FindStringSubmatchIndex(rawLine)
	if m == nil {
		return LineEdit{}, fmt.Errorf("MarkDeferred: line does not match checkbox regex: %q", rawLine)
	}
	newLine := rawLine[:m[4]] + "~" + rawLine[m[5]:]
	if !strings.Contains(newLine, "(deferred:") {
		newLine = strings.TrimRight(newLine, " ") + " (deferred: " + reason + ")"
	}
	return LineEdit{New: newLine}, nil
}
