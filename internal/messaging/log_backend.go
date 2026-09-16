package messaging

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LogBackend appends each Message as a JSON line to a file. Serves two
// purposes: (1) reference implementation for people writing new
// backends, (2) always-on local audit trail so you can grep for
// "what did chief try to send when?" without needing a real network
// backend configured. Enabled by default in chiefd's config wiring.
type LogBackend struct {
	NameStr string // e.g. "local-log"
	Path    string // typically ~/Library/Logs/Chief/messages.jsonl
	mu      sync.Mutex
}

// Name reports this backend's identifier. Match what config.yaml puts
// in the routing lists.
func (l *LogBackend) Name() string { return l.NameStr }

// Send serializes the message as one JSON line + newline. Newlines
// inside the body are preserved as \n in the JSON string.
func (l *LogBackend) Send(_ context.Context, m Message) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(struct {
		Ts          time.Time `json:"ts"`
		Backend     string    `json:"backend"`
		ProjectID   string    `json:"project_id,omitempty"`
		ProjectName string    `json:"project_name,omitempty"`
		Urgency     Urgency   `json:"urgency"`
		Title       string    `json:"title,omitempty"`
		Body        string    `json:"body,omitempty"`
		URL         string    `json:"url,omitempty"`
		Kind        string    `json:"kind,omitempty"`
	}{
		Ts:          time.Now().UTC(),
		Backend:     l.NameStr,
		ProjectID:   m.ProjectID,
		ProjectName: m.ProjectName,
		Urgency:     m.Urgency,
		Title:       m.Title,
		Body:        m.Body,
		URL:         m.URL,
		Kind:        m.Kind,
	})
}
