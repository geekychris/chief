package orchestrator

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geekychris/chief/internal/archive"
	"github.com/geekychris/chief/internal/store"
)

// TestRetentionSweep_ArchivesAndDeletesOldEvents — the core round-trip.
func TestRetentionSweep_ArchivesAndDeletesOldEvents(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Seed a project so events with project_id FK don't need to satisfy
	// anything (events has no FK — but we seed for realism).
	_ = st.CreateProject(context.Background(), store.Project{
		ID: "proj_test", Path: dir, Name: "test", SpawnMode: "attach", State: "idle",
	})

	// Insert 5 events. First 3 have old timestamps; last 2 are fresh.
	// We use raw SQL so we can pin the ts values deterministically.
	oldTs := time.Now().UTC().Add(-100 * 24 * time.Hour).Format(time.RFC3339Nano)
	freshTs := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339Nano)
	for i, ts := range []string{oldTs, oldTs, oldTs, freshTs, freshTs} {
		_, err := st.DB().Exec(`INSERT INTO events(ts, project_id, kind, payload) VALUES(?, ?, ?, ?)`,
			ts, "proj_test", "test.event", `{"i":`+string(rune('0'+i))+`}`)
		if err != nil {
			t.Fatal(err)
		}
	}
	before, _ := st.CountEvents(context.Background())
	if before != 5 {
		t.Fatalf("want 5 events seeded, got %d", before)
	}

	// Run the sweeper with a 90-day cutoff.
	archDir := filepath.Join(dir, "archive")
	sw := &RetentionSweeper{
		Store: st, Archive: archive.New(archDir), RetentionDays: 90,
	}
	if err := sw.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := st.CountEvents(context.Background())
	if after != 2 {
		t.Errorf("want 2 events remaining (the fresh ones), got %d", after)
	}

	// Archive file for the old-events' month should exist and contain 3 records.
	monthKey := time.Now().UTC().Add(-100 * 24 * time.Hour).Format("2006-01")
	archPath := filepath.Join(archDir, monthKey+".jsonl.gz")
	f, err := os.Open(archPath)
	if err != nil {
		t.Fatalf("archive file not created: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	sc := bufio.NewScanner(gz)
	n := 0
	for sc.Scan() {
		var row map[string]any
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Errorf("bad archive line: %v (line=%q)", err, sc.Text())
		}
		n++
	}
	if n != 3 {
		t.Errorf("want 3 archived rows, got %d", n)
	}
}

// TestRetentionSweep_NoOpWhenNothingOld — no archive file created if
// there are no events past the cutoff.
func TestRetentionSweep_NoOpWhenNothingOld(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "test.db"))
	defer st.Close()
	_ = st.CreateProject(context.Background(), store.Project{
		ID: "proj_test", Path: dir, Name: "test", SpawnMode: "attach", State: "idle",
	})
	// Only fresh events.
	_, _ = st.DB().Exec(`INSERT INTO events(ts, project_id, kind, payload) VALUES(?, ?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano), "proj_test", "test", "{}")
	archDir := filepath.Join(dir, "archive")
	sw := &RetentionSweeper{Store: st, Archive: archive.New(archDir), RetentionDays: 90}
	if err := sw.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(archDir); !os.IsNotExist(err) {
		// It's fine if the dir DOES exist as an empty placeholder — the
		// point is that no archive FILE was written.
		entries, _ := os.ReadDir(archDir)
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".gz" {
				t.Errorf("unexpected archive file created: %s", e.Name())
			}
		}
	}
}

// TestRetentionSweep_DisabledWhenNegativeRetention — RetentionDays<0
// short-circuits Run without doing anything.
func TestRetentionSweep_DisabledWhenNegativeRetention(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "test.db"))
	defer st.Close()
	sw := &RetentionSweeper{Store: st, Archive: archive.New(dir), RetentionDays: -1}
	// Run should return quickly when disabled — bound the test.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { sw.Run(ctx); close(done) }()
	select {
	case <-done:
		// good — bailed early.
	case <-time.After(3 * time.Second):
		t.Error("Run(ctx) did not return promptly when RetentionDays<0")
	}
}
