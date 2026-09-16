// Package methods holds the RPC request/response types shared between chiefd
// (server) and chief (CLI client). Putting them here keeps the wire schema in
// one place so both sides can evolve together.
package methods

import (
	"github.com/geekychris/chief/internal/store"
)

// ---------- project.add ----------

type ProjectAddRequest struct {
	Path      string `json:"path"`
	Name      string `json:"name,omitempty"`
	SpawnMode string `json:"spawn_mode,omitempty"`
}

type ProjectAddResponse struct {
	Project store.Project `json:"project"`
	// TasksImported is the count of tasks parsed from backlog.md on first
	// scan. Zero on registration of an empty project.
	TasksImported int `json:"tasks_imported"`
}

// ---------- project.list ----------

type ProjectListRequest struct{}

type ProjectListResponse struct {
	Projects []ProjectSummary `json:"projects"`
}

// ProjectSummary is what the CLI shows in the `chief project list` table.
type ProjectSummary struct {
	store.Project
	// PendingTasks / DoneTasks / DeferredTasks are computed cross-join counts
	// for the summary table.
	PendingTasks  int `json:"pending_tasks"`
	DoneTasks     int `json:"done_tasks"`
	DeferredTasks int `json:"deferred_tasks"`
}

// ---------- project.remove ----------

type ProjectRemoveRequest struct {
	IDOrPath string `json:"id_or_path"`
}

type ProjectRemoveResponse struct {
	Removed bool `json:"removed"`
}

// ---------- backlog.list ----------

type BacklogListRequest struct {
	// ProjectID filters to one project (accepts id, path, or name — resolved
	// server-side via store.GetProject). Empty means all projects.
	ProjectID string `json:"project_id,omitempty"`
	// Status filters by task status: pending|active|blocked|deferred|done.
	// Empty means all statuses.
	Status string `json:"status,omitempty"`
}

type BacklogListResponse struct {
	Tasks []BacklogRow `json:"tasks"`
}

// BacklogRow is a task joined with its project name for display.
type BacklogRow struct {
	store.Task
	ProjectName string `json:"project_name"`
}

// ---------- task.show ----------

type TaskShowRequest struct {
	ID string `json:"id"`
}

type TaskShowResponse struct {
	Task        store.Task `json:"task"`
	ProjectName string     `json:"project_name"`
}

// ---------- project.rescan ----------

// ProjectRescanRequest forces a re-parse of a project's markdown even if
// fsnotify hasn't fired. Handy from the CLI for manual reconciliation.
type ProjectRescanRequest struct {
	IDOrPath string `json:"id_or_path"`
}

type ProjectRescanResponse struct {
	Tasks int `json:"tasks"`
}

// ---------- task.add ----------

type TaskAddRequest struct {
	ProjectID         string   `json:"project_id"`          // id | name | path
	Title             string   `json:"title"`
	Body              string   `json:"body,omitempty"`
	Category          string   `json:"category,omitempty"`
	Priority          int      `json:"priority,omitempty"`
	RequiredResources []string `json:"required_resources,omitempty"`
	Due               string   `json:"due,omitempty"`
}

type TaskAddResponse struct {
	Task store.Task `json:"task"`
}

// ---------- task.update ----------

// TaskUpdateRequest updates a task's checkbox-line fields in backlog.md.
// UpdateBody must be explicitly set to have Body applied — nil pointer =
// "don't touch body". Send empty string with UpdateBody=true to clear.
type TaskUpdateRequest struct {
	TaskID            string   `json:"task_id"`
	Title             string   `json:"title"`
	Priority          int      `json:"priority,omitempty"`
	Category          string   `json:"category,omitempty"`
	RequiredResources []string `json:"required_resources,omitempty"`
	Due               string   `json:"due,omitempty"`
	UpdateBody        bool     `json:"update_body,omitempty"`
	Body              string   `json:"body,omitempty"`
}

type TaskUpdateResponse struct {
	Task store.Task `json:"task"`
}

// ---------- task.reorder ----------

type TaskReorderRequest struct {
	TaskID    string `json:"task_id"`
	Direction string `json:"direction"` // "up" | "down"
}

type TaskReorderResponse struct {
	Task    store.Task `json:"task"`
	Applied bool       `json:"applied"` // false = no-op (task already at edge of its section)
}

// ---------- historyviewer.status / install / open ----------

type HistoryViewerStatusRequest struct{}
type HistoryViewerStatusResponse struct {
	Installed   bool   `json:"installed"`
	Binary      string `json:"binary,omitempty"`
	HasApp      bool   `json:"has_app"`
	AppPath     string `json:"app_path,omitempty"`
	HasBrew     bool   `json:"has_brew"`
	InstallURL  string `json:"install_url"`
}

type HistoryViewerInstallRequest struct{}
type HistoryViewerInstallResponse struct {
	OK         bool     `json:"ok"`
	CLIPath    string   `json:"cli_path,omitempty"`
	Log        string   `json:"log"`
	Steps      []string `json:"steps"`
	DurationMS int64    `json:"duration_ms"`
	Error      string   `json:"error,omitempty"`
}

// HistoryViewerOpenRequest kicks off the viewer scoped to a project directory.
// If IDOrPath is provided, the project's on-disk path is used as --filter-dir.
type HistoryViewerOpenRequest struct {
	IDOrPath string `json:"id_or_path"`
}

