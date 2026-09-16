package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/geekychris/chief/internal/claudetrace"
	"github.com/geekychris/chief/internal/ipc"
	"github.com/geekychris/chief/internal/methods"
)

// execOpen shells the macOS `open` command. Small wrapper so all UI-level
// launches share error handling.
func execOpen(args ...string) error {
	return exec.Command("open", args...).Start()
}

// analyzerStatus is a Wails-side probe (independent of chiefd's answer) so
// the Open button can decide instantly.
func analyzerStatus() (bool, string) { return claudetrace.AnalyzerInstalled() }

// App holds the Wails context. All exported methods are surfaced as JS
// bindings under `window.go.main.App.<method>`.
type App struct {
	ctx context.Context
}

// NewApp constructs the backend struct. Called once by main().
func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// ---------- exposed bindings ----------
//
// Every backend method here is proxied through chiefd; we never touch SQLite
// directly. Fresh short-lived connections per call so a chiefd bounce doesn't
// break the UI.

// Ping returns chiefd's health snapshot ({pong, version, pid, time}) or an
// error string.
func (a *App) Ping() (map[string]any, error) {
	c, err := a.dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var raw json.RawMessage
	if err := c.Call("ping", nil, &raw); err != nil {
		return nil, err
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out, nil
}

// ListProjects returns per-project summaries with counts.
func (a *App) ListProjects() ([]methods.ProjectSummary, error) {
	c, err := a.dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var resp methods.ProjectListResponse
	if err := c.Call("project.list", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Projects, nil
}

// ListBacklog returns tasks; empty projectID = all projects, empty status = all statuses.
func (a *App) ListBacklog(projectID, status string) ([]methods.BacklogRow, error) {
	c, err := a.dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var resp methods.BacklogListResponse
	req := methods.BacklogListRequest{ProjectID: projectID, Status: status}
	if err := c.Call("backlog.list", req, &resp); err != nil {
		return nil, err
	}
	return resp.Tasks, nil
}

// GetTask returns a single task with its project name.
func (a *App) GetTask(id string) (methods.TaskShowResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.TaskShowResponse{}, err
	}
	defer c.Close()
	var resp methods.TaskShowResponse
	if err := c.Call("task.show", methods.TaskShowRequest{ID: id}, &resp); err != nil {
		return methods.TaskShowResponse{}, err
	}
	return resp, nil
}

// AddProject registers a new project directory (absolute path).
func (a *App) AddProject(path, name, spawnMode string) (methods.ProjectAddResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.ProjectAddResponse{}, err
	}
	defer c.Close()
	var resp methods.ProjectAddResponse
	req := methods.ProjectAddRequest{Path: path, Name: name, SpawnMode: spawnMode}
	if err := c.Call("project.add", req, &resp); err != nil {
		return methods.ProjectAddResponse{}, err
	}
	return resp, nil
}

// RemoveProject removes a project by id, name, or path. Files are left alone.
func (a *App) RemoveProject(idOrPath string) error {
	c, err := a.dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var resp methods.ProjectRemoveResponse
	return c.Call("project.remove", methods.ProjectRemoveRequest{IDOrPath: idOrPath}, &resp)
}

// RescanProject forces a re-parse of the project's markdown.
func (a *App) RescanProject(idOrPath string) (int, error) {
	c, err := a.dial()
	if err != nil {
		return 0, err
	}
	defer c.Close()
	var resp methods.ProjectRescanResponse
	if err := c.Call("project.rescan", methods.ProjectRescanRequest{IDOrPath: idOrPath}, &resp); err != nil {
		return 0, err
	}
	return resp.Tasks, nil
}

// AddTask appends a new task to the project's backlog.md.
func (a *App) AddTask(projectID, title, body, category string, priority int, resources []string) (methods.TaskAddResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.TaskAddResponse{}, err
	}
	defer c.Close()
	var resp methods.TaskAddResponse
	req := methods.TaskAddRequest{
		ProjectID: projectID, Title: title, Body: body,
		Category: category, Priority: priority,
		RequiredResources: resources,
	}
	if err := c.Call("task.add", req, &resp); err != nil {
		return methods.TaskAddResponse{}, err
	}
	return resp, nil
}

