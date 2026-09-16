package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/geekychris/chief/internal/store"
)

// Triager (c302) enriches newly-raised flags by shelling Claude for a
// 1-line summary + suggested urgency reclass + a link to the most
// similar recent open flag on the same project. Stores results in
// flag_metadata; the UI + CLI show them but chief never auto-applies
// (the human still owns the reclassification decision).
//
// Design: triage is triggered by a call to Triage(ctx, flagID) which
// callers (attention.Manager on Raise) invoke asynchronously — the
// Raise() return path stays fast; Claude latency is amortized offline.
type Triager struct {
	Store    *store.Store
	Claude   *ClaudeShell
	Enabled  bool
	Timeout  time.Duration
}

// Triage looks up the flag + up to N recent open flags on the same
// project, asks Claude for {summary, suggested_urgency, related_flag_id},
// and merges the result into flag_metadata.
func (t *Triager) Triage(ctx context.Context, flagID string) error {
	if !t.Enabled || t.Store == nil || t.Claude == nil {
		return nil
	}
	deadline := t.Timeout
	if deadline <= 0 {
		deadline = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	f, err := t.Store.GetFlag(ctx, flagID)
	if err != nil {
		return err
	}
	if f.AckAt != nil {
		return nil // no point triaging an acked flag
	}
	// Grab up to 8 other open flags on this project for grouping context.
	others, _ := t.Store.ListFlags(ctx, store.FlagFilter{
		ProjectID: f.ProjectID, OpenOnly: true, Limit: 8,
	})
	var recent string
	for _, o := range others {
		if o.ID == f.ID {
			continue
		}
		recent += fmt.Sprintf("  - {%s} urgency=%s: %s\n", o.ID, o.Urgency, truncateFor(o.Question, 120))
	}
	if recent == "" {
		recent = "  (no other open flags on this project)"
	}

	prompt := fmt.Sprintf(
		`You are triaging a chief attention flag. Return a JSON object with:
{
  "summary":            "one-line summary suitable for a menu bar (<= 80 chars, no trailing period)",
  "suggested_urgency": "info" | "attention" | "urgent",
  "related_flag_id":   "one of the other open flag ids if the new flag is likely a duplicate/related, else empty string",
  "reasoning":          "one short sentence explaining the urgency choice"
}

New flag ({id:%s}) urgency=%s kind=%s:
%s

Other open flags on the same project:
%s`,
		f.ID, f.Urgency, f.Kind, truncateFor(f.Question, 800), recent,
	)

	var out struct {
		Summary          string `json:"summary"`
		SuggestedUrgency string `json:"suggested_urgency"`
		RelatedFlagID    string `json:"related_flag_id"`
		Reasoning        string `json:"reasoning"`
	}
	if err := t.Claude.RunJSON(ctx, prompt, &out); err != nil {
		return fmt.Errorf("triage claude: %w", err)
	}
	patch := map[string]any{
		"triage_summary":    out.Summary,
		"triage_urgency":    out.SuggestedUrgency,
		"triage_related":    out.RelatedFlagID,
		"triage_reasoning":  out.Reasoning,
		"triage_completed":  time.Now().UTC().Format(time.RFC3339),
	}
	if err := t.Store.SetFlagMetadata(ctx, f.ID, patch); err != nil {
		return err
	}
	slog.Info("triage: enriched flag", "flag", f.ID, "suggested", out.SuggestedUrgency)
	return nil
}

func truncateFor(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
