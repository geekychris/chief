// Package archive writes events to gzipped JSONL files, grouped by
// month, before they're deleted from the live SQLite events table.
//
// File layout: ~/Library/Logs/Chief/archive/YYYY-MM.jsonl.gz
// One JSON object per line (one event per line). Multiple appends to
// the same file are safe because gzip decoders concatenate streams
// transparently — reading a file that grew across many retention
// sweeps still yields the full event list.
package archive

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/geekychris/chief/internal/store"
)

// Writer appends events to per-month gzipped JSONL files under Dir.
// Safe for concurrent use — a single lock serializes writes so gzip
// streams don't interleave.
type Writer struct {
	Dir string
	mu  sync.Mutex
}

// New returns a Writer that will create Dir on first write if missing.
func New(dir string) *Writer { return &Writer{Dir: dir} }

// Append writes each event to the file for its month. Returns the set
// of IDs it successfully wrote so the caller can DELETE just those.
// A partial failure (say, disk full mid-batch) is reported as an error
// but the IDs already written are still returned — DELETE-then-crash
// is the same as archive-then-crash for our purposes.
func (w *Writer) Append(events []store.Event) ([]int64, error) {
	if len(events) == 0 {
		return nil, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir archive dir %s: %w", w.Dir, err)
	}
	// Group by YYYY-MM so we open each target file at most once.
	byMonth := map[string][]store.Event{}
	var order []string // preserve encounter order for determinism
	for _, e := range events {
		key := e.Ts.UTC().Format("2006-01")
		if _, seen := byMonth[key]; !seen {
			order = append(order, key)
		}
		byMonth[key] = append(byMonth[key], e)
	}

	var written []int64
	for _, key := range order {
		path := filepath.Join(w.Dir, key+".jsonl.gz")
		ids, err := appendOne(path, byMonth[key])
		written = append(written, ids...)
		if err != nil {
			return written, fmt.Errorf("write %s: %w", path, err)
		}
	}
	return written, nil
}

// appendOne opens the target file in append mode, writes a gzip stream
// containing all events, closes cleanly. Concatenated gzip streams
// decompress transparently, so appending to an existing file is safe.
func appendOne(path string, events []store.Event) ([]int64, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	enc := json.NewEncoder(gz)
	var written []int64
	for _, e := range events {
		if err := enc.Encode(struct {
			ID        int64     `json:"id"`
			Ts        time.Time `json:"ts"`
			ProjectID string    `json:"project_id,omitempty"`
			SessionID string    `json:"session_id,omitempty"`
			Kind      string    `json:"kind"`
			Payload   json.RawMessage `json:"payload,omitempty"`
		}{
			ID: e.ID, Ts: e.Ts, ProjectID: e.ProjectID, SessionID: e.SessionID,
			Kind: e.Kind, Payload: json.RawMessage(e.Payload),
		}); err != nil {
			_ = gz.Close()
			return written, err
		}
		written = append(written, e.ID)
	}
	if err := gz.Close(); err != nil {
		return written, err
	}
	return written, nil
}