// UpdateTask rewrites a task's checkbox line (title, priority, category) in
// backlog.md. Body is applied only when updateBody is true (empty string then
// clears the body; non-empty replaces it).
func (a *App) UpdateTask(taskID, title, body, category string, priority int, updateBody bool) (methods.TaskUpdateResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.TaskUpdateResponse{}, err
	}
	defer c.Close()
	var resp methods.TaskUpdateResponse
	req := methods.TaskUpdateRequest{
		TaskID: taskID, Title: title, Priority: priority,
		Category: category, UpdateBody: updateBody, Body: body,
	}
	if err := c.Call("task.update", req, &resp); err != nil {
		return methods.TaskUpdateResponse{}, err
	}
	return resp, nil
}

// ReorderTask swaps a task with its adjacent same-section sibling.
// direction: "up" | "down". No-op if the task is at the edge of its section.
func (a *App) ReorderTask(taskID, direction string) (methods.TaskReorderResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.TaskReorderResponse{}, err
	}
	defer c.Close()
	var resp methods.TaskReorderResponse
	req := methods.TaskReorderRequest{TaskID: taskID, Direction: direction}
	if err := c.Call("task.reorder", req, &resp); err != nil {
		return methods.TaskReorderResponse{}, err
	}
	return resp, nil
}

// DeleteTask removes the task from its project's backlog.md (which cascades
// into the store via Rescan).  Only backlog.md items are deletable —
// completed items in completedlog.md stay as history.
func (a *App) DeleteTask(taskID string) error {
	c, err := a.dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var resp methods.TaskDeleteResponse
	return c.Call("task.delete", methods.TaskDeleteRequest{TaskID: taskID}, &resp)
}

// SendTasksBatch composes a combined "please work on these N tasks" prompt
// and injects it into the project's bound cmux surface.  Requires all
// task_ids to belong to the same project.  Mirrors SendTask's return shape
// (including the app-level graceful handling of unbound / gone surfaces).
type SendTasksBatchResult struct {
	OK           bool                            `json:"ok"`
	Count        int                             `json:"count,omitempty"`
	SurfaceRef   string                          `json:"surface_ref,omitempty"`
	Prompt       string                          `json:"prompt,omitempty"`
	NeedsBinding bool                            `json:"needs_binding,omitempty"`
	SurfaceGone  bool                            `json:"surface_gone,omitempty"`
	Candidates   *methods.CmuxCandidatesResponse `json:"candidates,omitempty"`
	Error        string                          `json:"error,omitempty"`
}

func (a *App) SendTasksBatch(taskIDs []string, surfaceOverride string) (SendTasksBatchResult, error) {
	c, err := a.dial()
	if err != nil {
		return SendTasksBatchResult{}, err
	}
	defer c.Close()
	var resp methods.TaskSendBatchResponse
	req := methods.TaskSendBatchRequest{TaskIDs: taskIDs, SurfaceOverride: surfaceOverride}
	if err := c.Call("task.send_batch", req, &resp); err != nil {
		if rpc, ok := err.(*ipc.RPCError); ok {
			if rpc.Code == methods.ErrCodeCmuxUnbound || rpc.Code == methods.ErrCodeCmuxSurfaceGone {
				// Look up the first task's project id to fetch candidates.
				if pid, cerr := a.projectIDForTask(taskIDs[0]); cerr == nil {
					if cands, cerr2 := a.CmuxCandidates(pid); cerr2 == nil {
						return SendTasksBatchResult{
							OK: false,
							NeedsBinding: rpc.Code == methods.ErrCodeCmuxUnbound,
							SurfaceGone:  rpc.Code == methods.ErrCodeCmuxSurfaceGone,
							Candidates:   &cands,
							Error:        rpc.Message,
						}, nil
					}
				}
			}
		}
		return SendTasksBatchResult{OK: false, Error: err.Error()}, nil
	}
	return SendTasksBatchResult{
		OK: true, Count: resp.Count, SurfaceRef: resp.SurfaceRef, Prompt: resp.Prompt,
	}, nil
}

// CmuxCandidates returns candidate cmux surfaces for a project (cwd matches
// filtered to Claude sessions) plus the full surface list for a manual override.
func (a *App) CmuxCandidates(projectID string) (methods.CmuxCandidatesResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.CmuxCandidatesResponse{}, err
	}
	defer c.Close()
	var resp methods.CmuxCandidatesResponse
	req := methods.CmuxCandidatesRequest{ProjectID: projectID, ClaudeOnly: true}
	if err := c.Call("cmux.candidates", req, &resp); err != nil {
		return methods.CmuxCandidatesResponse{}, err
	}
	return resp, nil
}

