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

// workspaceListEnvelope matches `cmux workspace list --json`. CWD is
// used by OpenTerminal to pick the project's own workspace even when
// no visible surface currently has a matching cwd (e.g. a workspace
// whose Claude session died leaves the workspace itself with the right
// current_directory).
type workspaceListEnvelope struct {
	Workspaces []struct {
		Ref              string `json:"ref"`
		CurrentDirectory string `json:"current_directory"`
	} `json:"workspaces"`
}

// ListSurfaces returns all cmux surfaces on this machine. cmux's
// list-panels command only enumerates panels in the CURRENTLY SELECTED
// workspace by default; to reach amiga_mcp's Claude session when
// chief-project-orchestrator is focused (a very common case), we fan
// out list-panels across every workspace returned by
// `cmux workspace list`.
func (c *Client) ListSurfaces(ctx context.Context) ([]Surface, error) {
	// Enumerate workspaces first so we can query each one.
	wsOut, err := c.run(ctx, "workspace", "list", "--json")
	if err != nil {
		// Fall back to a single default-workspace query — better than
		// nothing if `workspace list` isn't available for some reason.
		return c.listPanelsForWorkspace(ctx, "")
	}
	var wsEnv workspaceListEnvelope
	if err := json.Unmarshal(wsOut, &wsEnv); err != nil {
		return c.listPanelsForWorkspace(ctx, "")
	}
	if len(wsEnv.Workspaces) == 0 {
		return c.listPanelsForWorkspace(ctx, "")
	}
	seen := map[string]struct{}{}
	var all []Surface
	for _, w := range wsEnv.Workspaces {
		surfaces, err := c.listPanelsForWorkspace(ctx, w.Ref)
		if err != nil {
			// A single-workspace failure is not fatal — cmux may have
			// stale references to closed workspaces; log-and-skip.
			continue
		}
		for _, s := range surfaces {
			if _, dup := seen[s.Ref]; dup {
				continue
			}
			seen[s.Ref] = struct{}{}
			all = append(all, s)
		}
	}
	return all, nil
}

