package messaging

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// NtfyBackend delivers messages via ntfy.sh (or a self-hosted ntfy
// server). Protocol: POST <server>/<topic> with the body as the
// notification text and headers for title/priority/click-URL. No auth
// required for public topics; users protect topics by keeping the
// topic name a secret (URL-as-shared-secret).
//
// Config (config.yaml):
//
//	messaging:
//	  backends:
//	    - name: phone
//	      type: ntfy
//	      topic: <your-secret-topic>
//	      server: https://ntfy.sh   # optional; default = ntfy.sh
type NtfyBackend struct {
	NameStr string
	Server  string // "https://ntfy.sh" if empty
	Topic   string // required
	HTTP    *http.Client
}

// Name reports this backend's identifier.
func (n *NtfyBackend) Name() string { return n.NameStr }

// Send POSTs the message to <server>/<topic>. Priority is derived from
// urgency: urgent→5 (max), attention→3 (default), info→2 (low). Chief
// deep-links (if set on the Message) go into the Click header so the
// phone-side ntfy app can jump straight there.
func (n *NtfyBackend) Send(ctx context.Context, m Message) error {
	if n.Topic == "" {
		return fmt.Errorf("ntfy backend %q: topic required", n.NameStr)
	}
	server := strings.TrimRight(n.Server, "/")
	if server == "" {
		server = "https://ntfy.sh"
	}
	url := server + "/" + n.Topic

	body := m.Body
	if body == "" {
		body = m.Title
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return err
	}
	if m.Title != "" {
		// ntfy titles land in the Title header. Newlines break the
		// header protocol; replace with a middot for compactness.
		req.Header.Set("Title", singleLine(m.Title))
	}
	req.Header.Set("Priority", ntfyPriority(m.Urgency))
	if m.URL != "" {
		req.Header.Set("Click", m.URL)
	}
	// Small "Tags" header lets the ntfy app render an emoji per urgency
	// — pure cosmetic. Kept optional so people who don't want emoji
	// can filter tags out on the receiver side.
	if tag := ntfyTag(m.Urgency); tag != "" {
		req.Header.Set("Tags", tag)
	}

	client := n.HTTP
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
		return fmt.Errorf("ntfy %s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return nil
}

// ntfyPriority maps chief's three urgency levels onto ntfy's 1-5 scale.
// 3 is "default"; we skip 4 to preserve headroom for future levels.
func ntfyPriority(u Urgency) string {
	switch u {
	case UrgencyUrgent:
		return "5"
	case UrgencyAttention:
		return "3"
	case UrgencyInfo:
		return "2"
	}
	return "3"
}

func ntfyTag(u Urgency) string {
	switch u {
	case UrgencyUrgent:
		return "rotating_light"
	case UrgencyAttention:
		return "bell"
	case UrgencyInfo:
		return "information_source"
	}
	return ""
}

// singleLine collapses newlines/carriage-returns to " · ". Used for
// header values (HTTP forbids raw newlines) and to keep notification
// titles readable.
func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " · ")
	s = strings.ReplaceAll(s, "\n", " · ")
	s = strings.ReplaceAll(s, "\r", " · ")
	return s
}
