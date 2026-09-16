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

// ---------- digest.fire / digest.preview ----------

// DigestPreviewRequest asks chiefd to compose (but not send) the digest.
type DigestPreviewRequest struct {
	WindowHours int `json:"window_hours,omitempty"`
}
type DigestPreviewResponse struct {
	Body string `json:"body"`
}

// DigestFireRequest triggers the digest via the messaging router right
// now, regardless of the scheduler.
type DigestFireRequest struct {
	WindowHours int `json:"window_hours,omitempty"`
}
type DigestFireResponse struct {
	Sent bool `json:"sent"`
}

// ---------- search ----------

type SearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"` // 0 → 100
}
type SearchResponse struct {
	Tasks []BacklogRow `json:"tasks"`
}

// ---------- stats.summary ----------

// StatsSummaryRequest — accepts a window (rolling in days, default 7)
// so the CLI can ask "how much did I get done this week vs last month".
type StatsSummaryRequest struct {
	WindowDays int `json:"window_days,omitempty"`
}

// StatsSummaryResponse — per-project rollups + a global summary. The
// wire shape is UI-agnostic; both the CLI table renderer and a future
// dashboard consume the same payload.
type StatsSummaryResponse struct {
	WindowDays int              `json:"window_days"`
	Global     StatsGlobal      `json:"global"`
	Projects   []StatsPerProject `json:"projects"`
}

type StatsGlobal struct {
	Projects       int    `json:"projects"`
	TasksPending   int    `json:"tasks_pending"`
	TasksActive    int    `json:"tasks_active"`
	TasksDeferred  int    `json:"tasks_deferred"`
	TasksBlocked   int    `json:"tasks_blocked"`
	TasksDone      int    `json:"tasks_done"`
	FlagsOpen      int    `json:"flags_open"`
	CompletedWindow int   `json:"completed_window"` // task.added→done events in window; proxy = rescan.completed events with tasks_swept>0
	EventsTotal    int64  `json:"events_total"`
}

type StatsPerProject struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Path           string `json:"path"`
	State          string `json:"state"`
	Pending        int    `json:"pending"`
	Active         int    `json:"active"`
	Deferred       int    `json:"deferred"`
	Blocked        int    `json:"blocked"`
	Done           int    `json:"done"`
	FlagsOpen      int    `json:"flags_open"`
	LastActivity   string `json:"last_activity,omitempty"` // RFC3339; empty = never
	CompletedWindow int   `json:"completed_window"`
}

// ---------- backlog.next ----------

// BacklogNextRequest asks for the top-N pending tasks system-wide, ranked
// by (priority DESC, source_line ASC). Limit 0 uses server default (10).
type BacklogNextRequest struct {
	Limit int `json:"limit,omitempty"`
}

type BacklogNextResponse struct {
	Tasks []BacklogRow `json:"tasks"`
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

// ---------- task.delete ----------

type TaskDeleteRequest struct {
	TaskID string `json:"task_id"`
}

type TaskDeleteResponse struct {
	Deleted bool `json:"deleted"`
}

// ---------- task.send_batch ----------

// TaskSendBatchRequest bundles N tasks into a single "please do these in order"
// prompt and injects it into the cmux surface bound to the (single) project
// all the tasks belong to.  Returns an error if the tasks span projects.
type TaskSendBatchRequest struct {
	TaskIDs         []string `json:"task_ids"`
	SurfaceOverride string   `json:"surface_override,omitempty"`
	ExtraInstruction string  `json:"extra_instruction,omitempty"`
}

type TaskSendBatchResponse struct {
	SurfaceRef string   `json:"surface_ref"`
	Prompt     string   `json:"prompt"`
	Count      int      `json:"count"`
	TaskIDs    []string `json:"task_ids"`
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

// ---------- flag.raise / flag.list / flag.answer / flag.count ----------

// FlagRaiseRequest raises an attention flag. Session is typically a Claude
// session id; ProjectID is required (accepts id/path/name — resolved
// server-side).
type FlagRaiseRequest struct {
	ProjectID string `json:"project_id"`
	SessionID string `json:"session_id,omitempty"`
	Kind      string `json:"kind,omitempty"`      // question|next_task; default question
	Urgency   string `json:"urgency,omitempty"`   // info|attention|urgent; default attention
	Question  string `json:"question"`
	// SuggestedID is populated when kind=next_task — the task chief is
	// suggesting the session pick up next.
	SuggestedID string `json:"suggested_id,omitempty"`
}

type FlagRaiseResponse struct {
	Flag           FlagRow `json:"flag"`
	Coalesced      bool    `json:"coalesced"`
	NotifiedMacOS  bool    `json:"notified_macos"`
	RateLimited    bool    `json:"rate_limited"`
	SnoozedProject bool    `json:"snoozed_project"`
	InDND          bool    `json:"in_dnd"`
}

// FlagRow is the wire form of store.Flag, joined with the project name so
// the UI doesn't have to fan out a second lookup per row.
type FlagRow struct {
	store.Flag
	ProjectName string `json:"project_name"`
}

type FlagListRequest struct {
	ProjectID string `json:"project_id,omitempty"`
	OpenOnly  bool   `json:"open_only,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type FlagListResponse struct {
	Flags []FlagRow `json:"flags"`
}

type FlagAnswerRequest struct {
	FlagID     string `json:"flag_id"`
	Reply      string `json:"reply"`
	Resolution string `json:"resolution,omitempty"` // default "answered"
}

type FlagAnswerResponse struct {
	Flag FlagRow `json:"flag"`
}

type FlagCountRequest struct {
	ProjectID string `json:"project_id,omitempty"`
}
type FlagCountResponse struct {
	Open int `json:"open"`
}

// ---------- next.approve / next.skip / next.snooze ----------
//
// Companions to the FlagKindNextTask flow. All operate on a specific
// next-task flag_id; ack the flag with the corresponding resolution and
// (for approve) inject the suggested task into the project's cmux surface.

type NextApproveRequest struct {
	FlagID string `json:"flag_id"`
}
type NextApproveResponse struct {
	Flag       FlagRow `json:"flag"`
	SurfaceRef string  `json:"surface_ref"`
	Prompt     string  `json:"prompt"`
}

type NextSkipRequest struct {
	FlagID string `json:"flag_id"`
	Reason string `json:"reason,omitempty"`
}
type NextSkipResponse struct {
	Flag FlagRow `json:"flag"`
	// Deferred true if the suggested task was moved to deferred status.
	Deferred bool `json:"deferred"`
}

type NextSnoozeRequest struct {
	FlagID  string `json:"flag_id,omitempty"`
	Project string `json:"project,omitempty"` // if FlagID unset, snooze a whole project
	Minutes int    `json:"minutes"`
}
type NextSnoozeResponse struct {
	UntilRFC3339 string `json:"until"`
	ProjectID    string `json:"project_id"`
}
