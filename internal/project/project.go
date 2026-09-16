// Package project handles the per-project runtime dir (.chief/) and the
// register/list/remove lifecycle. `.chief/project.yaml` is the on-disk anchor
// linking a repo dir to its Chief project id, spawn mode, and cmux surface.
//
// SQLite mirrors the yaml so `chief project list` is a single query;
// yaml is canonical for the id and settings so a repo can be moved between
// machines and re-registered without losing identity.
package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/geekychris/chief/internal/backlog"
	"github.com/geekychris/chief/internal/store"
	"gopkg.in/yaml.v3"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ProjectFile is the on-disk .chief/project.yaml schema.
type ProjectFile struct {
	ID        string   `yaml:"id"`
	Name      string   `yaml:"name"`
	SpawnMode string   `yaml:"spawn_mode"` // attach|headless|spawn-interactive
	Cmux      CmuxBind `yaml:"cmux"`
}

// CmuxBind records the cmux workspace/surface this project is associated with.
// Populated at spawn-interactive time or when the SessionStart hook heartbeats
// with CMUX_WORKSPACE_ID/CMUX_SURFACE_ID env.
type CmuxBind struct {
	WorkspaceID string `yaml:"workspace_id"`
	SurfaceID   string `yaml:"surface_id"`
}

// EchoSuppressor is the subset of fswatch.Watcher the Manager needs so it can
// tell the watcher "the next event on this path is caused by me — swallow it."
// nil is fine; suppression just becomes a no-op (Rescan then works standalone).
type EchoSuppressor interface {
	SuppressNext(path, sha256Hex string)
}

// Manager wraps the store with .chief/project.yaml orchestration.
type Manager struct {
	Store    *store.Store
	Watcher  EchoSuppressor
}

// New returns a Manager bound to the given store. The watcher is optional;
// wire it in from chiefd once fswatch is running.
func New(s *store.Store) *Manager { return &Manager{Store: s} }

// RegisterOpts controls Register behavior.
type RegisterOpts struct {
	Path      string // required, absolute
	Name      string // defaults to filepath.Base(Path)
	SpawnMode string // defaults to "attach"
}

// Register creates .chief/project.yaml (if missing), inserts into SQLite,
// and does a first backlog scan. Idempotent: re-registering an already-known
// path returns the existing project.
func (m *Manager) Register(ctx context.Context, opts RegisterOpts) (store.Project, error) {
	if opts.Path == "" {
		return store.Project{}, errors.New("Path required")
	}
	absPath, err := filepath.Abs(opts.Path)
	if err != nil {
		return store.Project{}, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return store.Project{}, fmt.Errorf("stat %s: %w", absPath, err)
	}
	if !info.IsDir() {
		return store.Project{}, fmt.Errorf("%s is not a directory", absPath)
	}

	// Idempotent: if already registered by path or by an existing .chief/project.yaml, reuse.
	if existing, err := m.Store.GetProject(ctx, absPath); err == nil {
		return existing, nil
	}

	name := opts.Name
	if name == "" {
		name = filepath.Base(absPath)
	}
	spawnMode := opts.SpawnMode
	if spawnMode == "" {
		spawnMode = "attach"
	}

	chiefDir := filepath.Join(absPath, ".chief")
	yamlPath := filepath.Join(chiefDir, "project.yaml")

	var pf ProjectFile
	if _, err := os.Stat(yamlPath); err == nil {
		// Yaml exists (probably from a prior registration on another machine);
		// reuse its id.
		if err := readYAML(yamlPath, &pf); err != nil {
			return store.Project{}, fmt.Errorf("read existing project.yaml: %w", err)
		}
	}
	if pf.ID == "" {
		pf.ID = store.NewProjectID()
	}
	if pf.Name == "" {
		pf.Name = name
	}
	if pf.SpawnMode == "" {
		pf.SpawnMode = spawnMode
	}

	if err := os.MkdirAll(filepath.Join(chiefDir, "inbox"), 0o755); err != nil {
		return store.Project{}, fmt.Errorf("mkdir .chief: %w", err)
	}
	if err := writeYAML(yamlPath, pf); err != nil {
		return store.Project{}, fmt.Errorf("write project.yaml: %w", err)
	}

	proj := store.Project{
		ID:        pf.ID,
		Path:      absPath,
		Name:      pf.Name,
		SpawnMode: pf.SpawnMode,
		State:     "idle",
	}
	if err := m.Store.CreateProject(ctx, proj); err != nil {
		return store.Project{}, err
	}

	// First scan: populate tasks from backlog.md if present.
	if _, err := m.Rescan(ctx, proj.ID); err != nil {
		slog.Warn("initial rescan failed", "project", proj.ID, "err", err)
	}

	_ = m.Store.InsertEvent(ctx, proj.ID, "", "project.registered", map[string]any{"path": absPath})
	return proj, nil
}