type HistoryViewerOpenResponse struct {
	URL       string `json:"url,omitempty"`       // the URL Chief opened in the browser (web-fallback path)
	FilterDir string `json:"filter_dir"`
	Port      int    `json:"port,omitempty"`
	Spawned   bool   `json:"spawned"`             // true = we launched a fresh process; false = existing instance reused
	Mode      string `json:"mode"`                // "app" (native Wails window) | "web" (browser)
}

// ---------- analyzer.install ----------

// AnalyzerInstallRequest kicks off the analyzer install (clone + go build).
// No parameters — behavior is fixed (see internal/installer).
type AnalyzerInstallRequest struct{}

// AnalyzerInstallResponse mirrors installer.InstallResult so the CLI/UI don't
// have to import the internal package.
type AnalyzerInstallResponse struct {
	OK         bool     `json:"ok"`
	CLIPath    string   `json:"cli_path,omitempty"`
	AppPath    string   `json:"app_path,omitempty"`
	Log        string   `json:"log"`
	Steps      []string `json:"steps"`
	DurationMS int64    `json:"duration_ms"`
	Error      string   `json:"error,omitempty"`
}

// ---------- project.sessions ----------

// ProjectSessionsRequest asks for the Claude Code session JSONL inventory
// for a project — the "click to jump into claude-trace" data source.
type ProjectSessionsRequest struct {
	IDOrPath string `json:"id_or_path"`
}

// SessionInfo mirrors internal/claudetrace.Session for the wire.
type SessionInfo struct {
	ID       string `json:"id"`       // session UUID (filename without .jsonl)
	Path     string `json:"path"`     // absolute path to the .jsonl
	Size     int64  `json:"size"`     // bytes
	Modified string `json:"modified"` // RFC3339
}

type ProjectSessionsResponse struct {
	ProjectID         string        `json:"project_id"`
	Slug              string        `json:"slug"`                // Claude Code project dir slug
	SessionsDir       string        `json:"sessions_dir"`        // ~/.claude/projects/<slug>
	Sessions          []SessionInfo `json:"sessions"`
	AnalyzerInstalled bool          `json:"analyzer_installed"`  // ct or claude-trace.app present
	AnalyzerInvocation string       `json:"analyzer_invocation,omitempty"` // e.g. "ct" or "open -a claude-trace.app"
	AnalyzerRepoURL   string        `json:"analyzer_repo_url"`   // for install hint
}

// ---------- cmux.list / cmux.candidates / cmux.bind / task.send ----------

// CmuxSurface is the wire-facing subset of internal/cmux.Surface, mirrored
// so the CLI/UI don't have to import the internal package.
type CmuxSurface struct {
	Ref             string `json:"ref"`
	Title           string `json:"title"`
	CWD             string `json:"cwd"`
	Type            string `json:"type"`
	Focused         bool   `json:"focused"`
	IsClaude        bool   `json:"is_claude"`
	ClaudeSessionID string `json:"claude_session_id,omitempty"`
	AgentName       string `json:"agent_name,omitempty"`
	Launcher        string `json:"launcher,omitempty"`
}

type CmuxListRequest struct{}
type CmuxListResponse struct {
	Surfaces []CmuxSurface `json:"surfaces"`
}

type CmuxCandidatesRequest struct {
	ProjectID  string `json:"project_id"`
	ClaudeOnly bool   `json:"claude_only,omitempty"`
}

type CmuxCandidatesResponse struct {
	ProjectID        string        `json:"project_id"`
	ProjectPath      string        `json:"project_path"`
	CurrentSurface   string        `json:"current_surface,omitempty"` // as persisted in .chief/project.yaml
	CWDMatches       []CmuxSurface `json:"cwd_matches"`
	AllSurfaces      []CmuxSurface `json:"all_surfaces"`
}

type CmuxBindRequest struct {
	ProjectID  string `json:"project_id"`
	SurfaceRef string `json:"surface_ref"`
}

type CmuxBindResponse struct {
	ProjectID  string `json:"project_id"`
	SurfaceRef string `json:"surface_ref"`
}

// TaskSendRequest asks Chief to inject a "please work on this task" prompt
// into the project's bound cmux surface. If no binding exists, chiefd returns
// an error with a machine-readable code so the UI can prompt for one.
type TaskSendRequest struct {
	TaskID           string `json:"task_id"`
	SurfaceOverride  string `json:"surface_override,omitempty"` // if set, use this surface (and persist it)
	ExtraInstruction string `json:"extra_instruction,omitempty"`
}

type TaskSendResponse struct {
	SurfaceRef string `json:"surface_ref"`
	Prompt     string `json:"prompt"`     // exactly what we sent (for the UI to confirm)
}

// ErrCodeCmuxUnbound is returned in RPCError.Code when task.send is called
// on a project with no cmux surface binding yet. The UI should call
// cmux.candidates + cmux.bind then retry.
const ErrCodeCmuxUnbound = -33001

// ErrCodeCmuxSurfaceGone is returned when the previously bound surface no
// longer appears in `cmux list-panels`.
const ErrCodeCmuxSurfaceGone = -33002
