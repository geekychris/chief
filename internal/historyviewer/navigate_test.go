package historyviewer

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startFake stands up a tiny loopback HTTP server that speaks the two
// endpoints Chief actually depends on: GET /api/filter/current + POST
// /api/filter/directory. We use this instead of importing the real
// history_viewer (cross-repo) to keep the test hermetic + fast.
func startFake(t *testing.T) (addr string, latest func() (string, int64), stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu      = &noopLock{} // Mutex would work; simple pattern to match testing.T style
		dir     = ""
		version = int64(0)
	)
	_ = mu
	mux := http.NewServeMux()
	mux.HandleFunc("/api/filter/current", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"dir": dir, "version": version})
	})
	mux.HandleFunc("/api/filter/directory", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Dir string `json:"dir"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		dir = body.Dir
		version++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"dir": dir, "version": version})
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	return ln.Addr().String(),
		func() (string, int64) { return dir, version },
		func() { _ = srv.Close() }
}

// noopLock is just a stand-in so mu is defined; we could use sync.Mutex if
// concurrent access mattered, but our tests are serial.
type noopLock struct{}

func (n *noopLock) Lock()   {}
func (n *noopLock) Unlock() {}

func TestNavigateFilter_POSTsCorrectPayload(t *testing.T) {
	addr, latest, stop := startFake(t)
	defer stop()

	err := NavigateFilter(context.Background(), addr, "/Users/me/project")
	if err != nil {
		t.Fatalf("NavigateFilter: %v", err)
	}
	dir, v := latest()
	if dir != "/Users/me/project" {
		t.Fatalf("dir on server: want /Users/me/project, got %q", dir)
	}
	if v != 1 {
		t.Fatalf("version: want 1, got %d", v)
	}
}

func TestNavigateFilter_BumpsVersionOnRepeat(t *testing.T) {
	addr, latest, stop := startFake(t)
	defer stop()

	_ = NavigateFilter(context.Background(), addr, "/a")
	_ = NavigateFilter(context.Background(), addr, "/b")
	dir, v := latest()
	if dir != "/b" || v != 2 {
		t.Fatalf("after two calls: dir=%q v=%d", dir, v)
	}
}

func TestNavigateFilter_SurfacesServerErrors(t *testing.T) {
	// Server that always 500s so we can verify the client returns an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	err := NavigateFilter(context.Background(), addr, "/x")
	if err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error should mention status: %v", err)
	}
}

func TestRunningInstance_ReadsPortFileAndProbes(t *testing.T) {
	addr, _, stop := startFake(t)
	defer stop()

	// Point PortFilePath at a tmpfile for the duration of the test by
	// overriding HOME (PortFilePath is derived from UserHomeDir).
	oldHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	defer func() { _ = os.Setenv("HOME", oldHome) }()
	// Write the fake server's address as the port file.
	portFile := filepath.Join(tmpHome, "Library", "Caches", "history_viewer", "port")
	if err := os.MkdirAll(filepath.Dir(portFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(portFile, []byte(addr+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := RunningInstance(context.Background())
	if got != addr {
		t.Fatalf("RunningInstance: want %q got %q", addr, got)
	}
}

func TestRunningInstance_NoPortFile(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	got := RunningInstance(context.Background())
	if got != "" {
		t.Fatalf("expected empty when port file missing, got %q", got)
	}
}

func TestRunningInstance_StalePortFile(t *testing.T) {
	// Port file points at a non-listening port; probe should fail fast.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	portFile := filepath.Join(tmpHome, "Library", "Caches", "history_viewer", "port")
	_ = os.MkdirAll(filepath.Dir(portFile), 0o755)
	// Grab a free port then close it — nothing will be listening.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	staleAddr := ln.Addr().String()
	_ = ln.Close()
	_ = os.WriteFile(portFile, []byte(staleAddr+"\n"), 0o644)
	// Slightly longer timeout guard: RunningInstance's probe is 400ms.
	start := time.Now()
	got := RunningInstance(context.Background())
	if got != "" {
		t.Fatalf("expected empty for stale port file, got %q", got)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("stale-probe took too long: %v", time.Since(start))
	}
}
