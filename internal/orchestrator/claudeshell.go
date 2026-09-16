package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// ClaudeShell shells `claude -p <prompt>` and returns the trimmed stdout.
// Used by triage (c302) + estimator (8fa3). Not for hot paths — every
// call spawns a Claude session and burns API tokens; callers must gate
// on a config flag.
type ClaudeShell struct {
	Bin string // default "claude" (looked up on PATH at call time)
}

func (c *ClaudeShell) Run(ctx context.Context, prompt string) (string, error) {
	bin := c.Bin
	if bin == "" {
		bin = "claude"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return "", fmt.Errorf("%s not on PATH", bin)
	}
	cmd := exec.CommandContext(ctx, bin, "-p", prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RunJSON is a convenience wrapper that:
//   - appends a "Respond with a single JSON object." instruction
//   - strips markdown code fences from the reply
//   - json.Unmarshals into `into`
func (c *ClaudeShell) RunJSON(ctx context.Context, prompt string, into any) error {
	raw, err := c.Run(ctx, prompt+"\n\nRespond with a single JSON object — no markdown fences, no preamble.")
	if err != nil {
		return err
	}
	// Best-effort strip of fenced code blocks around the JSON payload.
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	// Find the outermost {...} in case the model still added prose.
	if i := strings.Index(raw, "{"); i > 0 {
		raw = raw[i:]
	}
	if j := strings.LastIndex(raw, "}"); j > 0 && j < len(raw)-1 {
		raw = raw[:j+1]
	}
	return json.Unmarshal([]byte(raw), into)
}
