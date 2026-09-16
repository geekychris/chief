package messaging

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PushoverBackend delivers messages via https://pushover.net. Requires
// an app-token (per-app registration at pushover.net) + a user-key
// (the recipient's account). Priority mapping mirrors ntfy: urgent
// gets the highest tier (2 = emergency, which repeats until acked;
// dropped to 1 = high to avoid nag storms until Chief has explicit
// acknowledgement plumbing).
//
// Config:
//
//	messaging:
//	  backends:
//	    - name: pushover
//	      type: pushover
//	      token: <app_token>
//	      user:  <user_key>
type PushoverBackend struct {
	NameStr string
	Token   string // app_token
	User    string // user_key
	HTTP    *http.Client

	// EndpointOverride is a test seam. Empty defaults to the real
	// Pushover API URL.
	EndpointOverride string
}

// Name reports this backend's identifier.
func (p *PushoverBackend) Name() string { return p.NameStr }

// Send POSTs an application/x-www-form-urlencoded body to Pushover.
// Returns an error on non-2xx. Note: Pushover expects the request
// even for validation errors; check the returned body for details in
// production if you see repeated 4xx.
func (p *PushoverBackend) Send(ctx context.Context, m Message) error {
	if p.Token == "" || p.User == "" {
		return fmt.Errorf("pushover backend %q: token and user required", p.NameStr)
	}
	endpoint := p.EndpointOverride
	if endpoint == "" {
		endpoint = "https://api.pushover.net/1/messages.json"
	}
	form := url.Values{}
	form.Set("token", p.Token)
	form.Set("user", p.User)
	body := m.Body
	if body == "" {
		body = m.Title
	}
	form.Set("message", body)
	if m.Title != "" {
		form.Set("title", singleLine(m.Title))
	}
	form.Set("priority", pushoverPriority(m.Urgency))
	if m.URL != "" {
		form.Set("url", m.URL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := p.HTTP
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
		return fmt.Errorf("pushover: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return nil
}

// pushoverPriority maps chief's urgency to Pushover's -2..2 scale.
// We stop at 1 (high) rather than 2 (emergency) — emergency requires
// user acknowledgement and repeat-cycles that Chief doesn't currently
// wire back through. Bump to 2 in a follow-up when we have explicit
// ack plumbing.
func pushoverPriority(u Urgency) string {
	switch u {
	case UrgencyUrgent:
		return "1"
	case UrgencyAttention:
		return "0"
	case UrgencyInfo:
		return "-1"
	}
	return "0"
}