// ReadYAML returns the parsed .chief/project.yaml for a registered project.
// Loads from disk each call — cheap enough (single small file) and always current.
func (m *Manager) ReadYAML(ctx context.Context, projectID string) (ProjectFile, error) {
	proj, err := m.Store.GetProject(ctx, projectID)
	if err != nil {
		return ProjectFile{}, err
	}
	var pf ProjectFile
	if err := readYAML(filepath.Join(proj.Path, ".chief", "project.yaml"), &pf); err != nil {
		return ProjectFile{}, err
	}
	return pf, nil
}

// WriteYAML mutates .chief/project.yaml via a callback. The callback receives
// the currently-persisted file and returns the version to write. Atomic: we
// write a temp file then rename.
func (m *Manager) WriteYAML(ctx context.Context, projectID string, mutate func(*ProjectFile)) (ProjectFile, error) {
	proj, err := m.Store.GetProject(ctx, projectID)
	if err != nil {
		return ProjectFile{}, err
	}
	path := filepath.Join(proj.Path, ".chief", "project.yaml")
	var pf ProjectFile
	_ = readYAML(path, &pf)
	mutate(&pf)
	if err := writeYAML(path, pf); err != nil {
		return ProjectFile{}, err
	}
	return pf, nil
}

// AddTaskOpts collects the user-supplied fields for AddTask.
type AddTaskOpts struct {
	Title             string
	Body              string
	Category          string
	Priority          int
	RequiredResources []string
	Due               string
}

// AddTask appends a new task line to the project's backlog.md, mints an id,
// writes back (with echo suppression), and re-syncs the store. Returns the
// minted id and the resulting store row.
func (m *Manager) AddTask(ctx context.Context, projectID string, opts AddTaskOpts) (store.Task, error) {
	if opts.Title == "" {
		return store.Task{}, errors.New("title required")
	}
	proj, err := m.Store.GetProject(ctx, projectID)
	if err != nil {
		return store.Task{}, err
	}
	backlogPath := filepath.Join(proj.Path, "backlog.md")

	original, err := os.ReadFile(backlogPath)
	if err != nil {
		if os.IsNotExist(err) {
			original = []byte{}
		} else {
			return store.Task{}, err
		}
	}

	// Mint id with collision-check against SQLite.
	var id string
	for i := 0; i < 32; i++ {
		cand := store.NewTaskID()
		exists, err := m.Store.TaskIDExists(ctx, cand)
		if err != nil {
			return store.Task{}, err
		}
		if !exists {
			id = cand
			break
		}
	}
	if id == "" {
		return store.Task{}, errors.New("could not mint unique task id after 32 attempts")
	}

	newContent, err := backlog.AppendToFile(string(original), backlog.NewTaskInput{
		ID: id, Title: opts.Title, Body: opts.Body,
		Priority: opts.Priority, Category: opts.Category,
		RequiredResources: opts.RequiredResources, Due: opts.Due,
	})
	if err != nil {
		return store.Task{}, err
	}

	outBytes := []byte(newContent)
	if m.Watcher != nil {
		m.Watcher.SuppressNext(backlogPath, sha256Hex(outBytes))
	}
	if err := os.WriteFile(backlogPath, outBytes, 0o644); err != nil {
		return store.Task{}, fmt.Errorf("write backlog.md: %w", err)
	}

	// Re-sync store from disk so the row Chief inserts matches the parser's
	// interpretation exactly (line-hash, category resolution, etc.). Then
	// look up and return the freshly-created row.
	if _, err := m.Rescan(ctx, proj.ID); err != nil {
		return store.Task{}, err
	}
	t, err := m.Store.GetTask(ctx, id)
	if err != nil {
		return store.Task{}, err
	}
	_ = m.Store.InsertEvent(ctx, proj.ID, "", "task.added",
		map[string]any{"id": id, "title": opts.Title, "priority": opts.Priority})
	return t, nil
}

