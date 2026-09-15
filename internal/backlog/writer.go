package backlog

import (
	"fmt"
	"strings"
)

// NewTaskLine renders a fresh checkbox line for AppendToFile. Called by the
// caller after minting an id via store.NewTaskID (with a collision-check loop).
type NewTaskInput struct {
	ID                string   // required — must be minted before calling
	Title             string   // required
	Priority          int      // 0 = omit tag
	RequiredResources []string // empty = omit tag
	Due               string   // empty = omit tag
	Category          string   // "" = append at end of file (or under "## Backlog" if present)
	Body              string   // "" = no body; otherwise wrapped as indented sub-bullets
}

// AppendToFile inserts a new task into the given markdown content, respecting
// section boundaries. Returns the new file content.
//
// Placement rules:
//   - If `in.Category` is set and a `## <Category>` header exists, insert as
//     the last checkbox under that section.
//   - If `in.Category` is set and NO such header exists, append the section
//     (blank spacer + `## <Category>` + blank + `- [ ] …`) at end of file.
//   - Else if a `## Backlog` header exists, insert as the last checkbox there.
//   - Else append a `## Backlog` section (creating it) and the task under it.
//
// Fresh files get proper structure automatically; existing files are never
// reordered or reformatted.  Round-trip guarantees hold for everything
// already present.
func AppendToFile(original string, in NewTaskInput) (string, error) {
	if in.ID == "" {
		return "", fmt.Errorf("AppendToFile: ID required")
	}
	if strings.TrimSpace(in.Title) == "" {
		return "", fmt.Errorf("AppendToFile: Title required")
	}
	block := renderTaskBlock(in)

	sep := "\n"
	if strings.Contains(original, "\r\n") {
		sep = "\r\n"
	}
	// Normalise: work with an empty slice for a truly empty file rather than
	// [""], which produced a stray leading newline in the first write.
	trimmed := strings.TrimRight(original, "\r\n")
	var lines []string
	if trimmed != "" {
		lines = strings.Split(trimmed, sep)
	}

	category := in.Category
	if category == "" {
		category = "Backlog"
	}

	target := findInsertionPoint(lines, category)
	if target < 0 {
		// Section absent: append it (with the task under it) at end of file.
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "## "+category, "")
		lines = append(lines, strings.Split(block, "\n")...)
	} else {
		// Insert block after the last checkbox in that section.
		before := lines[:target+1]
		after := lines[target+1:]
		newLines := append([]string{}, before...)
		newLines = append(newLines, strings.Split(block, "\n")...)
		newLines = append(newLines, after...)
		lines = newLines
	}

	out := strings.Join(lines, sep)
	// Always end with a single trailing newline.
	out = strings.TrimRight(out, "\r\n") + sep
	return out, nil
}