// CmuxBind persists a cmux surface binding to .chief/project.yaml.
func (a *App) CmuxBind(projectID, surfaceRef string) error {
	c, err := a.dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var resp methods.CmuxBindResponse
	return c.Call("cmux.bind", methods.CmuxBindRequest{ProjectID: projectID, SurfaceRef: surfaceRef}, &resp)
}

// SendTask injects a prompt for the task into the project's bound cmux surface.
// Returns { ok: true, prompt, surface_ref } or { ok: false, needs_binding: true, candidates }.
type SendTaskResult struct {
	OK           bool                        `json:"ok"`
	SurfaceRef   string                      `json:"surface_ref,omitempty"`
	Prompt       string                      `json:"prompt,omitempty"`
	NeedsBinding bool                        `json:"needs_binding,omitempty"`
	SurfaceGone  bool                        `json:"surface_gone,omitempty"`
	Candidates   *methods.CmuxCandidatesResponse `json:"candidates,omitempty"`
	Error        string                      `json:"error,omitempty"`
}

func (a *App) SendTask(taskID, surfaceOverride string) (SendTaskResult, error) {
	c, err := a.dial()
	if err != nil {
		return SendTaskResult{}, err
	}
	defer c.Close()
	var resp methods.TaskSendResponse
	req := methods.TaskSendRequest{TaskID: taskID, SurfaceOverride: surfaceOverride}
	if err := c.Call("task.send", req, &resp); err != nil {
		// If it's an unbound/gone-surface error, load candidates so the UI can
		// show a picker without a second round-trip.
		if rpc, ok := err.(*ipc.RPCError); ok {
			if rpc.Code == methods.ErrCodeCmuxUnbound || rpc.Code == methods.ErrCodeCmuxSurfaceGone {
				// Look up the task's project id to fetch candidates.
				projectID, cerr := a.projectIDForTask(taskID)
				if cerr == nil {
					if cands, cerr2 := a.CmuxCandidates(projectID); cerr2 == nil {
						return SendTaskResult{
							OK: false,
							NeedsBinding: rpc.Code == methods.ErrCodeCmuxUnbound,
							SurfaceGone:  rpc.Code == methods.ErrCodeCmuxSurfaceGone,
							Candidates:   &cands,
							Error:        rpc.Message,
						}, nil
					}
				}
			}
		}
		return SendTaskResult{OK: false, Error: err.Error()}, nil
	}
	return SendTaskResult{OK: true, SurfaceRef: resp.SurfaceRef, Prompt: resp.Prompt}, nil
}

// projectIDForTask looks up a task to find its project id — used by SendTask
// when we need to hand candidates back to the UI.
func (a *App) projectIDForTask(taskID string) (string, error) {
	c, err := a.dial()
	if err != nil {
		return "", err
	}
	defer c.Close()
	var resp methods.TaskShowResponse
	if err := c.Call("task.show", methods.TaskShowRequest{ID: taskID}, &resp); err != nil {
		return "", err
	}
	return resp.Task.ProjectID, nil
}

// GetProjectSessions returns Claude Code session JSONL info + whether the
// claude-trace analyzer is installed. Used by the UI's Sessions section.
func (a *App) GetProjectSessions(projectID string) (methods.ProjectSessionsResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.ProjectSessionsResponse{}, err
	}
	defer c.Close()
	var resp methods.ProjectSessionsResponse
	if err := c.Call("project.sessions", methods.ProjectSessionsRequest{IDOrPath: projectID}, &resp); err != nil {
		return methods.ProjectSessionsResponse{}, err
	}
	return resp, nil
}

// RevealInFinder opens the given absolute path in Finder (`open -R` reveals
// with parent shown). Silently no-ops on error to avoid noisy UI popups.
func (a *App) RevealInFinder(path string) error {
	// -R selects the item in the parent dir if it exists; falls back to
	// opening the path directly for directories.
	return execOpen("-R", path)
}

// OpenPath opens a file or directory with the system default handler.
func (a *App) OpenPath(path string) error {
	return execOpen(path)
}

// OpenURL opens a URL in the default browser.
func (a *App) OpenURL(url string) error {
	return execOpen(url)
}

// OpenClaudeTrace launches the analyzer app if installed. Falls back to
// revealing the sessions directory when not installed.
func (a *App) OpenClaudeTrace(fallbackDir string) error {
	return a.OpenClaudeTraceForProject("", fallbackDir)
}

