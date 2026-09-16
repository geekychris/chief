package historyviewer

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

// PortFilePath is the well-known file hv-app writes on startup so Chief can
// discover a running instance without probing.  Keep in sync with hv-app.
func PortFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Caches", "history_viewer", "port")
}

// RunningInstance returns the loopback address (e.g. "127.0.0.1:53103") of
// a live history_viewer if one is running, or "" if none is detected. Fast
// on failure — port file missing OR GET /api/filter/current didn't answer
// within the timeout.
func RunningInstance(ctx context.Context) string {
	b, err := os.ReadFile(PortFilePath())
	if err != nil {
		return ""
	}
	addr := strings.TrimSpace(string(b))
	if addr == "" {
		return ""
	}
	if !probeAlive(ctx, addr, 400*time.Millisecond) {
		return ""
	}
	return addr
}

// probeAlive fires GET /api/filter/current within timeout and returns true
// on any 2xx. history_viewer doesn't have a dedicated /healthz, so we use
// the filter endpoint (always cheap, always defined).
func probeAlive(parent context.Context, addr string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/filter/current", nil)
	if err != nil {
		return false
	}
	client := &http.Client{
		Timeout:   timeout,
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

// NavigateFilter POSTs a new directory filter to a running history_viewer,
// so its already-open Wails window updates in place (via the frontend's
// /api/filter/current poll) — no duplicate window, no scroll reset.
func NavigateFilter(parent context.Context, addr, dir string) error {
	body, err := json.Marshal(map[string]string{"dir": dir})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+addr+"/api/filter/directory", bytes.NewReader(body))
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
		return fmt.Errorf("navigate history_viewer: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
