package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SlackBackend delivers messages via a Slack Incoming Webhook. The
// simplest possible Slack integration — no OAuth, no app manifest.
// User creates a webhook in Slack (Apps → Incoming Webhooks → Add to
// workspace → pick channel), pastes the URL into config, done.
//
// Config:
//
//	messaging:
//	  backends:
//	    - name: slack
//	      type: slack
//	      webhook: https://hooks.slack.com/services/T../B../XX...
//
// Uses Slack Block Kit for richer formatting when a title is set,
// falls back to `text` for legacy clients (Slack renders both).
type SlackBackend struct {
	NameStr string
	Webhook string
	HTTP    *http.Client
}

// Name reports this backend's identifier.
func (s *SlackBackend) Name() string { return s.NameStr }

// Send POSTs a JSON body to the incoming webhook URL. Non-2xx returns
// the response body (Slack typically explains WHY it rejected the
// payload in the error text — helpful for debugging bad webhook URLs
// or workspace permissions).
func (s *SlackBackend) Send(ctx context.Context, m Message) error {
	if s.Webhook == "" {
		return fmt.Errorf("slack backend %q: webhook required", s.NameStr)
	}
	payload := map[string]any{
		"text": composeSlackFallback(m),
	}
	if m.Title != "" || m.Body != "" {
		blocks := []map[string]any{}
		if m.Title != "" {
			blocks = append(blocks, map[string]any{
				"type": "header",
				"text": map[string]any{"type": "plain_text", "text": clipText(m.Title, 150), "emoji": true},
			})
		}
		if m.Body != "" {
			blocks = append(blocks, map[string]any{
				"type": "section",
				"text": map[string]any{"type": "mrkdwn", "text": clipText(m.Body, 2900)},
			})
		}
		ctxItems := []map[string]any{
			{"type": "mrkdwn", "text": fmt.Sprintf("*%s*", m.Urgency)},
		}
		if m.ProjectName != "" {
			ctxItems = append(ctxItems, map[string]any{"type": "mrkdwn", "text": m.ProjectName})
		}
		if m.URL != "" {
			ctxItems = append(ctxItems, map[string]any{
				"type": "mrkdwn", "text": fmt.Sprintf("<%s|open in chief>", m.URL),
			})
		}
		blocks = append(blocks, map[string]any{"type": "context", "elements": ctxItems})
		payload["blocks"] = blocks
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Webhook, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		out, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("slack: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return nil
}

// composeSlackFallback produces the "text" field Slack shows in
// notification previews + legacy clients that don't render blocks.
func composeSlackFallback(m Message) string {
	if m.Title != "" && m.Body != "" {
		return m.Title + " — " + singleLine(m.Body)
	}
	if m.Title != "" {
		return m.Title
	}
	return m.Body
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