// OpenClaudeTraceForProject opens the analyzer at the given project. Prefers
// redirecting an already-running instance (via /api/v1/navigate) so the user
// doesn't get a second window; only spawns a fresh process when no live
// instance is detected.
//
// Redirect requires analyzer commit with the navigate endpoint (see
// PortFilePath + /api/v1/navigate). Deep-link launch requires analyzer commit
// dc4cf19 or later. Falls back to opening the sessions dir if neither works.
func (a *App) OpenClaudeTraceForProject(projectSlug, fallbackDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	// 1. Already running? Redirect it in place.
	if addr, alive := claudetrace.RunningInstance(ctx); alive {
		nav := claudetrace.NavDashboard()
		if projectSlug != "" {
			nav = claudetrace.NavProject(projectSlug)
		}
		if err := claudetrace.Navigate(ctx, addr, nav); err == nil {
			// Bring the app forward from Chief's side too — Wails
			// WindowShow inside the analyzer handles it, but this belt-
			// and-suspenders `open -a` doesn't hurt if that's a no-op.
			_ = execOpen("-a", "claude-trace")
			return nil
		}
		// Navigate failed (endpoint missing on older binary, etc.).
		// Fall through and launch a fresh instance.
	}

	// 2. Not running (or old binary): launch fresh with --project.
	binPath := claudetrace.AnalyzerAppBinary()
	if binPath == "" {
		// No .app; try the CLI as a graceful fallback.
		if _, err := exec.LookPath("ct"); err == nil {
			if projectSlug != "" {
				return exec.Command("ct", "sessions", projectSlug).Start()
			}
			return exec.Command("ct", "projects").Start()
		}
		if fallbackDir != "" {
			return execOpen(fallbackDir)
		}
		return nil
	}
	args := []string{}
	if projectSlug != "" {
		args = append(args, "--project", projectSlug)
	}
	cmd := exec.Command(binPath, args...)
	cmd.Env = os.Environ()
	return cmd.Start()
}

// InstallAnalyzer runs the analyzer install in chiefd. Blocks until done
// (typically 5-60s: clone + go build + optional wails build). Returns the
// installer log + status so the UI can show progress.
func (a *App) InstallAnalyzer() (methods.AnalyzerInstallResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.AnalyzerInstallResponse{}, err
	}
	defer c.Close()
	var resp methods.AnalyzerInstallResponse
	if err := c.Call("analyzer.install", methods.AnalyzerInstallRequest{}, &resp); err != nil {
		return methods.AnalyzerInstallResponse{}, err
	}
	return resp, nil
}

// HistoryViewerStatus reports whether the zsh history viewer is installed.
func (a *App) HistoryViewerStatus() (methods.HistoryViewerStatusResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.HistoryViewerStatusResponse{}, err
	}
	defer c.Close()
	var resp methods.HistoryViewerStatusResponse
	if err := c.Call("historyviewer.status", methods.HistoryViewerStatusRequest{}, &resp); err != nil {
		return methods.HistoryViewerStatusResponse{}, err
	}
	return resp, nil
}

// InstallHistoryViewer runs the viewer install (brew if available, else
// clone+go build). Blocks until done (typically 5-30s).
func (a *App) InstallHistoryViewer() (methods.HistoryViewerInstallResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.HistoryViewerInstallResponse{}, err
	}
	defer c.Close()
	var resp methods.HistoryViewerInstallResponse
	if err := c.Call("historyviewer.install", methods.HistoryViewerInstallRequest{}, &resp); err != nil {
		return methods.HistoryViewerInstallResponse{}, err
	}
	return resp, nil
}

// OpenHistoryViewer spawns (or reuses) the viewer scoped to the project's
// directory and opens the deep-link URL in the default browser.
func (a *App) OpenHistoryViewer(projectID string) (methods.HistoryViewerOpenResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.HistoryViewerOpenResponse{}, err
	}
	defer c.Close()
	var resp methods.HistoryViewerOpenResponse
	if err := c.Call("historyviewer.open", methods.HistoryViewerOpenRequest{IDOrPath: projectID}, &resp); err != nil {
		return methods.HistoryViewerOpenResponse{}, err
	}
	// Fire the browser from Chief's side (macOS `open` respects the default
	// browser). Best-effort — errors here don't invalidate the RPC result.
	if resp.URL != "" {
		_ = execOpen(resp.URL)
	}
	return resp, nil
}

