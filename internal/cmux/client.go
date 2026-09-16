// Package cmux is a thin wrapper around the `cmux` CLI. We shell out rather
// than speak the JSON-RPC socket protocol directly — the CLI is stable,
// documented, and the round-trip cost is negligible for the low-frequency
// operations Chief needs (list surfaces, send prompt).
//
// Verified against cmux v2 as of 2026-09-13. Verb inventory used here:
//   cmux list-panels --json           → discover surfaces
//   cmux send --surface <ref> <text>  → inject a prompt into a surface
package cmux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DefaultBinary is the standard install location on macOS. Callers can
// override by setting Client.Binary or the CMUX_BIN environment variable.
const DefaultBinary = "/Applications/cmux.app/Contents/Resources/bin/cmux"

// Client shells out to the cmux binary. Zero-value is usable when cmux is
// installed at DefaultBinary and using its default socket path.
type Client struct {
	Binary string // path to cmux CLI; falls back to $CMUX_BIN then DefaultBinary
	Socket string // optional CMUX_SOCKET_PATH override
	// Password for cmux's --password flag. Required when cmux's
	// automation.socketControlMode is "password" or when calling from a
	// process not spawned inside cmux. Falls back to $CMUX_SOCKET_PASSWORD
	// (or, for an ephemeral bootstrap, $CMUX_SOCKET_CAPABILITY).
	Password string
	// Timeout for individual CLI invocations. Zero = 5s default.
	Timeout time.Duration
}

// Surface is one cmux terminal surface (a pane inside a workspace).
type Surface struct {
	Ref              string // e.g. "surface:51" — pass this to Send
	Title            string
	CWD              string // resolved cwd (falls back to requested_working_directory)
	Type             string // "terminal", ...
	Focused          bool
	IsClaude         bool   // resume_binding.kind == "claude"
	ClaudeSessionID  string // resume_binding.checkpoint_id when IsClaude
	WorkspaceRef     string // hint for launching new sessions in same workspace
	Launcher         string // resume_binding.launch_command.launcher — "claude"/"codex"/...
	PaneRef          string
	AgentName        string // resume_binding.name — "Claude Code" etc.
}

// listPanelsEnvelope matches the top-level shape of `cmux list-panels --json`.
type listPanelsEnvelope struct {
	Surfaces []rawSurface `json:"surfaces"`
}

type rawSurface struct {
	Ref                        string           `json:"ref"`
	Title                      string           `json:"title"`
	Focused                    bool             `json:"focused"`
	Type                       string           `json:"type"`
	PaneRef                    string           `json:"pane_ref"`
	RequestedWorkingDirectory  string           `json:"requested_working_directory"`
	ResumeBinding              *rawResumeBinding `json:"resume_binding"`
}

type rawResumeBinding struct {
	Kind          string          `json:"kind"`
	CWD           string          `json:"cwd"`
	Name          string          `json:"name"`
	CheckpointID  string          `json:"checkpoint_id"`
	LaunchCommand *rawLaunchCmd   `json:"launch_command"`
}

type rawLaunchCmd struct {
	Launcher string `json:"launcher"`
}

// ListSurfaces returns all cmux surfaces on this machine.
func (c *Client) ListSurfaces(ctx context.Context) ([]Surface, error) {
	out, err := c.run(ctx, "list-panels", "--json")
	if err != nil {
		return nil, err
	}
	var env listPanelsEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("parse list-panels: %w (raw: %s)", err, truncate(string(out), 400))
	}
	res := make([]Surface, 0, len(env.Surfaces))
	for _, r := range env.Surfaces {
		s := Surface{
			Ref: r.Ref, Title: r.Title, Focused: r.Focused,
			Type: r.Type, PaneRef: r.PaneRef,
			CWD: r.RequestedWorkingDirectory,
		}
		if r.ResumeBinding != nil {
			if r.ResumeBinding.CWD != "" {
				s.CWD = r.ResumeBinding.CWD
			}
			s.IsClaude = strings.EqualFold(r.ResumeBinding.Kind, "claude")
			s.ClaudeSessionID = r.ResumeBinding.CheckpointID
			s.AgentName = r.ResumeBinding.Name
			if r.ResumeBinding.LaunchCommand != nil {
				s.Launcher = r.ResumeBinding.LaunchCommand.Launcher
			}
		}
		res = append(res, s)
	}
	return res, nil
}