// findInsertionPoint returns the line index (0-based) to insert AFTER.
// -1 means "no matching section — caller decides how to handle." If category
// is empty, "Backlog" is used as the target name.
//
// The returned index points at the LAST line of the LAST task's block
// (checkbox + its body). Inserting after that avoids the regression where
// a new checkbox lands between an earlier task's checkbox and its indented
// body, causing the parser to re-attribute the body on the next read.
func findInsertionPoint(lines []string, category string) int {
	target := strings.ToLower(strings.TrimSpace(category))
	if target == "" {
		target = "backlog"
	}
	// Locate the section header line, then find where the section ends
	// (next H1/H2/H3 or EOF).
	inSection := false
	sectionStart := -1
	sectionEnd := len(lines) - 1
	for i, line := range lines {
		if m := headerRE.FindStringSubmatch(line); m != nil {
			if inSection {
				sectionEnd = i - 1
				break
			}
			hLevel := len(m[1])
			name := strings.ToLower(strings.TrimSpace(m[2]))
			if hLevel >= 1 && name == target {
				inSection = true
				sectionStart = i
			}
		}
	}
	if !inSection {
		return -1
	}
	// Walk the section; for each column-0 checkbox, extend past its body
	// (mirroring the parser's Body-collection rule) and record the last
	// line that belongs to it.
	lastTaskEnd := sectionStart
	for i := sectionStart + 1; i <= sectionEnd && i < len(lines); i++ {
		m := checkboxRE.FindStringSubmatch(lines[i])
		if m == nil || m[1] != "" {
			continue // not a column-0 checkbox
		}
		bodyEnd := i
		for j := i + 1; j <= sectionEnd && j < len(lines); j++ {
			ln := lines[j]
			if strings.TrimSpace(ln) == "" {
				if !isBodyContinuation(lines, j+1) {
					break
				}
				bodyEnd = j
				continue
			}
			if headerRE.MatchString(ln) {
				break
			}
			if mm := checkboxRE.FindStringSubmatch(ln); mm != nil && mm[1] == "" {
				break
			}
			if !strings.HasPrefix(ln, " ") && !strings.HasPrefix(ln, "\t") {
				break
			}
			bodyEnd = j
		}
		lastTaskEnd = bodyEnd
		i = bodyEnd // fast-forward; the outer i++ moves to bodyEnd+1
	}
	return lastTaskEnd
}

// renderTaskBlock composes the multiline block for a new task.
func renderTaskBlock(in NewTaskInput) string {
	var tags []string
	if in.Priority != 0 {
		tags = append(tags, fmt.Sprintf("[priority:%d]", in.Priority))
	}
	if len(in.RequiredResources) > 0 {
		tags = append(tags, fmt.Sprintf("[resources:%s]", strings.Join(in.RequiredResources, ",")))
	}
	if in.Due != "" {
		tags = append(tags, fmt.Sprintf("[due:%s]", in.Due))
	}
	line := fmt.Sprintf("- [ ] {id:%s} %s", in.ID, strings.TrimSpace(in.Title))
	if len(tags) > 0 {
		line += " " + strings.Join(tags, " ")
	}
	if strings.TrimSpace(in.Body) == "" {
		return line
	}
	// Body: prefix every line with 2 spaces so the parser recognizes it as
	// indented body content belonging to the task.
	bodyLines := strings.Split(strings.TrimRight(in.Body, "\n"), "\n")
	for i, bl := range bodyLines {
		bodyLines[i] = "  " + bl
	}
	return line + "\n" + strings.Join(bodyLines, "\n")
}

// HasTaskID reports whether markdown content already contains a checkbox line
// with the given `{id:xxx}` annotation. Used to dedupe when moving items into
// completedlog.md — never append a duplicate.
func HasTaskID(content, id string) bool {
	if id == "" {
		return false
	}
	needle := "{id:" + strings.ToLower(id) + "}"
	for _, ln := range strings.Split(content, "\n") {
		if strings.Contains(strings.ToLower(ln), needle) {
			return true
		}
	}
	return false
}

// RemoveTasksByIDs strips the checkbox lines (and their indented body lines)
// for every task whose id is in `ids`. Preserves everything else verbatim.
// Requires the parsed task list from ParseFile so it knows each task's exact
// line range.
func RemoveTasksByIDs(content string, tasks []Task, ids map[string]bool) string {
	if len(ids) == 0 {
		return content
	}
	sep := "\n"
	if strings.Contains(content, "\r\n") {
		sep = "\r\n"
	}
	hadTrailingNewline := len(content) > 0 && content[len(content)-1] == '\n'
	lines := strings.Split(strings.TrimRight(content, "\r\n"), sep)

	remove := map[int]bool{}
	for _, t := range tasks {
		if !ids[t.ID] {
			continue
		}
		idx := t.LineNum - 1 // 1-based → 0-based
		if idx < 0 || idx >= len(lines) {
			continue
		}
		remove[idx] = true
		// Include every subsequent indented body line, mirroring the
		// parser's Body-collection rule.
		for j := idx + 1; j < len(lines); j++ {
			ln := lines[j]
			if strings.TrimSpace(ln) == "" {
				if !isBodyContinuation(lines, j+1) {
					break
				}
				remove[j] = true
				continue
			}
			if headerRE.MatchString(ln) {
				break
			}
			if m := checkboxRE.FindStringSubmatch(ln); m != nil && m[1] == "" {
				break
			}
			if !strings.HasPrefix(ln, " ") && !strings.HasPrefix(ln, "\t") {
				break
			}
			remove[j] = true
		}
	}
	out := make([]string, 0, len(lines))
	for i, ln := range lines {
		if !remove[i] {
			out = append(out, ln)
		}
	}
	collapsed := collapseBlankRuns(out)
	joined := strings.Join(collapsed, sep)
	if hadTrailingNewline {
		joined = strings.TrimRight(joined, "\r\n") + sep
	}
	return joined
}