// UpdateTaskOpts is what UpdateTask consumes; nil Body pointer means "don't
// touch body", *"" means "clear body", *"foo" means "replace body with foo".
type UpdateTaskOpts struct {
	Title             string
	Priority          int
	Category          string
	RequiredResources []string
	Due               string
	Body              *string
}

// UpdateTask rewrites a task's checkbox line in backlog.md (title, priority,
// category, resources, due) and optionally its body. Preserves the original
// checkbox character. Triggers a rescan afterwards so the store reflects.
func (m *Manager) UpdateTask(ctx context.Context, taskID string, opts UpdateTaskOpts) (store.Task, error) {
	if strings.TrimSpace(opts.Title) == "" {
		return store.Task{}, errors.New("title required")
	}
	t, err := m.Store.GetTask(ctx, taskID)
	if err != nil {
		return store.Task{}, err
	}
	proj, err := m.Store.GetProject(ctx, t.ProjectID)
	if err != nil {
		return store.Task{}, err
	}
	// UpdateTask only applies to backlog.md items. Done items live in
	// completedlog.md and are historical; edits there should be manual.
	if t.SourceFile != "backlog.md" {
		return store.Task{}, fmt.Errorf("task %s lives in %s; only backlog.md items are editable", taskID, t.SourceFile)
	}
	backlogPath := filepath.Join(proj.Path, "backlog.md")
	content, err := os.ReadFile(backlogPath)
	if err != nil {
		return store.Task{}, fmt.Errorf("read %s: %w", backlogPath, err)
	}
	tasks := backlog.ParseFile(string(content))
	newContent, err := backlog.UpdateTask(string(content), tasks, backlog.UpdateTaskInput{
		ID: taskID, Title: opts.Title, Priority: opts.Priority,
		Category: opts.Category, RequiredResources: opts.RequiredResources,
		Due: opts.Due, Body: opts.Body,
	})
	if err != nil {
		return store.Task{}, err
	}
	if err := m.writeSuppressed(backlogPath, []byte(newContent)); err != nil {
		return store.Task{}, err
	}
	if _, err := m.Rescan(ctx, proj.ID); err != nil {
		return store.Task{}, err
	}
	_ = m.Store.InsertEvent(ctx, proj.ID, "", "task.updated",
		map[string]any{"id": taskID, "title": opts.Title})
	return m.Store.GetTask(ctx, taskID)
}

// DeleteTask removes a task from backlog.md (checkbox line + body). The
// subsequent Rescan drops the row from the DB. Only supports backlog.md
// items — completedlog.md entries stay as historical record and should be
// edited manually if the user really wants to expunge them.
func (m *Manager) DeleteTask(ctx context.Context, taskID string) error {
	t, err := m.Store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if t.SourceFile != "backlog.md" {
		return fmt.Errorf("task %s lives in %s; only backlog.md items can be deleted", taskID, t.SourceFile)
	}
	proj, err := m.Store.GetProject(ctx, t.ProjectID)
	if err != nil {
		return err
	}
	backlogPath := filepath.Join(proj.Path, "backlog.md")
	content, err := os.ReadFile(backlogPath)
	if err != nil {
		return err
	}
	tasks := backlog.ParseFile(string(content))
	newContent := backlog.RemoveTasksByIDs(string(content), tasks, map[string]bool{taskID: true})
	if string(newContent) == string(content) {
		return nil // no-op (nothing matched)
	}
	if err := m.writeSuppressed(backlogPath, []byte(newContent)); err != nil {
		return err
	}
	if _, err := m.Rescan(ctx, proj.ID); err != nil {
		return err
	}
	_ = m.Store.InsertEvent(ctx, proj.ID, "", "task.deleted",
		map[string]any{"id": taskID, "title": t.Title})
	return nil
}

