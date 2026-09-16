package claudetrace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PortFilePath returns the well-known file the analyzer writes its loopback
// address to on startup (see analyzer/app.go writePortFile). Chief reads this
// to discover a running instance before deciding whether to launch a new one.
//
// Keep this in sync with analyzer's PortFilePath().
func PortFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Caches", "claude-trace", "port")
}

// RunningInstance probes for a live analyzer instance.  Returns the loopback
// address (e.g. "127.0.0.1:65174") and true on success. False+empty means:
// port file missing OR /healthz didn't answer in time.  Fast on failure —
// suitable for the click-path.
func RunningInstance(ctx context.Context) (string, bool) {
	b, err := os.ReadFile(PortFilePath())
	if err != nil {
		return "", false
	}
	addr := strings.TrimSpace(string(b))
	if addr == "" {
		return "", false
	}
	if !pingHealth(ctx, addr, 400*time.Millisecond) {
		return "", false
	}
	return addr, true
}

// pingHealth GETs /api/v1/healthz and returns true on any 2xx within timeout.
func pingHealth(parent context.Context, addr string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/v1/healthz", nil)
	if err != nil {
		return false
	}
	client := &http.Client{
		Timeout: timeout,
		// Force loopback dial; avoids any resolver hiccups.
		Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: timeout}).DialContext},
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// Nav mirrors the analyzer's frontend Nav discriminated union. Callers use
// the constructors below for common cases.
type Nav map[string]any

// NavDashboard returns a payload that puts the analyzer on the dashboard.
func NavDashboard() Nav { return Nav{"view": "dashboard"} }

// NavProject returns a payload that opens the given project slug.
func NavProject(slug string) Nav { return Nav{"view": "project", "slug": slug} }

// NavSession returns a payload that opens a specific session.
func NavSession(sessionID, projectSlug string) Nav {
	return Nav{"view": "session", "sessionId": sessionID, "projectSlug": projectSlug}
}

// Navigate POSTs the payload to a running analyzer at addr (e.g. as returned
// by RunningInstance). Non-2xx responses become an error.
func Navigate(parent context.Context, addr string, nav Nav) error {
	body, err := json.Marshal(nav)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+addr+"/api/v1/navigate", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("navigate: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
