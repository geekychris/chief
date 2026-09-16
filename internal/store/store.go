// Package store is the SQLite persistence layer for chiefd.
//
// The daemon is the only process that opens the database directly; the CLI
// talks to chiefd over the unix socket. WAL mode and foreign keys are set at
// connection open via DSN pragmas.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"crypto/rand"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// currentSchemaVersion is the PRAGMA user_version we expect after migrations.
// Bump this and add a branch in migrate() when the schema changes.
const currentSchemaVersion = 3

// Store wraps *sql.DB and provides typed DAO methods.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (or creates) the database at dbPath. WAL mode + foreign keys are
// enforced via DSN. On first open the schema is applied.
func Open(dbPath string) (*Store, error) {
	// modernc's SQLite driver accepts pragmas as DSN query params; each
	// _pragma appends a `PRAGMA ...;` executed on connection init.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
		filepath.ToSlash(dbPath),
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	// Keep the pool small; one writer at a time avoids most contention.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)

	s := &Store{db: db, path: dbPath}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database pool.
func (s *Store) Close() error { return s.db.Close() }

// DB returns the raw *sql.DB. Tests and future ad-hoc queries may need it;
// production code should prefer the DAO methods below.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate(ctx context.Context) error {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if v >= currentSchemaVersion {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// v0 → v1: initial schema. schema.sql uses CREATE TABLE IF NOT EXISTS
	// so it's safe to apply on both fresh and pre-existing DBs.
	if v < 1 {
		if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
			return fmt.Errorf("apply v1 schema: %w", err)
		}
	}
	// v1 → v2: add tasks.source_line so the UI can sort by doc order (what
	// the user sees when editing backlog.md), not by created_at.  Doubles
	// as the anchor for reorder operations.
	if v < 2 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN source_line INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("apply v2 (source_line): %w", err)
		}
		if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS tasks_project_order ON tasks(project_id, source_line)`); err != nil {
			return fmt.Errorf("apply v2 (index): %w", err)
		}
	}

	// v2 → v3: attention flags table. Pre-existing DBs don't have the table
	// yet; schema.sql now creates it (IF NOT EXISTS is safe).
	if v < 3 {
		if _, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS flags (
    id            TEXT PRIMARY KEY,
    project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id    TEXT,
    kind          TEXT NOT NULL DEFAULT 'question',
    urgency       TEXT NOT NULL DEFAULT 'attention',
    question      TEXT NOT NULL DEFAULT '',
    suggested_id  TEXT,
    agg_key       TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    ack_at        TEXT,
    ack_reply     TEXT NOT NULL DEFAULT '',
    resolution    TEXT NOT NULL DEFAULT ''
)`); err != nil {
			return fmt.Errorf("apply v3 (flags): %w", err)
		}
		if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS flags_open_by_urgency ON flags(ack_at, urgency, created_at)`); err != nil {
			return fmt.Errorf("apply v3 (flags_open_by_urgency): %w", err)
		}
		if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS flags_project_open ON flags(project_id, ack_at)`); err != nil {
			return fmt.Errorf("apply v3 (flags_project_open): %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", currentSchemaVersion)); err != nil {
		return fmt.Errorf("bump user_version: %w", err)
	}
	return tx.Commit()
}

// ---------- Project DAO ----------

