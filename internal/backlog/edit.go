package backlog

import (
	"fmt"
	"strings"
)

// UpdateTaskInput is the mutable set of fields for UpdateTask. ID and Title
// are required; other fields (except Body) default to empty/zero. Body treated
// specially: nil pointer = leave existing body untouched; non-nil pointer =
// replace with the given content (empty string clears the body).
type UpdateTaskInput struct {
	ID                string
	Title             string
	Priority          int
	Category          string
	RequiredResources []string
	Due               string
	Body              *string // nil = don't touch body; *"" = clear; *"..." = replace
}

// UpdateTask rewrites the checkbox line for the task with in.ID, using the
// same tag format renderTaskBlock uses on new tasks. Preserves the original
// checkbox character (pending, deferred, etc.) — Chief's sweep pulls done
// items to completedlog.md, so updates against backlog.md are on live tasks.
// Body is replaced only when in.Body is non-nil.
//
// Requires the parsed task list from ParseFile (for LineNum + body extent).
func UpdateTask(content string, tasks []Task, in UpdateTaskInput) (string, error) {
	if in.ID == "" || strings.TrimSpace(in.Title) == "" {
		return "", fmt.Errorf("UpdateTask: ID and Title required")
	}
	var target *Task
	for i := range tasks {
		if tasks[i].ID == in.ID {
			target = &tasks[i]
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("UpdateTask: task %s not found", in.ID)
	}

	sep := "\n"
	if strings.Contains(content, "\r\n") {
		sep = "\r\n"
	}
	hadTrailingNewline := len(content) > 0 && content[len(content)-1] == '\n'
	lines := strings.Split(strings.TrimRight(content, "\r\n"), sep)

	startIdx := target.LineNum - 1
	if startIdx < 0 || startIdx >= len(lines) {
		return "", fmt.Errorf("UpdateTask: task %s LineNum out of range", in.ID)
	}
	endIdx := blockEnd(lines, startIdx)

	// Preserve the original checkbox char (space, x, ~).
	m := checkboxRE.FindStringSubmatch(lines[startIdx])
	if m == nil {
		return "", fmt.Errorf("UpdateTask: line %d is not a checkbox: %q", target.LineNum, lines[startIdx])
	}
	checkboxChar := m[2]

	newBlock := renderUpdatedBlock(in, checkboxChar, target.Body, lines[startIdx+1:endIdx+1])
	newLines := strings.Split(newBlock, "\n")

	result := make([]string, 0, len(lines)-(endIdx-startIdx+1)+len(newLines))
	result = append(result, lines[:startIdx]...)
	result = append(result, newLines...)
	if endIdx+1 < len(lines) {
		result = append(result, lines[endIdx+1:]...)
	}
	out := strings.Join(result, sep)
	if hadTrailingNewline {
		out = strings.TrimRight(out, "\r\n") + sep
	}
	return out, nil
}

// blockEnd returns the last line index belonging to the task at startIdx —
// the checkbox line plus its indented body (mirroring the parser's rule).
func blockEnd(lines []string, startIdx int) int {
	end := startIdx
	for j := startIdx + 1; j < len(lines); j++ {
		ln := lines[j]
		if strings.TrimSpace(ln) == "" {
			if !isBodyContinuation(lines, j+1) {
				break
			}
			end = j
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
		end = j
	}
	return end
}

// renderUpdatedBlock composes the checkbox line + body for an UpdateTask.
// If in.Body is nil, the existing body lines are preserved verbatim (passed as
// existingBody); if non-nil, the body is replaced with the new content (each
// line indented 2 spaces).
func renderUpdatedBlock(in UpdateTaskInput, checkboxChar, oldBody string, existingBodyLines []string) string {
	_ = oldBody // reserved for future use — currently we rebuild from existingBodyLines to preserve exact formatting when Body is nil.

	var tags []string
	if in.Priority != 0 {
		tags = append(tags, fmt.Sprintf("[priority:%d]", in.Priority))
	}
	if len(in.RequiredResources) > 0 {
		tags = append(tags, fmt.Sprintf("[resources:%s]", strings.Join(in.RequiredResources, ",")))
	}
	if in.Category != "" {
		tags = append(tags, fmt.Sprintf("[category:%s]", in.Category))
	}
	if in.Due != "" {
		tags = append(tags, fmt.Sprintf("[due:%s]", in.Due))
	}
	line := fmt.Sprintf("- [%s] {id:%s} %s", checkboxChar, in.ID, strings.TrimSpace(in.Title))
	if len(tags) > 0 {
		line += " " + strings.Join(tags, " ")
	}

	// Body handling.
	if in.Body != nil {
		body := strings.TrimRight(*in.Body, "\n")
		if strings.TrimSpace(body) == "" {
			return line
		}
		bodyLines := strings.Split(body, "\n")
		for i, bl := range bodyLines {
			bodyLines[i] = "  " + bl
		}
		return line + "\n" + strings.Join(bodyLines, "\n")
	}
	// Preserve existing body verbatim.
	if len(existingBodyLines) == 0 {
		return line
	}
	return line + "\n" + strings.Join(existingBodyLines, "\n")
}

// MoveTask swaps the block of task `id` with its adjacent same-section
// sibling in the given direction ("up" or "down"). If the task is already
// at the top/bottom of its section (or the direction is unknown), returns
// the content unchanged. Sections are delimited by any Markdown header.
func MoveTask(content string, tasks []Task, id, direction string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("MoveTask: id required")
	}
	if direction != "up" && direction != "down" {
		return "", fmt.Errorf("MoveTask: direction must be up or down (got %q)", direction)
	}
	var target *Task
	for i := range tasks {
		if tasks[i].ID == id {
			target = &tasks[i]
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("MoveTask: task %s not found", id)
	}

	sep := "\n"
	if strings.Contains(content, "\r\n") {
		sep = "\r\n"
	}
	hadTrailingNewline := len(content) > 0 && content[len(content)-1] == '\n'
	lines := strings.Split(strings.TrimRight(content, "\r\n"), sep)

	startIdx := target.LineNum - 1
	if startIdx < 0 || startIdx >= len(lines) {
		return content, nil
	}
	endIdx := blockEnd(lines, startIdx)

	// Find the adjacent task's block boundaries, respecting section headers.
	var otherStart, otherEnd int
	if direction == "up" {
		otherStart, otherEnd = prevBlock(lines, startIdx)
	} else {
		otherStart, otherEnd = nextBlock(lines, endIdx+1)
	}
	if otherStart < 0 {
		// Already at the edge of its section.
		return content, nil
	}

	var newLines []string
	if direction == "up" {
		// [before | other | target | after]  →  swap other and target
		// original slice ranges (otherStart..otherEnd, then anything between
		// otherEnd+1..startIdx-1, then startIdx..endIdx). Preserve interlink
		// content (blank lines etc.) between them.
		newLines = make([]string, 0, len(lines))
		newLines = append(newLines, lines[:otherStart]...)
		newLines = append(newLines, lines[startIdx:endIdx+1]...)
		newLines = append(newLines, lines[otherEnd+1:startIdx]...)
		newLines = append(newLines, lines[otherStart:otherEnd+1]...)
		newLines = append(newLines, lines[endIdx+1:]...)
	} else {
		newLines = make([]string, 0, len(lines))
		newLines = append(newLines, lines[:startIdx]...)
		newLines = append(newLines, lines[otherStart:otherEnd+1]...)
		newLines = append(newLines, lines[endIdx+1:otherStart]...)
		newLines = append(newLines, lines[startIdx:endIdx+1]...)
		newLines = append(newLines, lines[otherEnd+1:]...)
	}

	out := strings.Join(newLines, sep)
	if hadTrailingNewline {
		out = strings.TrimRight(out, "\r\n") + sep
	}
	return out, nil
}

// prevBlock returns the [start, end] indices of the column-0 task block
// immediately above `fromIdx`, or (-1,-1) if none exists in the same section
// (i.e., a header is encountered before another task).
func prevBlock(lines []string, fromIdx int) (int, int) {
	for i := fromIdx - 1; i >= 0; i-- {
		ln := lines[i]
		if headerRE.MatchString(ln) {
			return -1, -1
		}
		m := checkboxRE.FindStringSubmatch(ln)
		if m != nil && m[1] == "" {
			end := blockEnd(lines, i)
			return i, end
		}
	}
	return -1, -1
}

// nextBlock returns the [start, end] indices of the column-0 task block
// starting at or after `fromIdx`, or (-1,-1) if a header comes first.
func nextBlock(lines []string, fromIdx int) (int, int) {
	for i := fromIdx; i < len(lines); i++ {
		ln := lines[i]
		if headerRE.MatchString(ln) {
			return -1, -1
		}
		m := checkboxRE.FindStringSubmatch(ln)
		if m != nil && m[1] == "" {
			end := blockEnd(lines, i)
			return i, end
		}
	}
	return -1, -1
}