func collapseBlankRuns(lines []string) []string {
	out := make([]string, 0, len(lines))
	blank := false
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			if blank {
				continue
			}
			blank = true
			out = append(out, ln)
		} else {
			blank = false
			out = append(out, ln)
		}
	}
	// Trim leading blanks so the file doesn't start with a bare newline.
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	return out
}

// CompletionInput is what PrependCompletion needs to render an entry.
type CompletionInput struct {
	ID       string
	Title    string
	Priority int
	Category string
	DoneAt   string // free-form; caller decides format ("2026-09-15 15:02" or RFC3339)
}

// PrependCompletion inserts a one-line entry at the top of completedlog.md,
// under a `# Completed` header (created if missing). Newest-first ordering.
// If the file already contains this task id, returns content unchanged (dedupe).
func PrependCompletion(content string, in CompletionInput) string {
	if in.ID == "" || in.Title == "" || in.DoneAt == "" {
		return content
	}
	if HasTaskID(content, in.ID) {
		return content
	}
	line := renderCompletionLine(in)
	sep := "\n"
	if strings.Contains(content, "\r\n") {
		sep = "\r\n"
	}
	// Empty file → seed header + entry.
	if strings.TrimSpace(content) == "" {
		return "# Completed" + sep + sep + line + sep
	}
	lines := strings.Split(strings.TrimRight(content, "\r\n"), sep)
	// Find the `# Completed` H1; if absent, create one at the top.
	insertAt := -1
	for i, ln := range lines {
		if m := headerRE.FindStringSubmatch(ln); m != nil {
			if len(m[1]) == 1 && strings.EqualFold(strings.TrimSpace(m[2]), "Completed") {
				insertAt = i + 1
				if insertAt < len(lines) && strings.TrimSpace(lines[insertAt]) == "" {
					insertAt++
				}
				break
			}
		}
	}
	if insertAt < 0 {
		newContent := "# Completed" + sep + sep + line + sep + sep + strings.TrimLeft(content, "\r\n")
		if !strings.HasSuffix(newContent, sep) {
			newContent += sep
		}
		return newContent
	}
	before := lines[:insertAt]
	after := lines[insertAt:]
	newLines := append([]string{}, before...)
	newLines = append(newLines, line)
	newLines = append(newLines, after...)
	joined := strings.Join(newLines, sep)
	if !strings.HasSuffix(joined, sep) {
		joined += sep
	}
	return joined
}

func renderCompletionLine(in CompletionInput) string {
	var tags []string
	if in.Priority != 0 {
		tags = append(tags, fmt.Sprintf("[priority:%d]", in.Priority))
	}
	if in.Category != "" {
		tags = append(tags, fmt.Sprintf("[category:%s]", in.Category))
	}
	line := fmt.Sprintf("- [x] {id:%s} %s", in.ID, strings.TrimSpace(in.Title))
	if len(tags) > 0 {
		line += " " + strings.Join(tags, " ")
	}
	line += " (done " + in.DoneAt + ")"
	return line
}