// Project is the persisted row. Types match the JSON returned to the CLI.
type Project struct {
	ID              string     `json:"id"`
	Path            string     `json:"path"`
	Name            string     `json:"name"`
	SpawnMode       string     `json:"spawn_mode"`
	CmuxWorkspaceID *string    `json:"cmux_workspace_id,omitempty"`
	State           string     `json:"state"`
	LastSeen        *time.Time `json:"last_seen,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

// ErrNotFound is returned by lookup methods when no row matches.
var ErrNotFound = errors.New("not found")

// CreateProject inserts a new project row. Returns ErrConflict if the path
// or id is already registered.
var ErrConflict = errors.New("conflict")

func (s *Store) CreateProject(ctx context.Context, p Project) error {
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO projects(id, path, name, spawn_mode, cmux_workspace_id, state, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Path, p.Name, p.SpawnMode, p.CmuxWorkspaceID, p.State, p.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: project path or id already exists", ErrConflict)
		}
		return err
	}
	return nil
}

// ListProjects returns every project ordered by name.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, path, name, spawn_mode, cmux_workspace_id, state, last_seen, created_at
		FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProject looks up by id or by path. Empty selectors return ErrNotFound.
func (s *Store) GetProject(ctx context.Context, idOrPath string) (Project, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, path, name, spawn_mode, cmux_workspace_id, state, last_seen, created_at
		FROM projects WHERE id = ? OR path = ? OR name = ?`,
		idOrPath, idOrPath, idOrPath,
	)
	p, err := scanProject(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	return p, err
}

// DeleteProject removes a project and (via ON DELETE CASCADE) its tasks.
// Idempotent: returns nil even if no row matched.
func (s *Store) DeleteProject(ctx context.Context, idOrPath string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM projects WHERE id = ? OR path = ? OR name = ?`,
		idOrPath, idOrPath, idOrPath,
	)
	return err
}

// scanProject reads columns in the fixed order used by ListProjects/GetProject.
// Pass either *sql.Row.Scan or *sql.Rows.Scan.
func scanProject(scan func(...any) error) (Project, error) {
	var p Project
	var cmuxID sql.NullString
	var lastSeen sql.NullString
	var createdAt string
	if err := scan(&p.ID, &p.Path, &p.Name, &p.SpawnMode, &cmuxID, &p.State, &lastSeen, &createdAt); err != nil {
		return Project{}, err
	}
	if cmuxID.Valid {
		p.CmuxWorkspaceID = &cmuxID.String
	}
	if lastSeen.Valid {
		if t, err := time.Parse(time.RFC3339, lastSeen.String); err == nil {
			p.LastSeen = &t
		}
	}
	if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		p.CreatedAt = t
	}
	return p, nil
}

// ---------- Task DAO ----------

// TaskStatus values persisted in tasks.status.
type TaskStatus string

const (
	TaskPending  TaskStatus = "pending"
	TaskActive   TaskStatus = "active"
	TaskBlocked  TaskStatus = "blocked"
	TaskDeferred TaskStatus = "deferred"
	TaskDone     TaskStatus = "done"
	TaskDropped  TaskStatus = "dropped"
)

// Task is the persisted row.
type Task struct {
	ID                string     `json:"id"`
	ProjectID         string     `json:"project_id"`
	Title             string     `json:"title"`
	Body              string     `json:"body,omitempty"`
	SourceFile        string     `json:"source_file"`
	SourceLineHash    string     `json:"source_line_hash"`
	SourceLine        int        `json:"source_line"` // 1-based line of the checkbox in SourceFile
	Status            TaskStatus `json:"status"`
	Priority          int        `json:"priority"`
	Category          string     `json:"category,omitempty"`
	RequiredResources []string   `json:"required_resources,omitempty"`
	Due               *string    `json:"due,omitempty"`
	ClaimedAt         *time.Time `json:"claimed_at,omitempty"`
	ClaimedBySession  *string    `json:"claimed_by_session,omitempty"`
	ReviveCount       int        `json:"revive_count"`
	CreatedAt         time.Time  `json:"created_at"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
}