// ReadProjectFile returns the raw text of a specific file (constitution.md,
// PROJECT.md, etc.) inside a registered project. Restricted to a whitelist so
// the UI can't turn into an arbitrary file reader.
func (a *App) ReadProjectFile(projectID, filename string) (string, error) {
	if !allowedProjectFile(filename) {
		return "", fmt.Errorf("file not allowed: %s", filename)
	}
	c, err := a.dial()
	if err != nil {
		return "", err
	}
	defer c.Close()
	var resp methods.ProjectListResponse
	if err := c.Call("project.list", nil, &resp); err != nil {
		return "", err
	}
	var repoPath string
	for _, p := range resp.Projects {
		if p.ID == projectID || p.Name == projectID || p.Path == projectID {
			repoPath = p.Path
			break
		}
	}
	if repoPath == "" {
		return "", fmt.Errorf("project not found: %s", projectID)
	}
	b, err := os.ReadFile(filepath.Join(repoPath, filename))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}

// CodeGraphStatus reports install state + prereq availability.
func (a *App) CodeGraphStatus() (methods.CodeGraphStatusResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.CodeGraphStatusResponse{}, err
	}
	defer c.Close()
	var resp methods.CodeGraphStatusResponse
	if err := c.Call("codegraph.status", methods.CodeGraphStatusRequest{}, &resp); err != nil {
		return methods.CodeGraphStatusResponse{}, err
	}
	return resp, nil
}

// InstallCodeGraph clones + builds code_graph_search. Long-lived (up
// to 15min); the UI shows a progress modal.
func (a *App) InstallCodeGraph() (methods.CodeGraphInstallResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.CodeGraphInstallResponse{}, err
	}
	defer c.Close()
	var resp methods.CodeGraphInstallResponse
	if err := c.Call("codegraph.install", methods.CodeGraphInstallRequest{}, &resp); err != nil {
		return methods.CodeGraphInstallResponse{}, err
	}
	return resp, nil
}

// OpenCodeGraph launches (or reuses) code_graph_search for a project.
// When Code Graph Search.app is installed (upstream commit 6e27e4c),
// the .app opens itself in a native JavaFX WebView window — we skip
// opening the browser. Fallback (mode=web): open the URL in the
// default browser.
func (a *App) OpenCodeGraph(projectID string) (methods.CodeGraphOpenResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.CodeGraphOpenResponse{}, err
	}
	defer c.Close()
	var resp methods.CodeGraphOpenResponse
	if err := c.Call("codegraph.open", methods.CodeGraphOpenRequest{IDOrPath: projectID}, &resp); err != nil {
		return methods.CodeGraphOpenResponse{}, err
	}
	if resp.Mode != "app" && resp.URL != "" {
		_ = execOpen(resp.URL)
	}
	return resp, nil
}

// LogSearchStatus reports install state + prereq availability +
// whether the .app is currently running.
func (a *App) LogSearchStatus() (methods.LogSearchStatusResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.LogSearchStatusResponse{}, err
	}
	defer c.Close()
	var resp methods.LogSearchStatusResponse
	if err := c.Call("logsearch.status", methods.LogSearchStatusRequest{}, &resp); err != nil {
		return methods.LogSearchStatusResponse{}, err
	}
	return resp, nil
}

// InstallLogSearch clones + builds local_log_search (mvn + jpackage).
// Long-lived (up to 15min); UI shows the install modal spinner.
func (a *App) InstallLogSearch() (methods.LogSearchInstallResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.LogSearchInstallResponse{}, err
	}
	defer c.Close()
	var resp methods.LogSearchInstallResponse
	if err := c.Call("logsearch.install", methods.LogSearchInstallRequest{}, &resp); err != nil {
		return methods.LogSearchInstallResponse{}, err
	}
	return resp, nil
}

// OpenLogSearch launches (or focuses) Little Log Peep.app. Not
// project-scoped — log-search maintains its own sources via the app UI.
func (a *App) OpenLogSearch() (methods.LogSearchOpenResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.LogSearchOpenResponse{}, err
	}
	defer c.Close()
	var resp methods.LogSearchOpenResponse
	if err := c.Call("logsearch.open", methods.LogSearchOpenRequest{}, &resp); err != nil {
		return methods.LogSearchOpenResponse{}, err
	}
	return resp, nil
}