// CandidatesForPath filters surfaces whose cwd matches (or is a descendant of)
// the given path. `claudeOnly=true` returns only Claude sessions.
func (c *Client) CandidatesForPath(ctx context.Context, path string, claudeOnly bool) ([]Surface, error) {
	all, err := c.ListSurfaces(ctx)
	if err != nil {
		return nil, err
	}
	target := strings.TrimRight(path, "/")
	var out []Surface
	for _, s := range all {
		if claudeOnly && !s.IsClaude {
			continue
		}
		if s.CWD == target || strings.HasPrefix(s.CWD, target+"/") {
			out = append(out, s)
		}
	}
	return out, nil
}

// Send injects raw text into the given surface. cmux interprets `\n` (and
// `\r`) as Enter, so callers should include a trailing "\n" if they want the
// input submitted. Prefer SendAndRun when you want a guaranteed submit.
func (c *Client) Send(ctx context.Context, surfaceRef, text string) error {
	if surfaceRef == "" {
		return errors.New("Send: surfaceRef required")
	}
	if _, err := c.run(ctx, "send", "--surface", surfaceRef, "--", text); err != nil {
		return err
	}
	return nil
}

// SendKey sends a single named key event (see `cmux send-key --help` for the
// key vocabulary — "enter", "esc", "tab", "ctrl+c", etc.).
func (c *Client) SendKey(ctx context.Context, surfaceRef, key string) error {
	if surfaceRef == "" {
		return errors.New("SendKey: surfaceRef required")
	}
	if key == "" {
		return errors.New("SendKey: key required")
	}
	if _, err := c.run(ctx, "send-key", "--surface", surfaceRef, "--", key); err != nil {
		return err
	}
	return nil
}

// SendAndRun types text into the surface and then explicitly submits it with
// an Enter key event. Use for prompts that must be executed (not just queued
// as a draft the user hits Enter on later). Trims trailing newlines from
// text so we don't double-submit — Enter is fired exactly once, at the end.
func (c *Client) SendAndRun(ctx context.Context, surfaceRef, text string) error {
	// Drop trailing newline(s) so the explicit send-key enter is the sole
	// submit signal. Preserves internal newlines in the body.
	trimmed := text
	for len(trimmed) > 0 && (trimmed[len(trimmed)-1] == '\n' || trimmed[len(trimmed)-1] == '\r') {
		trimmed = trimmed[:len(trimmed)-1]
	}
	if trimmed != "" {
		if err := c.Send(ctx, surfaceRef, trimmed); err != nil {
			return err
		}
	}
	return c.SendKey(ctx, surfaceRef, "enter")
}

// CloseSurface asks cmux to close (terminate) the surface at surfaceRef.
// Used by the idle-Claude killer (1b72). No-op if the surface is already
// gone — cmux returns an error we treat as success.
func (c *Client) CloseSurface(ctx context.Context, surfaceRef string) error {
	if surfaceRef == "" {
		return errors.New("CloseSurface: surfaceRef required")
	}
	_, err := c.run(ctx, "close-surface", "--surface", surfaceRef)
	return err
}

// run executes cmux with args, returning stdout. Errors include stderr for
// debugging. Uses c.Timeout (default 5s). Password (if any) is passed as the
// first global flag before the subcommand.
func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	bin := c.Binary
	if bin == "" {
		if v := os.Getenv("CMUX_BIN"); v != "" {
			bin = v
		} else {
			bin = DefaultBinary
		}
	}
	// Resolve password: explicit → CMUX_SOCKET_PASSWORD → CMUX_SOCKET_CAPABILITY
	// (the last is a session-scoped token cmux injects into its own children;
	// handy for bootstrap/demo but not stable across cmux restarts).
	password := c.Password
	if password == "" {
		password = os.Getenv("CMUX_SOCKET_PASSWORD")
	}
	if password == "" {
		password = os.Getenv("CMUX_SOCKET_CAPABILITY")
	}
	full := args
	if password != "" {
		full = append([]string{"--password", password}, args...)
	}

	timeout := c.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, full...)
	if c.Socket != "" {
		cmd.Env = append(os.Environ(), "CMUX_SOCKET_PATH="+c.Socket)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cmux %s: %w (stderr: %s)", strings.Join(args, " "), err, truncate(stderr.String(), 400))
	}
	return stdout.Bytes(), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
