// Package fswatch wraps fsnotify with two things Chief needs:
//
//   1. Debounce — coalesce the two-writes-per-save that macOS FSEvents produces.
//   2. Echo suppression — when Chief itself writes back to backlog.md (to
//      mint an ID or flip a checkbox), the resulting fsnotify event should be
//      dropped so the daemon doesn't loop-reparse its own output.
//
// The caller registers a project (id + path) plus a handler; the watcher
// invokes the handler whenever a tracked file under path settles.
package fswatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Handler is called after debounce fires for a specific project's changed file.
// The full path of the changed file is passed; the handler decides what to do
// (typically: manager.Rescan(projectID)).
type Handler func(projectID, path string)

// TrackedFiles are the base names within each project dir that fswatch cares
// about. Kept small; expanded when more file types come online (M2 adds
// PROJECT.md).
var TrackedFiles = map[string]bool{
	"backlog.md":      true,
	"completedlog.md": true,
	"dropped.md":      true,
	"constitution.md": true,
	"PROJECT.md":      true,
}

// Watcher owns one fsnotify.Watcher and dispatches to registered handlers.
type Watcher struct {
	fs      *fsnotify.Watcher
	handler Handler

	mu         sync.Mutex
	projects   map[string]string        // projectID → absolute path
	pathToProj map[string]string        // dir path → projectID (reverse index)
	timers     map[string]*time.Timer   // full file path → active debounce timer
	echoHashes map[string]string        // full file path → expected sha256 hex to suppress once

	debounce time.Duration
	logger   *slog.Logger
}

// New creates a Watcher. handler runs in the fsnotify goroutine — keep it
// short or dispatch to another worker.
func New(handler Handler) (*Watcher, error) {
	fs, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{
		fs:         fs,
		handler:    handler,
		projects:   map[string]string{},
		pathToProj: map[string]string{},
		timers:     map[string]*time.Timer{},
		echoHashes: map[string]string{},
		debounce:   300 * time.Millisecond,
		logger:     slog.Default(),
	}, nil
}

// Add registers a project directory to watch. Idempotent for the same id/path.
// The directory is watched (not individual files) so newly-created files show
// up automatically.
func (w *Watcher) Add(projectID, dir string) error {
	dir = filepath.Clean(dir)
	w.mu.Lock()
	defer w.mu.Unlock()
	if existing, ok := w.projects[projectID]; ok && existing == dir {
		return nil
	}
	if err := w.fs.Add(dir); err != nil {
		return err
	}
	w.projects[projectID] = dir
	w.pathToProj[dir] = projectID
	return nil
}

// Remove unregisters and unwatches a project.
func (w *Watcher) Remove(projectID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	dir, ok := w.projects[projectID]
	if !ok {
		return
	}
	_ = w.fs.Remove(dir)
	delete(w.projects, projectID)
	delete(w.pathToProj, dir)
}

// SuppressNext records the expected sha256 hex of a file's contents so the
// upcoming fsnotify event caused by Chief's own write is dropped. Call this
// immediately before writing, with a hash of the bytes you're about to write.
func (w *Watcher) SuppressNext(path, sha256Hex string) {
	path = filepath.Clean(path)
	w.mu.Lock()
	w.echoHashes[path] = sha256Hex
	w.mu.Unlock()
}

// HashBytes returns the sha256 hex of the given bytes. Handy for callers
// that need to compute an expected hash before calling SuppressNext.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Run blocks reading events and dispatching handlers until ctx is done.
func (w *Watcher) Run(ctx context.Context) error {
	defer w.fs.Close()
	for {
		select {
		case <-ctx.Done():
			w.cancelAllTimers()
			return ctx.Err()
		case err, ok := <-w.fs.Errors:
			if !ok {
				return nil
			}
			w.logger.Warn("fsnotify error", "err", err)
		case ev, ok := <-w.fs.Events:
			if !ok {
				return nil
			}
			w.dispatch(ev)
		}
	}
}

func (w *Watcher) dispatch(ev fsnotify.Event) {
	base := filepath.Base(ev.Name)
	if !TrackedFiles[base] {
		return
	}
	// Ignore chmod-only events; they don't imply new content.
	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
		return
	}
	dir := filepath.Clean(filepath.Dir(ev.Name))
	w.mu.Lock()
	projID, ok := w.pathToProj[dir]
	if !ok {
		w.mu.Unlock()
		return
	}
	// Reset the debounce timer for this exact file path.
	fullPath := filepath.Clean(ev.Name)
	if t, ok := w.timers[fullPath]; ok {
		t.Stop()
	}
	w.timers[fullPath] = time.AfterFunc(w.debounce, func() {
		w.onSettled(projID, fullPath)
	})
	w.mu.Unlock()
}

func (w *Watcher) onSettled(projectID, path string) {
	// Echo-suppression check: if we recorded an expected hash and the file's
	// current content matches, this event was our own write — swallow it.
	b, err := os.ReadFile(path)
	if err == nil {
		got := HashBytes(b)
		w.mu.Lock()
		want, hasWant := w.echoHashes[path]
		if hasWant && want == got {
			delete(w.echoHashes, path)
			delete(w.timers, path)
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()
	}

	w.mu.Lock()
	delete(w.timers, path)
	w.mu.Unlock()

	w.handler(projectID, path)
}

func (w *Watcher) cancelAllTimers() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, t := range w.timers {
		t.Stop()
	}
	w.timers = map[string]*time.Timer{}
}

// SetDebounce overrides the default 300ms window; useful for tests.
func (w *Watcher) SetDebounce(d time.Duration) {
	w.mu.Lock()
	w.debounce = d
	w.mu.Unlock()
}