// listPanelsForWorkspace runs `cmux list-panels --json` for the given
// workspace ref (or the current workspace when ref is ""). Extracted so
// ListSurfaces can fan out across all workspaces AND callers with a
// specific workspace in mind can target it directly.
func (c *Client) listPanelsForWorkspace(ctx context.Context, workspaceRef string) ([]Surface, error) {
	args := []string{"list-panels", "--json"}
	if workspaceRef != "" {
		args = append(args, "--workspace", workspaceRef)
	}
	out, err := c.run(ctx, args...)
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

// workspaceRefForSurface finds the workspace containing surfaceRef by
// scanning `cmux workspace list --json` for a workspace whose panels
// include the given surface. Returns "" when no match — the caller
// should then fall back to creating a fresh workspace.
func (c *Client) workspaceRefForSurface(ctx context.Context, surfaceRef string) string {
	wsOut, err := c.run(ctx, "workspace", "list", "--json")
	if err != nil {
		return ""
	}
	var env workspaceListEnvelope
	if err := json.Unmarshal(wsOut, &env); err != nil {
		return ""
	}
	for _, w := range env.Workspaces {
		surfaces, err := c.listPanelsForWorkspace(ctx, w.Ref)
		if err != nil {
			continue
		}
		for _, s := range surfaces {
			if s.Ref == surfaceRef {
				return w.Ref
			}
		}
	}
	return ""
}

// TerminalResult describes the outcome of OpenTerminal.
type TerminalResult struct {
	SurfaceRef   string // the terminal that was focused or created
	WorkspaceRef string // the workspace it belongs to
	Reused       bool   // true when we focused an existing shell instead of creating one
	NewWorkspace bool   // true when we had to spawn a fresh workspace (project had none)
}

// OpenTerminal opens (or focuses) a plain terminal cmux surface for the
// given project cwd. Strategy — in order of preference so users stay in
// their existing project workspace and don't accumulate stray shells:
//
//  1. Find a "project workspace" — one whose current_directory matches
//     (or contains) the target path. When multiple workspaces match
//     (common when the user has both an active editing workspace and a
//     leftover one for the same repo), prefer the one that already
//     hosts a live surface with the same cwd.
//  2. Inside that workspace, look for an existing non-Claude terminal
//     surface at the same cwd. If found, `focus-panel` it — no new
//     shell created. This is what "reuse an existing" means.
//  3. Otherwise, `new-surface --type terminal --workspace <ref>
//     --working-directory <cwd> --focus true` — a fresh shell in the
//     project's existing workspace, at the project's root.
//  4. Only when NO workspace matches at all do we create a brand-new
//     one via `new-workspace --cwd <path>`.
//
// The returned TerminalResult carries the surface/workspace refs plus
// booleans so callers can render "reused existing" vs "spawned fresh"
// in the UI without a second RPC round-trip.
func (c *Client) OpenTerminal(ctx context.Context, cwd string) (TerminalResult, error) {
	if cwd == "" {
		return TerminalResult{}, errors.New("OpenTerminal: cwd required")
	}
	target := strings.TrimRight(cwd, "/")

	// Enumerate workspaces (with cwd) and all surfaces (across every
	// workspace) so we can score matches without a bunch of extra RPCs.
	wsOut, err := c.run(ctx, "workspace", "list", "--json")
	if err != nil {
		return c.spawnFreshWorkspace(ctx, cwd)
	}
	var wsEnv workspaceListEnvelope
	if err := json.Unmarshal(wsOut, &wsEnv); err != nil {
		return c.spawnFreshWorkspace(ctx, cwd)
	}
	surfacesByWS := map[string][]Surface{}
	for _, w := range wsEnv.Workspaces {
		s, err := c.listPanelsForWorkspace(ctx, w.Ref)
		if err != nil {
			continue
		}
		surfacesByWS[w.Ref] = s
	}

	// Score each workspace: exact cwd match > prefix match, and among
	// matches prefer one that already hosts a surface at cwd.
	type wsScore struct {
		ref     string
		wsCWD   string
		score   int
		hasLive bool // any surface with cwd inside target
	}
	var candidates []wsScore
	for _, w := range wsEnv.Workspaces {
		wsCWDTrim := strings.TrimRight(w.CurrentDirectory, "/")
		score := 0
		switch {
		case wsCWDTrim == target:
			score = 3
		case strings.HasPrefix(wsCWDTrim, target+"/"):
			score = 2
		case strings.HasPrefix(target, wsCWDTrim+"/"):
			// Workspace cwd is an ANCESTOR of target (e.g. workspace at
			// ~/code, project at ~/code/foo). Weakest match — better
			// than making a new workspace but not by much.
			score = 1
		}
		hasLive := false
		for _, s := range surfacesByWS[w.Ref] {
			if s.CWD == target || strings.HasPrefix(s.CWD, target+"/") {
				hasLive = true
				break
			}
		}
		if score == 0 && !hasLive {
			continue
		}
		if hasLive {
			score++ // tiebreak
		}
		candidates = append(candidates, wsScore{ref: w.Ref, wsCWD: wsCWDTrim, score: score, hasLive: hasLive})
	}
	if len(candidates) == 0 {
		return c.spawnFreshWorkspace(ctx, cwd)
	}
	// Pick the highest-scored workspace.
	best := candidates[0]
	for _, cand := range candidates[1:] {
		if cand.score > best.score {
			best = cand
		}
	}

	// Reuse: focus an existing plain-terminal surface with matching cwd.
	for _, s := range surfacesByWS[best.ref] {
		if s.IsClaude {
			continue // never hijack a Claude session
		}
		if s.CWD == target || strings.HasPrefix(s.CWD, target+"/") {
			if _, err := c.run(ctx, "focus-panel", "--panel", s.Ref, "--workspace", best.ref); err == nil {
				return TerminalResult{
					SurfaceRef: s.Ref, WorkspaceRef: best.ref, Reused: true,
				}, nil
			}
			// Focus failed — fall through to spawn.
			break
		}
	}

	// Spawn a fresh terminal inside the project's workspace.
	out, err := c.run(ctx, "new-surface",
		"--type", "terminal",
		"--workspace", best.ref,
		"--working-directory", cwd,
		"--focus", "true",
	)
	if err != nil {
		// As a last resort, still get the user a shell.
		return c.spawnFreshWorkspace(ctx, cwd)
	}
	return TerminalResult{
		SurfaceRef:   parseSurfaceRefFromOutput(string(out)),
		WorkspaceRef: best.ref,
	}, nil
}

// spawnFreshWorkspace is the last-resort fallback when no workspace can
// be reused. `new-workspace --cwd <path>` creates one anchored at cwd
// and spawns a shell inside it.
func (c *Client) spawnFreshWorkspace(ctx context.Context, cwd string) (TerminalResult, error) {
	name := lastPathComponent(cwd)
	out, err := c.run(ctx, "new-workspace",
		"--cwd", cwd,
		"--name", name+" (chief)",
		"--focus", "true",
	)
	if err != nil {
		return TerminalResult{}, err
	}
	return TerminalResult{
		SurfaceRef:   parseSurfaceRefFromOutput(string(out)),
		NewWorkspace: true,
	}, nil
}

// parseSurfaceRefFromOutput extracts a `surface:NN` ref from cmux's
// occasional plain-text output. Returns "" when nothing matches.
func parseSurfaceRefFromOutput(s string) string {
	// cmux prints "surface:NN" somewhere in stdout for new-surface /
	// new-workspace. Search for the pattern.
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, "surface:"); idx >= 0 {
			rest := line[idx+len("surface:"):]
			end := 0
			for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
				end++
			}
			if end > 0 {
				return "surface:" + rest[:end]
			}
		}
	}
	return ""
}

func lastPathComponent(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
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