// MoveTask swaps the given task's block with its adjacent same-section
// sibling. Direction is "up" or "down". Silent no-op when the task is at
// the edge of its section (blocked by an H1/H2/H3 header).
func (m *Manager) MoveTask(ctx context.Context, taskID, direction string) (store.Task, error) {
	if direction != "up" && direction != "down" {
		return store.Task{}, fmt.Errorf("direction must be up or down (got %q)", direction)
	}
	t, err := m.Store.GetTask(ctx, taskID)
	if err != nil {
		return store.Task{}, err
	}
	if t.SourceFile != "backlog.md" {
		return store.Task{}, fmt.Errorf("task %s lives in %s; only backlog.md items are reorderable", taskID, t.SourceFile)
	}
	proj, err := m.Store.GetProject(ctx, t.ProjectID)
	if err != nil {
		return store.Task{}, err
	}
	backlogPath := filepath.Join(proj.Path, "backlog.md")
	content, err := os.ReadFile(backlogPath)
	if err != nil {
		return store.Task{}, err
	}
	tasks := backlog.ParseFile(string(content))
	newContent, err := backlog.MoveTask(string(content), tasks, taskID, direction)
	if err != nil {
		return store.Task{}, err
	}
	if string(newContent) == string(content) {
		return t, nil // no-op (edge of section)
	}
	if err := m.writeSuppressed(backlogPath, []byte(newContent)); err != nil {
		return store.Task{}, err
	}
	if _, err := m.Rescan(ctx, proj.ID); err != nil {
		return store.Task{}, err
	}
	_ = m.Store.InsertEvent(ctx, proj.ID, "", "task.moved",
		map[string]any{"id": taskID, "direction": direction})
	return m.Store.GetTask(ctx, taskID)
}

// Remove deletes the project row (cascading tasks). Leaves .chief/ on disk
// so registration state is preserved; the caller can also delete the dir.
func (m *Manager) Remove(ctx context.Context, idOrPath string) error {
	proj, err := m.Store.GetProject(ctx, idOrPath)
	if err != nil {
		return err
	}
	if err := m.Store.DeleteProject(ctx, proj.ID); err != nil {
		return err
	}
	_ = m.Store.InsertEvent(ctx, "", "", "project.removed", map[string]any{"id": proj.ID, "path": proj.Path})
	return nil
}