// StatsDetailed returns the aggregated analytics payload used by the
// dashboard modal. days=0 uses the server default (14).
func (a *App) StatsDetailed(days int) (methods.StatsDetailedResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.StatsDetailedResponse{}, err
	}
	defer c.Close()
	var resp methods.StatsDetailedResponse
	if err := c.Call("stats.detailed", methods.StatsDetailedRequest{Days: days}, &resp); err != nil {
		return methods.StatsDetailedResponse{}, err
	}
	return resp, nil
}

// NextUp returns the top-N pending tasks across all projects, ranked
// (priority DESC, source_line ASC). Powers the Next Up modal.
func (a *App) NextUp(limit int) ([]methods.BacklogRow, error) {
	c, err := a.dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var resp methods.BacklogNextResponse
	if err := c.Call("backlog.next", methods.BacklogNextRequest{Limit: limit}, &resp); err != nil {
		return nil, err
	}
	return resp.Tasks, nil
}

// ---------- flags (attention inbox) ----------

// ListFlags returns attention flags across projects. openOnly filters to
// unresolved; empty projectID = all projects.
func (a *App) ListFlags(projectID string, openOnly bool) ([]methods.FlagRow, error) {
	c, err := a.dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var resp methods.FlagListResponse
	req := methods.FlagListRequest{ProjectID: projectID, OpenOnly: openOnly}
	if err := c.Call("flag.list", req, &resp); err != nil {
		return nil, err
	}
	return resp.Flags, nil
}

// CountOpenFlags returns the number of unresolved flags. Powers the toolbar badge.
func (a *App) CountOpenFlags() (int, error) {
	c, err := a.dial()
	if err != nil {
		return 0, err
	}
	defer c.Close()
	var resp methods.FlagCountResponse
	if err := c.Call("flag.count", methods.FlagCountRequest{}, &resp); err != nil {
		return 0, err
	}
	return resp.Open, nil
}

// AnswerFlag resolves a flag with a reply. Resolution defaults to "answered".
func (a *App) AnswerFlag(flagID, reply, resolution string) (methods.FlagRow, error) {
	c, err := a.dial()
	if err != nil {
		return methods.FlagRow{}, err
	}
	defer c.Close()
	var resp methods.FlagAnswerResponse
	if err := c.Call("flag.answer", methods.FlagAnswerRequest{
		FlagID: flagID, Reply: reply, Resolution: resolution,
	}, &resp); err != nil {
		return methods.FlagRow{}, err
	}
	return resp.Flag, nil
}

// ApproveNextTask acks a next-task flag AND injects the suggested task
// into the project's bound cmux surface.
func (a *App) ApproveNextTask(flagID string) (methods.NextApproveResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.NextApproveResponse{}, err
	}
	defer c.Close()
	var resp methods.NextApproveResponse
	if err := c.Call("next.approve", methods.NextApproveRequest{FlagID: flagID}, &resp); err != nil {
		return methods.NextApproveResponse{}, err
	}
	return resp, nil
}

// SkipNextTask acks a next-task flag with resolution=skipped.
func (a *App) SkipNextTask(flagID, reason string) (methods.NextSkipResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.NextSkipResponse{}, err
	}
	defer c.Close()
	var resp methods.NextSkipResponse
	if err := c.Call("next.skip", methods.NextSkipRequest{FlagID: flagID, Reason: reason}, &resp); err != nil {
		return methods.NextSkipResponse{}, err
	}
	return resp, nil
}

// SnoozeNextTask holds off notifications for a project (or by flag) for N minutes.
func (a *App) SnoozeNextTask(flagID, project string, minutes int) (methods.NextSnoozeResponse, error) {
	c, err := a.dial()
	if err != nil {
		return methods.NextSnoozeResponse{}, err
	}
	defer c.Close()
	var resp methods.NextSnoozeResponse
	if err := c.Call("next.snooze", methods.NextSnoozeRequest{
		FlagID: flagID, Project: project, Minutes: minutes,
	}, &resp); err != nil {
		return methods.NextSnoozeResponse{}, err
	}
	return resp, nil
}

// ---------- helpers ----------

func (a *App) dial() (*ipc.Client, error) {
	sockPath, err := ipc.SocketPath()
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", sockPath, 1*time.Second)
	if err != nil {
		return nil, fmt.Errorf("chiefd unreachable (%s): %w", sockPath, err)
	}
	return ipc.NewClient(conn), nil
}

func allowedProjectFile(name string) bool {
	switch name {
	case "constitution.md", "PROJECT.md", "backlog.md", "CLAUDE.md", "README.md":
		return true
	}
	return false
}