// UpsertTasks replaces the current backlog snapshot for a project with the
// provided set — matched by id.  Tasks in `desired` that aren't in the DB
// are INSERTed; matches are UPDATEd; existing rows for the project whose id
// isn't in `desired` are hard-deleted (they were removed from the file).
//
// This is what fswatch → parser → store calls after each file change.
// Runs in a single transaction so partial failures don't corrupt state.
func (s *Store) UpsertTasks(ctx context.Context, projectID string, desired []Task) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	keep := map[string]struct{}{}
	for _, t := range desired {
		keep[t.ID] = struct{}{}
	}

	// Load current ids for the project so we know what to delete.
	existing, err := loadProjectTaskIDs(ctx, tx, projectID)
	if err != nil {
		return err
	}
	for id := range existing {
		if _, ok := keep[id]; !ok {
			if _, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE id = ? AND project_id = ?`, id, projectID); err != nil {
				return fmt.Errorf("delete task %s: %w", id, err)
			}
		}
	}

	for _, t := range desired {
		if t.CreatedAt.IsZero() {
			t.CreatedAt = time.Now().UTC()
		}
		resJSON, err := json.Marshal(t.RequiredResources)
		if err != nil {
			return err
		}
		var completedAt any
		if t.CompletedAt != nil {
			completedAt = t.CompletedAt.Format(time.RFC3339)
		}
		if _, ok := existing[t.ID]; ok {
			if _, err := tx.ExecContext(ctx, `
				UPDATE tasks
				   SET title = ?, body = ?, source_file = ?, source_line_hash = ?,
				       source_line = ?, status = ?, priority = ?, category = ?,
				       required_resources = ?, due = ?, completed_at = ?
				 WHERE id = ? AND project_id = ?`,
				t.Title, t.Body, t.SourceFile, t.SourceLineHash, t.SourceLine,
				string(t.Status), t.Priority, t.Category, string(resJSON),
				t.Due, completedAt, t.ID, projectID,
			); err != nil {
				return fmt.Errorf("update task %s: %w", t.ID, err)
			}
		} else {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO tasks(id, project_id, title, body, source_file, source_line_hash,
				                  source_line, status, priority, category, required_resources, due,
				                  created_at, completed_at)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				t.ID, projectID, t.Title, t.Body, t.SourceFile, t.SourceLineHash,
				t.SourceLine, string(t.Status), t.Priority, t.Category, string(resJSON),
				t.Due, t.CreatedAt.Format(time.RFC3339), completedAt,
			); err != nil {
				return fmt.Errorf("insert task %s: %w", t.ID, err)
			}
		}
	}
	return tx.Commit()
}

func loadProjectTaskIDs(ctx context.Context, tx *sql.Tx, projectID string) (map[string]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tasks WHERE project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

// ListTasks returns tasks matching the filter. Any zero-value filter field is
// unconstrained. Ordered by project name, then priority DESC, then created_at.
type TaskFilter struct {
	ProjectID string
	Status    TaskStatus
}

func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]Task, error) {
	q := `
		SELECT t.id, t.project_id, t.title, t.body, t.source_file, t.source_line_hash,
		       t.source_line, t.status, t.priority, t.category, t.required_resources, t.due,
		       t.claimed_at, t.claimed_by_session, t.revive_count,
		       t.created_at, t.completed_at
		  FROM tasks t
		  JOIN projects p ON p.id = t.project_id
		 WHERE 1=1`
	var args []any
	if f.ProjectID != "" {
		q += " AND t.project_id = ?"
		args = append(args, f.ProjectID)
	}
	if f.Status != "" {
		q += " AND t.status = ?"
		args = append(args, string(f.Status))
	}
	// Sort: non-done first (pending/active/blocked/deferred), then done;
	// within each group, by project name then doc order (source_line). This
	// keeps completed items visually last while still inline in the table.
	q += ` ORDER BY (t.status = 'done'), p.name, t.source_line ASC, t.created_at ASC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// NextPending returns the top-N pending tasks across ALL projects ranked
// by (priority DESC, source_line ASC, created_at ASC). This powers the
// "which project should I focus on right now" view — the CLI subcommand
// `chief backlog next` and the UI's Next Up modal.
//
// Only status='pending' tasks are returned; active/blocked/deferred/done/
// dropped are excluded. limit ≤ 0 means "no limit" (returns everything).
func (s *Store) NextPending(ctx context.Context, limit int) ([]Task, error) {
	q := `
		SELECT id, project_id, title, body, source_file, source_line_hash,
		       source_line, status, priority, category, required_resources, due,
		       claimed_at, claimed_by_session, revive_count,
		       created_at, completed_at
		  FROM tasks
		 WHERE status = 'pending'
		 ORDER BY priority DESC, source_line ASC, created_at ASC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTask returns a single task by id.
func (s *Store) GetTask(ctx context.Context, id string) (Task, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, title, body, source_file, source_line_hash,
		       source_line, status, priority, category, required_resources, due,
		       claimed_at, claimed_by_session, revive_count,
		       created_at, completed_at
		  FROM tasks WHERE id = ?`, id)
	t, err := scanTask(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

// TaskIDExists is used by the parser during ID minting to avoid collisions.
func (s *Store) TaskIDExists(ctx context.Context, id string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func scanTask(scan func(...any) error) (Task, error) {
	var t Task
	var resJSON string
	var due, claimedAt, claimedBy, completedAt sql.NullString
	var createdAt string
	var status string
	if err := scan(
		&t.ID, &t.ProjectID, &t.Title, &t.Body, &t.SourceFile, &t.SourceLineHash,
		&t.SourceLine, &status, &t.Priority, &t.Category, &resJSON, &due,
		&claimedAt, &claimedBy, &t.ReviveCount,
		&createdAt, &completedAt,
	); err != nil {
		return Task{}, err
	}
	t.Status = TaskStatus(status)
	if resJSON != "" {
		_ = json.Unmarshal([]byte(resJSON), &t.RequiredResources)
	}
	if due.Valid {
		v := due.String
		t.Due = &v
	}
	if claimedAt.Valid {
		if v, err := time.Parse(time.RFC3339, claimedAt.String); err == nil {
			t.ClaimedAt = &v
		}
	}
	if claimedBy.Valid {
		v := claimedBy.String
		t.ClaimedBySession = &v
	}
	if v, err := time.Parse(time.RFC3339, createdAt); err == nil {
		t.CreatedAt = v
	}
	if completedAt.Valid {
		if v, err := time.Parse(time.RFC3339, completedAt.String); err == nil {
			t.CompletedAt = &v
		}
	}
	return t, nil
}

// ---------- Events DAO ----------

// InsertEvent appends to the audit log. project_id/session_id are optional.
func (s *Store) InsertEvent(ctx context.Context, projectID, sessionID, kind string, payload any) error {
	var raw []byte
	if payload != nil {
		var err error
		raw, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	} else {
		raw = []byte("{}")
	}
	var pid, sid any
	if projectID != "" {
		pid = projectID
	}
	if sessionID != "" {
		sid = sessionID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO events(ts, project_id, session_id, kind, payload)
		VALUES(?, ?, ?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano), pid, sid, kind, string(raw),
	)
	return err
}

// ---------- Flags DAO ----------

// FlagKind values persisted in flags.kind.
type FlagKind string

const (
	FlagKindQuestion FlagKind = "question"  // Claude asked the human something
	FlagKindNextTask FlagKind = "next_task" // Chief suggests the next backlog item
)

// FlagUrgency values persisted in flags.urgency. Matches the three-tier
// notification model used by the notify package.
type FlagUrgency string

const (
	UrgencyInfo      FlagUrgency = "info"      // badge only, no notification
	UrgencyAttention FlagUrgency = "attention" // macOS notification
	UrgencyUrgent    FlagUrgency = "urgent"    // notification + push (later)
)

// FlagResolution captures how a flag was ack'd. Empty until ack_at is set.
type FlagResolution string

const (
	ResolutionAnswered  FlagResolution = "answered"  // human replied via inbox
	ResolutionApproved  FlagResolution = "approved"  // next_task: approve suggestion
	ResolutionSkipped   FlagResolution = "skipped"   // next_task: skip (defer suggested)
	ResolutionSnoozed   FlagResolution = "snoozed"   // next_task: hold off for N minutes
	ResolutionDismissed FlagResolution = "dismissed" // manual dismiss w/o reply
)

// Flag is the persisted row.
type Flag struct {
	ID          string         `json:"id"`
	ProjectID   string         `json:"project_id"`
	SessionID   *string        `json:"session_id,omitempty"`
	Kind        FlagKind       `json:"kind"`
	Urgency     FlagUrgency    `json:"urgency"`
	Question    string         `json:"question"`
	SuggestedID *string        `json:"suggested_id,omitempty"`
	AggKey      string         `json:"agg_key"`
	CreatedAt   time.Time      `json:"created_at"`
	AckAt       *time.Time     `json:"ack_at,omitempty"`
	AckReply    string         `json:"ack_reply,omitempty"`
	Resolution  FlagResolution `json:"resolution,omitempty"`
}

// InsertFlag creates a flag row. Caller supplies the id (typically minted by
// NewFlagID). Returns ErrConflict if the id collides.
func (s *Store) InsertFlag(ctx context.Context, f Flag) error {
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now().UTC()
	}
	if f.Kind == "" {
		f.Kind = FlagKindQuestion
	}
	if f.Urgency == "" {
		f.Urgency = UrgencyAttention
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO flags(id, project_id, session_id, kind, urgency, question,
		                  suggested_id, agg_key, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.ProjectID, sqlNull(f.SessionID), string(f.Kind), string(f.Urgency),
		f.Question, sqlNull(f.SuggestedID), f.AggKey,
		f.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil && isUniqueViolation(err) {
		return fmt.Errorf("%w: flag id already exists", ErrConflict)
	}
	return err
}

// FlagFilter narrows a ListFlags query.
type FlagFilter struct {
	ProjectID string // empty = all projects
	OpenOnly  bool   // true = ack_at IS NULL
	Kind      FlagKind
	Limit     int // 0 = no limit
}

// ListFlags returns flags matching the filter. Ordered by urgency
// (urgent > attention > info) then age (oldest first), so the top of the
// list is what the user should look at first.
func (s *Store) ListFlags(ctx context.Context, f FlagFilter) ([]Flag, error) {
	q := `
		SELECT id, project_id, session_id, kind, urgency, question,
		       suggested_id, agg_key, created_at, ack_at, ack_reply, resolution
		  FROM flags WHERE 1=1`
	var args []any
	if f.ProjectID != "" {
		q += " AND project_id = ?"
		args = append(args, f.ProjectID)
	}
	if f.OpenOnly {
		q += " AND ack_at IS NULL"
	}
	if f.Kind != "" {
		q += " AND kind = ?"
		args = append(args, string(f.Kind))
	}
	// urgency sort: urgent(0) < attention(1) < info(2). CASE expression so
	// we don't have to store a numeric priority alongside the text label.
	q += ` ORDER BY
		CASE urgency
			WHEN 'urgent'    THEN 0
			WHEN 'attention' THEN 1
			WHEN 'info'      THEN 2
			ELSE 3
		END ASC,
		created_at ASC`
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Flag
	for rows.Next() {
		fl, err := scanFlag(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, fl)
	}
	return out, rows.Err()
}

// GetFlag returns one flag by id.
func (s *Store) GetFlag(ctx context.Context, id string) (Flag, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, session_id, kind, urgency, question,
		       suggested_id, agg_key, created_at, ack_at, ack_reply, resolution
		  FROM flags WHERE id = ?`, id)
	f, err := scanFlag(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Flag{}, ErrNotFound
	}
	return f, err
}

// AckFlag marks a flag resolved with the given reply text + resolution code.
// Idempotent — re-ack'ing overwrites the previous ack (used when the user
// changes their mind, e.g. answers a flag twice).
func (s *Store) AckFlag(ctx context.Context, id, reply string, resolution FlagResolution) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE flags
		   SET ack_at = ?, ack_reply = ?, resolution = ?
		 WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), reply, string(resolution), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountOpenFlags returns the number of unresolved flags. projectID="" for all.
// The menu-bar badge polls this every few seconds.
func (s *Store) CountOpenFlags(ctx context.Context, projectID string) (int, error) {
	var n int
	var err error
	if projectID == "" {
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM flags WHERE ack_at IS NULL`).Scan(&n)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM flags WHERE ack_at IS NULL AND project_id = ?`, projectID).Scan(&n)
	}
	return n, err
}

// FindOpenFlagByAggKey looks up an unresolved flag with a given agg_key so
// notifications can be coalesced. Returns ErrNotFound if none.
func (s *Store) FindOpenFlagByAggKey(ctx context.Context, projectID, aggKey string) (Flag, error) {
	if aggKey == "" {
		return Flag{}, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, session_id, kind, urgency, question,
		       suggested_id, agg_key, created_at, ack_at, ack_reply, resolution
		  FROM flags
		 WHERE project_id = ? AND ack_at IS NULL AND agg_key = ?
		 ORDER BY created_at ASC LIMIT 1`, projectID, aggKey)
	f, err := scanFlag(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Flag{}, ErrNotFound
	}
	return f, err
}

// CountOpenFlagsSince counts open flags for a project created after `since`.
// Used by the per-project rate limiter (attention pkg).
func (s *Store) CountOpenFlagsSince(ctx context.Context, projectID string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM flags
		 WHERE project_id = ? AND created_at >= ?`,
		projectID, since.Format(time.RFC3339Nano),
	).Scan(&n)
	return n, err
}

func scanFlag(scan func(...any) error) (Flag, error) {
	var f Flag
	var sess, sug, ackAt sql.NullString
	var createdAt string
	var kind, urg, res string
	if err := scan(
		&f.ID, &f.ProjectID, &sess, &kind, &urg, &f.Question,
		&sug, &f.AggKey, &createdAt, &ackAt, &f.AckReply, &res,
	); err != nil {
		return Flag{}, err
	}
	f.Kind = FlagKind(kind)
	f.Urgency = FlagUrgency(urg)
	f.Resolution = FlagResolution(res)
	if sess.Valid {
		v := sess.String
		f.SessionID = &v
	}
	if sug.Valid {
		v := sug.String
		f.SuggestedID = &v
	}
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		f.CreatedAt = t
	} else if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		f.CreatedAt = t
	}
	if ackAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, ackAt.String); err == nil {
			f.AckAt = &t
		} else if t, err := time.Parse(time.RFC3339, ackAt.String); err == nil {
			f.AckAt = &t
		}
	}
	return f, nil
}

func sqlNull(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// NewFlagID returns "flag_" + 6 random hex chars.
func NewFlagID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return "flag_" + hex.EncodeToString(b[:])
}

// ---------- ID helpers ----------

// NewProjectID returns "proj_" + 8 random hex chars.
func NewProjectID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "proj_" + hex.EncodeToString(b[:])
}

// NewTaskID returns 4 random hex chars, matching the {id:xxxx} format in the
// backlog file spec. Collision handling is the caller's problem — check with
// TaskIDExists in a retry loop.
func NewTaskID() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// isUniqueViolation returns true for sqlite constraint errors related to
// UNIQUE (used by CreateProject to translate to ErrConflict).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// modernc.org/sqlite surfaces "UNIQUE constraint failed" in the error string.
	// This is enough for our single-writer use.
	return contains(msg, "UNIQUE constraint failed") || contains(msg, "constraint failed: UNIQUE")
}

// contains is a tiny helper to avoid importing strings for one call site.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
