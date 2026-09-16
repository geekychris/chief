package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/geekychris/chief/internal/store"
)

// Estimator (8fa3) shells Claude for S/M/L sizing + a priority
// suggestion for a task. Meant to run on task.add (async) and
// on-demand via a CLI. Result lands in task_metadata under keys
// `estimate_size` + `estimate_priority` + `estimate_reason`.
type Estimator struct {
	Store   *store.Store
	Claude  *ClaudeShell
	Enabled bool
	Timeout time.Duration
}

// Estimate looks up the task + reads its project's constitution.md
// (if present) for context, then shells Claude for a sizing.
func (e *Estimator) Estimate(ctx context.Context, taskID string) error {
	if !e.Enabled || e.Store == nil || e.Claude == nil {
		return nil
	}
	deadline := e.Timeout
	if deadline <= 0 {
		deadline = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	tasks, err := e.Store.ListTasks(ctx, store.TaskFilter{})
	if err != nil {
		return err
	}
	var t store.Task
	for _, x := range tasks {
		if x.ID == taskID {
			t = x
			break
		}
	}
	if t.ID == "" {
		return fmt.Errorf("estimator: task %s not found", taskID)
	}

	proj, err := e.Store.GetProject(ctx, t.ProjectID)
	if err != nil {
		return err
	}
	constitution := readIfPresent(filepath.Join(proj.Path, "constitution.md"), 4000)
	category := t.Category
	if category == "" {
		category = "(no category)"
	}

	prompt := fmt.Sprintf(
		`You are estimating a chief backlog item. Return a JSON object with:
{
  "size":     "S" | "M" | "L",
  "priority": integer -3..+5 (positive = higher priority),
  "reason":   "one short sentence"
}

Guidance:
  S = under 2h focused work, one file / one concern
  M = half a day, spans 2-5 files, one subsystem
  L = full day+, cross-cuts, or requires design decisions

Task title:    %s
Task body:     %s
Category:      %s
Project:       %s

Project constitution (may be empty):
%s`,
		t.Title, truncateFor(t.Body, 800), category, proj.Name, constitution,
	)

	var out struct {
		Size     string `json:"size"`
		Priority int    `json:"priority"`
		Reason   string `json:"reason"`
	}
	if err := e.Claude.RunJSON(ctx, prompt, &out); err != nil {
		return fmt.Errorf("estimator claude: %w", err)
	}
	// Sanity-clamp priority so a bad response can't produce a weird row.
	if out.Priority < -9 {
		out.Priority = -9
	}
	if out.Priority > 9 {
		out.Priority = 9
	}
	patch := map[string]any{
		"estimate_size":     out.Size,
		"estimate_priority": out.Priority,
		"estimate_reason":   out.Reason,
		"estimated_at":      time.Now().UTC().Format(time.RFC3339),
	}
	if err := e.Store.SetTaskMetadata(ctx, t.ID, patch); err != nil {
		return err
	}
	slog.Info("estimator: sized", "task", t.ID, "size", out.Size, "priority", out.Priority)
	return nil
}

func readIfPresent(path string, maxBytes int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "(not present)"
	}
	if len(b) > maxBytes {
		return string(b[:maxBytes]) + "\n… (truncated)"
	}
	return string(b)
}