// Rescan re-parses backlog.md AND completedlog.md for a project, sweeps any
// `[x]` items from backlog.md into completedlog.md (dedupe by id), mints ids
// for new tasks, and upserts everything to SQLite.
//
// This is called by fswatch on file settle and by explicit CLI requests. All
// writes are echo-suppressed so we don't loop on our own edits.
func (m *Manager) Rescan(ctx context.Context, projectID string) ([]backlog.Task, error) {
	proj, err := m.Store.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	backlogPath := filepath.Join(proj.Path, "backlog.md")
	completedPath := filepath.Join(proj.Path, "completedlog.md")

	backlogContent, err := readOrEmpty(backlogPath)
	if err != nil {
		return nil, err
	}
	completedContent, err := readOrEmpty(completedPath)
	if err != nil {
		return nil, err
	}

	backlogTasks := backlog.ParseFile(backlogContent)

	// ---- ID minting (backlog only; completedlog entries already have ids) ----
	mint := func() (string, error) {
		for i := 0; i < 32; i++ {
			id := store.NewTaskID()
			exists, err := m.Store.TaskIDExists(ctx, id)
			if err != nil {
				return "", err
			}
			if !exists {
				return id, nil
			}
		}
		return "", errors.New("could not mint unique task id after 32 attempts")
	}
	backlogTasks, idEdits, err := backlog.AssignIDs(backlogTasks, mint)
	if err != nil {
		return nil, err
	}
	if len(idEdits) > 0 {
		out := backlog.Rewrite(backlogContent, idEdits)
		if err := m.writeSuppressed(backlogPath, []byte(out)); err != nil {
			return nil, fmt.Errorf("write ids back to %s: %w", backlogPath, err)
		}
		backlogContent = out
		// Re-parse with ids assigned so LineNums stay accurate for the sweep.
		backlogTasks = backlog.ParseFile(backlogContent)
	}

	// ---- Sweep [x] items from backlog.md into completedlog.md ----
	doneIDs := map[string]bool{}
	sweptTasks := []backlog.Task{}
	for _, t := range backlogTasks {
		if t.Status == backlog.StatusDone {
			doneIDs[t.ID] = true
			sweptTasks = append(sweptTasks, t)
		}
	}
	if len(doneIDs) > 0 {
		// Append (newest-first) to completedlog.md, dedupe by id.
		// Preserve task order in the batch by iterating in reverse so the
		// earlier-listed item ends up on top after successive prepends.
		newCompleted := completedContent
		for i := len(sweptTasks) - 1; i >= 0; i-- {
			t := sweptTasks[i]
			doneAt := t.CompletedAt
			if doneAt == "" {
				doneAt = time.Now().UTC().Format("2006-01-02 15:04")
			}
			newCompleted = backlog.PrependCompletion(newCompleted, backlog.CompletionInput{
				ID: t.ID, Title: t.Title, Priority: t.Priority,
				Category: t.Category, DoneAt: doneAt,
			})
		}
		if newCompleted != completedContent {
			if err := m.writeSuppressed(completedPath, []byte(newCompleted)); err != nil {
				return nil, fmt.Errorf("write %s: %w", completedPath, err)
			}
			completedContent = newCompleted
		}
		// Remove swept lines from backlog.md.
		newBacklog := backlog.RemoveTasksByIDs(backlogContent, backlogTasks, doneIDs)
		if newBacklog != backlogContent {
			if err := m.writeSuppressed(backlogPath, []byte(newBacklog)); err != nil {
				return nil, fmt.Errorf("write %s: %w", backlogPath, err)
			}
			backlogContent = newBacklog
			backlogTasks = backlog.ParseFile(backlogContent)
		}
	}

	// ---- Parse completedlog.md; merge into store view ----
	completedTasks := backlog.ParseFile(completedContent)

	storeTasks := make([]store.Task, 0, len(backlogTasks)+len(completedTasks))
	for _, t := range backlogTasks {
		storeTasks = append(storeTasks, toStoreTask(t, proj.ID, "backlog.md"))
	}
	// Dedupe: if a completedlog entry has the same id as a backlog entry
	// (weird, but possible if the user manually re-added), backlog wins.
	seen := map[string]bool{}
	for _, t := range storeTasks {
		seen[t.ID] = true
	}
	for _, t := range completedTasks {
		if seen[t.ID] {
			continue
		}
		storeTasks = append(storeTasks, toStoreTask(t, proj.ID, "completedlog.md"))
	}
	if err := m.Store.UpsertTasks(ctx, proj.ID, storeTasks); err != nil {
		return nil, err
	}
	// Return combined for callers that care about count.
	all := append([]backlog.Task{}, backlogTasks...)
	all = append(all, completedTasks...)
	return all, nil
}

// readOrEmpty reads a file, treating missing files as an empty string. Any
// other read error is surfaced.
func readOrEmpty(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(b), nil
}

// writeSuppressed writes a file after registering its expected content hash
// with the fswatch watcher, so the resulting fsnotify event is dropped.
func (m *Manager) writeSuppressed(path string, data []byte) error {
	if m.Watcher != nil {
		m.Watcher.SuppressNext(path, sha256Hex(data))
	}
	return os.WriteFile(path, data, 0o644)
}

func toStoreTask(t backlog.Task, projectID, sourceFile string) store.Task {
	s := store.Task{
		ID:                t.ID,
		ProjectID:         projectID,
		Title:             t.Title,
		Body:              t.Body,
		SourceFile:        sourceFile,
		SourceLineHash:    backlog.LineHash(t.RawLine),
		SourceLine:        t.LineNum,
		Status:            store.TaskStatus(t.Status),
		Priority:          t.Priority,
		Category:          t.Category,
		RequiredResources: t.RequiredResources,
	}
	if t.Due != "" {
		d := t.Due
		s.Due = &d
	}
	if t.CompletedAt != "" {
		if v, err := time.Parse("2006-01-02", t.CompletedAt); err == nil {
			s.CompletedAt = &v
		} else if v, err := time.Parse("2006-01-02 15:04", t.CompletedAt); err == nil {
			s.CompletedAt = &v
		}
	}
	return s
}

func readYAML(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(b, out)
}

func writeYAML(path string, in any) error {
	b, err := yaml.Marshal(in)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

