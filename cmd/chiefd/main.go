// chiefd is the Chief daemon. It owns the SQLite database, watches registered
// projects with fsnotify, dispatches CLI requests over a unix socket, and
// (from M2 on) exposes MCP.
//
// Bring-up order at startup:
//   1. Logger + data/log directories.
//   2. SQLite store (WAL mode, migrations).
//   3. fsnotify watcher (started as its own goroutine).
//   4. Project manager wired to store + watcher.
//   5. Re-register every persisted project's directory with the watcher; do
//      an initial rescan so state matches disk.
//   6. Unix socket listener with the JSON-RPC-ish dispatcher.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/geekychris/chief/internal/archive"
	"github.com/geekychris/chief/internal/attention"
	"github.com/geekychris/chief/internal/claudetrace"
	"github.com/geekychris/chief/internal/cmux"
	"github.com/geekychris/chief/internal/config"
	"github.com/geekychris/chief/internal/fswatch"
	"github.com/geekychris/chief/internal/gitshim"
	"github.com/geekychris/chief/internal/historyviewer"
	"github.com/geekychris/chief/internal/installer"
	"github.com/geekychris/chief/internal/ipc"
	"github.com/geekychris/chief/internal/messaging"
	"github.com/geekychris/chief/internal/methods"
	"github.com/geekychris/chief/internal/notify"
	"github.com/geekychris/chief/internal/orchestrator"
	"github.com/geekychris/chief/internal/project"
	"github.com/geekychris/chief/internal/store"
)

// Version is stamped at build time via -ldflags. Defaults to "dev".
var Version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "chiefd:", err)
		os.Exit(1)
	}
}

func run() error {
	logger, cleanup, err := setupLogger()
	if err != nil {
		return fmt.Errorf("logger: %w", err)
	}
	defer cleanup()
	slog.SetDefault(logger)

	dataDir, err := ipc.DataDir()
	if err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	dbPath := filepath.Join(dataDir, "chief.db")
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	slog.Info("store opened", "path", dbPath)

	// Global chief config. Loaded once up front so the sweeper and the
	// method handlers share the same cmux socket auth. Absent config
	// falls back to defaults + env vars — booting still works.
	cfg, err := config.Load()
	if err != nil {
		slog.Warn("config load", "err", err)
	}

	mgr := project.New(st)

	// Messaging plugin arch: build the router + backends from config
	// once at boot. Log backend is always present as a local audit
	// trail; other backends (ntfy, Pushover, Telegram, Slack) only
	// register if their config entry exists.
	router := buildMessagingRouter(cfg, mgr)

	// Attention manager: writes flags to `flags` table + fires macOS
	// notifications (osascript). Wired into project.Manager so the Rescan
	// path can raise "next-task" suggestions when a task transitions to done.
	att := &attention.Manager{
		Store:    st,
		Notifier: notify.New(),
		Router:   router,
	}
	// DND resolver: per-project override wins over global. A project
	// with dnd.disable=true always fires (opts out of global quiet hours).
	att.DNDWindowFor = func(projectID string) attention.DNDWindow {
		pf, err := mgr.ReadYAML(context.Background(), projectID)
		if err == nil && pf.DND.Disable {
			return attention.DNDWindow{Disable: true}
		}
		if err == nil && (pf.DND.Start != "" || pf.DND.End != "" || len(pf.DND.Weekdays) > 0) {
			return attention.DNDWindow{
				Start:    pf.DND.Start,
				End:      pf.DND.End,
				Weekdays: pf.DND.Weekdays,
			}
		}
		return attention.DNDWindow{
			Start:    cfg.DND.Start,
			End:      cfg.DND.End,
			Weekdays: cfg.DND.Weekdays,
		}
	}
	mgr.Attention = att

	// Handler for fswatch: whenever a tracked file settles in a project dir,
	// call Rescan. The handler runs on the fswatch goroutine — Rescan is fast
	// (parse + SQLite upsert in a single tx) so we don't need a worker pool
	// yet. If contention shows up in dogfooding, add one here.
	watcher, err := fswatch.New(func(projectID, path string) {
		slog.Info("file settled", "project", projectID, "path", path)
		if _, err := mgr.Rescan(context.Background(), projectID); err != nil {
			slog.Error("rescan failed", "project", projectID, "err", err)
		}
	})
	if err != nil {
		return fmt.Errorf("fswatch: %w", err)
	}
	mgr.Watcher = watcher

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Watcher runs until ctx is cancelled.
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		if err := watcher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("watcher exited", "err", err)
		}
	}()

	// Re-attach every registered project to the watcher and do a first-scan
	// so state matches disk (e.g., user edited backlog.md while chiefd was down).
	projs, err := st.ListProjects(ctx)
	if err != nil {
		return fmt.Errorf("list projects on boot: %w", err)
	}
	for _, p := range projs {
		if err := watcher.Add(p.ID, p.Path); err != nil {
			slog.Warn("failed to watch project on boot", "project", p.ID, "path", p.Path, "err", err)
			continue
		}
		if _, err := mgr.Rescan(ctx, p.ID); err != nil {
			slog.Warn("failed to rescan project on boot", "project", p.ID, "err", err)
		}
	}
	slog.Info("re-attached projects", "count", len(projs))

	// Socket setup.
	sockPath, err := ipc.SocketPath()
	if err != nil {
		return fmt.Errorf("socket path: %w", err)
	}
	if err := removeStaleSocket(sockPath); err != nil {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", sockPath, err)
	}
	defer ln.Close()
	if err := os.Chmod(sockPath, 0o600); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}

	// Idle-session sweeper: periodic supervisor that raises info-urgency
	// flags when a project's cmux binding goes stale or its surface title
	// hasn't changed for the configured idle timeout. Shim on top of
	// existing cmux polling — a proper heartbeat-driven implementation
	// arrives with M2 hooks.
	idleCmux := &cmux.Client{Password: cfg.Cmux.SocketPassword, Socket: cfg.Cmux.SocketPath}
	sweeper := &orchestrator.IdleSweeper{
		Store: st, Projects: mgr, Cmux: idleCmux, Attention: att,
	}
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		sweeper.Run(ctx)
	}()

	// Events retention sweeper: archives events older than the
	// configured window (default 90 days) to gzipped JSONL and deletes
	// them from SQLite. Set config.Events.RetentionDays < 0 to disable.
	logDir, _ := ipc.LogDir()
	archiveDir := filepath.Join(logDir, "archive")
	retention := &orchestrator.RetentionSweeper{
		Store:         st,
		Archive:       archive.New(archiveDir),
		RetentionDays: cfg.Events.RetentionDays,
	}
	if cfg.Events.SweepEveryHours > 0 {
		retention.Interval = time.Duration(cfg.Events.SweepEveryHours) * time.Hour
	}
	retentionDone := make(chan struct{})
	go func() {
		defer close(retentionDone)
		retention.Run(ctx)
	}()

	// Digest sweeper: composes + dispatches the daily/weekly rollup via
	// the messaging router. Disabled by default; enable in config.yaml
	// under messaging.digest.
	digest := &orchestrator.DigestSweeper{
		Store:    st,
		Router:   router,
		Enabled:  cfg.Messaging.Digest.Enabled,
		At:       cfg.Messaging.Digest.At,
		Cadence:  cfg.Messaging.Digest.Cadence,
		Backends: cfg.Messaging.Digest.Backends,
	}
	if cfg.Messaging.Digest.WindowHours > 0 {
		digest.Window = time.Duration(cfg.Messaging.Digest.WindowHours) * time.Hour
	}
	digestDone := make(chan struct{})
	go func() {
		defer close(digestDone)
		digest.Run(ctx)
	}()

	srv := ipc.NewServer()
	registerMethods(srv, st, mgr, watcher, att, cfg, digest)

	slog.Info("chiefd started", "socket", sockPath, "version", Version, "pid", os.Getpid())

	var wg sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := ln.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				slog.Error("accept error", "err", err)
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				if err := srv.Serve(conn); err != nil {
					slog.Debug("connection ended", "err", err)
				}
			}()
		}
	}()

	<-ctx.Done()
	slog.Info("shutdown signal received")
	_ = ln.Close()
	<-acceptDone
	<-watchDone
	<-sweepDone
	<-retentionDone
	<-digestDone

	waitDone := make(chan struct{})
	go func() { wg.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		slog.Warn("handlers did not finish in time; exiting")
	}
	slog.Info("chiefd stopped")
	return nil
}

// removeStaleSocket removes a leftover socket file, refusing if another
// chiefd is currently listening on it.
func removeStaleSocket(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s exists and is not a socket; refusing to remove", path)
	}
	c, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err == nil {
		_ = c.Close()
		return fmt.Errorf("another chiefd is already listening at %s", path)
	}
	return os.Remove(path)
}

func setupLogger() (*slog.Logger, func(), error) {
	dir, err := ipc.LogDir()
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "chiefd.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	h := slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(h), func() { _ = f.Close() }, nil
}

// registerMethods wires the per-milestone method surface. Handlers close over
// the store/manager/watcher via method-level factories to avoid globals.
func registerMethods(s *ipc.Server, st *store.Store, mgr *project.Manager, w *fswatch.Watcher, att *attention.Manager, cfg config.Config, digest *orchestrator.DigestSweeper) {
	s.Register("ping", handlePing)
	s.Register("project.add", handleProjectAdd(st, mgr, w))
	s.Register("project.list", handleProjectList(st))
	s.Register("project.remove", handleProjectRemove(mgr, w))
	s.Register("project.rescan", handleProjectRescan(mgr))
	s.Register("backlog.list", handleBacklogList(st))
	s.Register("backlog.next", handleBacklogNext(st))
	s.Register("stats.summary", handleStatsSummary(st))
	s.Register("search", handleSearch(st))
	s.Register("digest.preview", handleDigestPreview(digest))
	s.Register("digest.fire", handleDigestFire(digest))
	s.Register("task.show", handleTaskShow(st))
	s.Register("task.add", handleTaskAdd(mgr))
	s.Register("task.update", handleTaskUpdate(mgr))
	s.Register("task.reorder", handleTaskReorder(mgr))
	s.Register("task.delete", handleTaskDelete(mgr))

	// cmux integration (Feature B — send task to a running Claude pane).
	// Shares cfg with the sweeper wired in run() so both use the same
	// socket auth.
	cmuxClient := &cmux.Client{
		Password: cfg.Cmux.SocketPassword,
		Socket:   cfg.Cmux.SocketPath,
	}
	s.Register("cmux.list", handleCmuxList(cmuxClient))
	s.Register("cmux.candidates", handleCmuxCandidates(cmuxClient, mgr))
	s.Register("cmux.bind", handleCmuxBind(mgr))
	s.Register("task.send", handleTaskSend(cmuxClient, mgr, st))
	s.Register("task.send_batch", handleTaskSendBatch(cmuxClient, mgr, st))
	s.Register("project.sessions", handleProjectSessions(mgr))
	s.Register("analyzer.install", handleAnalyzerInstall())

	s.Register("historyviewer.status", handleHistoryViewerStatus())
	s.Register("historyviewer.install", handleHistoryViewerInstall())
	s.Register("historyviewer.open", handleHistoryViewerOpen(mgr))

	// Attention pipeline: raise/list/answer/count flags + next-task actions.
	s.Register("flag.raise", handleFlagRaise(st, att))
	s.Register("flag.list", handleFlagList(st))
	s.Register("flag.answer", handleFlagAnswer(st, att))
	s.Register("flag.count", handleFlagCount(st))
	s.Register("next.approve", handleNextApprove(cmuxClient, mgr, st, att))
	s.Register("next.skip", handleNextSkip(mgr, st, att))
	s.Register("next.snooze", handleNextSnooze(st, att))
}

// ---------- flag / next handlers ----------

// enrichFlag joins a flag row with the project name so the UI/CLI don't
// need a second lookup per row.
func enrichFlag(st *store.Store, f store.Flag) methods.FlagRow {
	row := methods.FlagRow{Flag: f}
	if p, err := st.GetProject(context.Background(), f.ProjectID); err == nil {
		row.ProjectName = p.Name
	}
	return row
}

func handleFlagRaise(st *store.Store, att *attention.Manager) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.FlagRaiseRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.ProjectID == "" || req.Question == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "project_id and question required"}
		}
		ctx := context.Background()
		// Resolve project by id/name/path so callers can use any identifier.
		p, err := st.GetProject(ctx, req.ProjectID)
		if err != nil {
			return nil, err
		}
		res, err := att.Raise(ctx, attention.RaiseOpts{
			ProjectID:   p.ID,
			SessionID:   req.SessionID,
			Kind:        store.FlagKind(req.Kind),
			Urgency:     store.FlagUrgency(req.Urgency),
			Question:    req.Question,
			SuggestedID: req.SuggestedID,
		})
		if err != nil {
			return nil, err
		}
		_ = st.InsertEvent(ctx, p.ID, req.SessionID, "flag.raised", map[string]any{
			"flag_id": res.Flag.ID, "urgency": res.Flag.Urgency, "coalesced": res.Coalesced,
			"rate_limited": res.RateLimited, "snoozed": res.SnoozedProject,
		})
		return methods.FlagRaiseResponse{
			Flag:           enrichFlag(st, res.Flag),
			Coalesced:      res.Coalesced,
			NotifiedMacOS:  res.NotifiedMacOS,
			RateLimited:    res.RateLimited,
			SnoozedProject: res.SnoozedProject,
			InDND:          res.InDND,
		}, nil
	}
}

func handleFlagList(st *store.Store) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.FlagListRequest
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
			}
		}
		ctx := context.Background()
		filter := store.FlagFilter{OpenOnly: req.OpenOnly, Kind: store.FlagKind(req.Kind), Limit: req.Limit}
		if req.ProjectID != "" {
			p, err := st.GetProject(ctx, req.ProjectID)
			if err != nil {
				return nil, err
			}
			filter.ProjectID = p.ID
		}
		flags, err := st.ListFlags(ctx, filter)
		if err != nil {
			return nil, err
		}
		rows := make([]methods.FlagRow, 0, len(flags))
		for _, f := range flags {
			rows = append(rows, enrichFlag(st, f))
		}
		return methods.FlagListResponse{Flags: rows}, nil
	}
}

func handleFlagAnswer(st *store.Store, att *attention.Manager) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.FlagAnswerRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.FlagID == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "flag_id required"}
		}
		ctx := context.Background()
		res := store.FlagResolution(req.Resolution)
		if res == "" {
			res = store.ResolutionAnswered
		}
		f, err := att.Ack(ctx, req.FlagID, req.Reply, res)
		if err != nil {
			return nil, err
		}
		_ = st.InsertEvent(ctx, f.ProjectID, "", "flag.answered", map[string]any{
			"flag_id": f.ID, "resolution": res,
		})
		return methods.FlagAnswerResponse{Flag: enrichFlag(st, f)}, nil
	}
}

func handleFlagCount(st *store.Store) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.FlagCountRequest
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &req)
		}
		ctx := context.Background()
		pid := ""
		if req.ProjectID != "" {
			p, err := st.GetProject(ctx, req.ProjectID)
			if err != nil {
				return nil, err
			}
			pid = p.ID
		}
		n, err := st.CountOpenFlags(ctx, pid)
		if err != nil {
			return nil, err
		}
		return methods.FlagCountResponse{Open: n}, nil
	}
}

func handleNextApprove(cc *cmux.Client, mgr *project.Manager, st *store.Store, att *attention.Manager) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.NextApproveRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.FlagID == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "flag_id required"}
		}
		ctx := context.Background()
		f, err := st.GetFlag(ctx, req.FlagID)
		if err != nil {
			return nil, err
		}
		if f.Kind != store.FlagKindNextTask || f.SuggestedID == nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "flag is not a next-task suggestion"}
		}
		t, err := st.GetTask(ctx, *f.SuggestedID)
		if err != nil {
			return nil, err
		}
		// Reuse the existing task.send handler logic by inlining the send.
		pf, _ := mgr.ReadYAML(ctx, t.ProjectID)
		surfaceRef := pf.Cmux.SurfaceID
		if surfaceRef == "" {
			return nil, &ipc.RPCError{Code: methods.ErrCodeCmuxUnbound, Message: "no cmux surface bound for this project"}
		}
		prompt := renderTaskPrompt(t, "")
		if err := cc.SendAndRun(ctx, surfaceRef, prompt); err != nil {
			return nil, err
		}
		ackedFlag, err := att.Ack(ctx, f.ID, "", store.ResolutionApproved)
		if err != nil {
			return nil, err
		}
		_ = st.InsertEvent(ctx, t.ProjectID, "", "next.approved",
			map[string]any{"flag_id": f.ID, "task_id": t.ID, "surface": surfaceRef})
		return methods.NextApproveResponse{
			Flag: enrichFlag(st, ackedFlag), SurfaceRef: surfaceRef, Prompt: prompt,
		}, nil
	}
}

func handleNextSkip(mgr *project.Manager, st *store.Store, att *attention.Manager) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.NextSkipRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.FlagID == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "flag_id required"}
		}
		ctx := context.Background()
		f, err := st.GetFlag(ctx, req.FlagID)
		if err != nil {
			return nil, err
		}
		// Ack the flag; the suggested task itself is left pending (skip
		// != defer). Deferring is a heavier operation the user can do
		// through the normal task UI. Keeps this action reversible.
		acked, err := att.Ack(ctx, f.ID, req.Reason, store.ResolutionSkipped)
		if err != nil {
			return nil, err
		}
		_ = st.InsertEvent(ctx, f.ProjectID, "", "next.skipped",
			map[string]any{"flag_id": f.ID, "reason": req.Reason})
		return methods.NextSkipResponse{Flag: enrichFlag(st, acked), Deferred: false}, nil
	}
}

func handleNextSnooze(st *store.Store, att *attention.Manager) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.NextSnoozeRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.Minutes <= 0 {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "minutes must be > 0"}
		}
		ctx := context.Background()
		var projectID string
		var flagID string
		switch {
		case req.FlagID != "":
			f, err := st.GetFlag(ctx, req.FlagID)
			if err != nil {
				return nil, err
			}
			projectID = f.ProjectID
			flagID = f.ID
			_, _ = att.Ack(ctx, f.ID, fmt.Sprintf("snoozed %d min", req.Minutes), store.ResolutionSnoozed)
		case req.Project != "":
			p, err := st.GetProject(ctx, req.Project)
			if err != nil {
				return nil, err
			}
			projectID = p.ID
		default:
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "flag_id or project required"}
		}
		until := time.Now().UTC().Add(time.Duration(req.Minutes) * time.Minute)
		att.Snooze(projectID, until)
		_ = st.InsertEvent(ctx, projectID, "", "next.snoozed",
			map[string]any{"flag_id": flagID, "until": until.Format(time.RFC3339)})
		return methods.NextSnoozeResponse{UntilRFC3339: until.Format(time.RFC3339), ProjectID: projectID}, nil
	}
}

// ---------- analyzer.install ----------

func handleAnalyzerInstall() ipc.Handler {
	return func(_ ipc.HandlerContext, _ json.RawMessage) (any, error) {
		// Long-lived: git clone + go build can take 30-90s. Use a generous
		// timeout on the RPC layer's context; the client will block until we
		// finish. Wails frontend shows a spinner + log in a modal.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		r := installer.InstallAnalyzer(ctx)
		return methods.AnalyzerInstallResponse{
			OK: r.OK, CLIPath: r.CLIPath, AppPath: r.AppPath,
			Log: r.Log, Steps: r.Steps, DurationMS: r.DurationMS, Error: r.Error,
		}, nil
	}
}

// ---------- historyviewer handlers ----------

const historyViewerRepoURL = "https://github.com/geekychris/history_viewer"

func handleHistoryViewerStatus() ipc.Handler {
	return func(_ ipc.HandlerContext, _ json.RawMessage) (any, error) {
		installed, bin := historyviewer.IsInstalled()
		app := historyviewer.AppBundlePath()
		return methods.HistoryViewerStatusResponse{
			Installed:  installed,
			Binary:     bin,
			HasApp:     app != "",
			AppPath:    app,
			HasBrew:    historyviewer.HomebrewAvailable(),
			InstallURL: historyViewerRepoURL,
		}, nil
	}
}

func handleHistoryViewerInstall() ipc.Handler {
	return func(_ ipc.HandlerContext, _ json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		r := installer.InstallHistoryViewer(ctx)
		return methods.HistoryViewerInstallResponse{
			OK: r.OK, CLIPath: r.CLIPath, Log: r.Log,
			Steps: r.Steps, DurationMS: r.DurationMS, Error: r.Error,
		}, nil
	}
}

// handleHistoryViewerOpen prefers, in order:
//
//  1. Redirect a running instance in place via POST /api/filter/directory,
//     so an already-open native window updates its filter without spawning
//     a duplicate (requires history_viewer commit with the filter API + hv-app
//     port file).
//  2. Launch the native .app with --filter-dir <path>.
//  3. Fall back to spawning the raw CLI on a fixed loopback port and
//     opening the default browser at ?dir=<path>.
func handleHistoryViewerOpen(mgr *project.Manager) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.HistoryViewerOpenRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.IDOrPath == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "id_or_path required"}
		}
		p, err := mgr.Store.GetProject(context.Background(), req.IDOrPath)
		if err != nil {
			return nil, err
		}

		// Path 0: an instance is already running — redirect its filter in
		// place and bring the window forward.
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		if addr := historyviewer.RunningInstance(ctx); addr != "" {
			if err := historyviewer.NavigateFilter(ctx, addr, p.Path); err == nil {
				// Bring app forward (best-effort; the .app is running so
				// LaunchServices reactivates it without spawning a duplicate).
				_ = exec.Command("open", "-a", "History Viewer").Start()
				return methods.HistoryViewerOpenResponse{
					FilterDir: p.Path, Spawned: false, Mode: "app-navigate",
				}, nil
			}
			// Navigate failed (endpoint missing on older binary, etc.);
			// fall through to spawn a fresh instance.
		}

		// Path 1: native app is installed — invoke its inner binary directly
		// so --args reliably reaches Wails (LaunchServices' `open --args`
		// doesn't consistently forward flags to Wails apps).
		if appBin := historyviewer.AppBinaryPath(); appBin != "" {
			cmd := exec.Command(appBin, "--filter-dir", p.Path)
			cmd.Env = os.Environ()
			if err := cmd.Start(); err != nil {
				return nil, fmt.Errorf("spawn History Viewer.app: %w", err)
			}
			return methods.HistoryViewerOpenResponse{
				FilterDir: p.Path, Spawned: true, Mode: "app",
			}, nil
		}

		// Path 2: only the CLI is installed. Spawn on the fixed port + point
		// the default browser at ?dir=<path>.
		bin := historyviewer.BinaryPath()
		if bin == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInternal, Message: "history_viewer is not installed; run historyviewer.install first"}
		}
		port := historyviewer.DefaultPort
		spawned := false
		if !viewerAlive(port) {
			cmd := exec.Command(bin,
				"--ui", "web",
				"--port", fmt.Sprintf("%d", port),
				"--filter-dir", p.Path,
			)
			cmd.Env = os.Environ()
			if err := cmd.Start(); err != nil {
				return nil, fmt.Errorf("spawn history_viewer: %w", err)
			}
			spawned = true
			deadline := time.Now().Add(10 * time.Second)
			for !viewerAlive(port) && time.Now().Before(deadline) {
				time.Sleep(200 * time.Millisecond)
			}
		}
		u := fmt.Sprintf("http://127.0.0.1:%d/?dir=%s", port, url.QueryEscape(p.Path))
		return methods.HistoryViewerOpenResponse{
			URL: u, FilterDir: p.Path, Port: port, Spawned: spawned, Mode: "web",
		}, nil
	}
}

// viewerAlive probes whether history_viewer is already serving on the port.
func viewerAlive(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ---------- project.sessions ----------

const analyzerRepoURL = "https://github.com/geekychris/claude-session-analyzer"

func handleProjectSessions(mgr *project.Manager) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.ProjectSessionsRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.IDOrPath == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "id_or_path required"}
		}
		p, err := mgr.Store.GetProject(context.Background(), req.IDOrPath)
		if err != nil {
			return nil, err
		}
		slug := claudetrace.SlugForPath(p.Path)
		dir, _ := claudetrace.SessionsDir(p.Path)
		sessions, err := claudetrace.ListSessions(p.Path)
		if err != nil {
			return nil, err
		}
		installed, invocation := claudetrace.AnalyzerInstalled()

		out := methods.ProjectSessionsResponse{
			ProjectID: p.ID, Slug: slug, SessionsDir: dir,
			AnalyzerInstalled: installed, AnalyzerInvocation: invocation,
			AnalyzerRepoURL: analyzerRepoURL,
		}
		for _, s := range sessions {
			out.Sessions = append(out.Sessions, methods.SessionInfo{
				ID: s.ID, Path: s.Path, Size: s.Size,
				Modified: s.Modified.Format(time.RFC3339),
			})
		}
		return out, nil
	}
}

// ---------- cmux handlers ----------

func handleCmuxList(cc *cmux.Client) ipc.Handler {
	return func(_ ipc.HandlerContext, _ json.RawMessage) (any, error) {
		ss, err := cc.ListSurfaces(context.Background())
		if err != nil {
			return nil, err
		}
		return methods.CmuxListResponse{Surfaces: toWireSurfaces(ss)}, nil
	}
}

func handleCmuxCandidates(cc *cmux.Client, mgr *project.Manager) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.CmuxCandidatesRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.ProjectID == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "project_id required"}
		}
		ctx := context.Background()
		p, err := mgr.Store.GetProject(ctx, req.ProjectID)
		if err != nil {
			return nil, err
		}
		all, err := cc.ListSurfaces(ctx)
		if err != nil {
			return nil, err
		}
		var matches []cmux.Surface
		for _, s := range all {
			if req.ClaudeOnly && !s.IsClaude {
				continue
			}
			if s.CWD == p.Path || strings.HasPrefix(s.CWD, p.Path+"/") {
				matches = append(matches, s)
			}
		}
		// Read currently-bound surface (if any) for the UI to preselect.
		var bound string
		if pf, err := mgr.ReadYAML(ctx, p.ID); err == nil {
			bound = pf.Cmux.SurfaceID
		}
		return methods.CmuxCandidatesResponse{
			ProjectID:      p.ID,
			ProjectPath:    p.Path,
			CurrentSurface: bound,
			CWDMatches:     toWireSurfaces(matches),
			AllSurfaces:    toWireSurfaces(all),
		}, nil
	}
}

func handleCmuxBind(mgr *project.Manager) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.CmuxBindRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.ProjectID == "" || req.SurfaceRef == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "project_id and surface_ref required"}
		}
		ctx := context.Background()
		p, err := mgr.Store.GetProject(ctx, req.ProjectID)
		if err != nil {
			return nil, err
		}
		pf, err := mgr.WriteYAML(ctx, p.ID, func(pf *project.ProjectFile) {
			pf.Cmux.SurfaceID = req.SurfaceRef
		})
		if err != nil {
			return nil, err
		}
		_ = mgr.Store.InsertEvent(ctx, p.ID, "", "cmux.bound",
			map[string]any{"surface_ref": pf.Cmux.SurfaceID})
		return methods.CmuxBindResponse{ProjectID: p.ID, SurfaceRef: pf.Cmux.SurfaceID}, nil
	}
}

func handleTaskSend(cc *cmux.Client, mgr *project.Manager, st *store.Store) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.TaskSendRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.TaskID == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "task_id required"}
		}
		ctx := context.Background()
		t, err := st.GetTask(ctx, req.TaskID)
		if err != nil {
			return nil, err
		}

		// Resolve target surface: explicit override first, then persisted binding.
		surfaceRef := req.SurfaceOverride
		if surfaceRef == "" {
			pf, _ := mgr.ReadYAML(ctx, t.ProjectID)
			surfaceRef = pf.Cmux.SurfaceID
		}
		if surfaceRef == "" {
			return nil, &ipc.RPCError{Code: methods.ErrCodeCmuxUnbound, Message: "no cmux surface bound for this project; call cmux.candidates + cmux.bind first"}
		}

		// Verify the surface still exists (cmux may have been restarted).
		surfaces, err := cc.ListSurfaces(ctx)
		if err != nil {
			return nil, err
		}
		found := false
		for _, s := range surfaces {
			if s.Ref == surfaceRef {
				found = true
				break
			}
		}
		if !found {
			return nil, &ipc.RPCError{Code: methods.ErrCodeCmuxSurfaceGone, Message: "bound cmux surface " + surfaceRef + " no longer exists; rebind"}
		}

		// If an override was provided, persist it so subsequent sends stick.
		if req.SurfaceOverride != "" {
			_, _ = mgr.WriteYAML(ctx, t.ProjectID, func(pf *project.ProjectFile) {
				pf.Cmux.SurfaceID = req.SurfaceOverride
			})
		}

		// Auto-branch (e98f): if enabled for this project, git switch -c
		// chief/<id>-<slug> in the project dir before sending the prompt.
		// Best-effort — Skipped reasons (not-a-repo, dirty tree) are
		// logged but never fail the send.
		pf, _ := mgr.ReadYAML(ctx, t.ProjectID)
		if pf.AutoBranch.Enable {
			proj, _ := st.GetProject(ctx, t.ProjectID)
			if br, err := gitshim.EnsureBranch(ctx, proj.Path, t.ID, t.Title); err != nil {
				slog.Warn("autobranch failed", "project", proj.Path, "task", t.ID, "err", err)
			} else if br.Created {
				slog.Info("autobranch created", "branch", br.Branch, "project", proj.Path)
				_ = st.InsertEvent(ctx, t.ProjectID, "", "autobranch.created",
					map[string]any{"task_id": t.ID, "branch": br.Branch})
			} else if br.Skipped != "" {
				slog.Info("autobranch skipped", "reason", br.Skipped, "branch", br.Branch)
			}
		}

		prompt := renderTaskPrompt(t, req.ExtraInstruction)
		if err := cc.SendAndRun(ctx, surfaceRef, prompt); err != nil {
			return nil, err
		}
		_ = st.InsertEvent(ctx, t.ProjectID, "", "task.sent",
			map[string]any{"task_id": t.ID, "surface": surfaceRef})
		return methods.TaskSendResponse{SurfaceRef: surfaceRef, Prompt: prompt}, nil
	}
}

// handleTaskSendBatch composes a single prompt from N tasks and delivers
// it to the surface bound to their (single) project. Requires all task_ids
// to be in the same project — cmux surfaces are per-project.
func handleTaskSendBatch(cc *cmux.Client, mgr *project.Manager, st *store.Store) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.TaskSendBatchRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if len(req.TaskIDs) == 0 {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "task_ids required"}
		}
		ctx := context.Background()

		// Load tasks + validate same-project.
		tasks := make([]store.Task, 0, len(req.TaskIDs))
		var projectID string
		for _, id := range req.TaskIDs {
			t, err := st.GetTask(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("task %s: %w", id, err)
			}
			if projectID == "" {
				projectID = t.ProjectID
			} else if t.ProjectID != projectID {
				return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "task.send_batch: all task_ids must belong to the same project (cmux surfaces are per-project)"}
			}
			tasks = append(tasks, t)
		}

		// Resolve target surface: explicit override first, then persisted binding.
		surfaceRef := req.SurfaceOverride
		if surfaceRef == "" {
			pf, _ := mgr.ReadYAML(ctx, projectID)
			surfaceRef = pf.Cmux.SurfaceID
		}
		if surfaceRef == "" {
			return nil, &ipc.RPCError{Code: methods.ErrCodeCmuxUnbound, Message: "no cmux surface bound for this project; call cmux.candidates + cmux.bind first"}
		}

		// Verify the surface is alive.
		surfaces, err := cc.ListSurfaces(ctx)
		if err != nil {
			return nil, err
		}
		found := false
		for _, s := range surfaces {
			if s.Ref == surfaceRef {
				found = true
				break
			}
		}
		if !found {
			return nil, &ipc.RPCError{Code: methods.ErrCodeCmuxSurfaceGone, Message: "bound cmux surface " + surfaceRef + " no longer exists; rebind"}
		}

		// Persist override if provided.
		if req.SurfaceOverride != "" {
			_, _ = mgr.WriteYAML(ctx, projectID, func(pf *project.ProjectFile) {
				pf.Cmux.SurfaceID = req.SurfaceOverride
			})
		}

		prompt := renderBatchPrompt(tasks, req.ExtraInstruction)
		if err := cc.SendAndRun(ctx, surfaceRef, prompt); err != nil {
			return nil, err
		}
		for _, t := range tasks {
			_ = st.InsertEvent(ctx, t.ProjectID, "", "task.sent",
				map[string]any{"task_id": t.ID, "surface": surfaceRef, "batch": true, "batch_size": len(tasks)})
		}
		return methods.TaskSendBatchResponse{
			SurfaceRef: surfaceRef, Prompt: prompt,
			Count: len(tasks), TaskIDs: req.TaskIDs,
		}, nil
	}
}

// renderBatchPrompt composes a "please do these N tasks in order" prompt.
// Format is chosen so Claude sees the list as a single user turn and works
// through them serially, marking each done before moving on.
func renderBatchPrompt(tasks []store.Task, extra string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[chief batch — %d tasks from the backlog]\n\n", len(tasks))
	fmt.Fprintln(&b, "Please work through the following tasks in order. As you finish each one, mark it complete in backlog.md by changing `- [ ]` to `- [x]` on the line beginning with `{id:XXXX}`, then move on to the next.")
	fmt.Fprintln(&b)
	for i, t := range tasks {
		fmt.Fprintf(&b, "─── %d/%d ─── [id:%s] %s\n", i+1, len(tasks), t.ID, t.Title)
		if t.Category != "" {
			fmt.Fprintf(&b, "Category: %s\n", t.Category)
		}
		if t.Priority != 0 {
			fmt.Fprintf(&b, "Priority: %d\n", t.Priority)
		}
		if len(t.RequiredResources) > 0 {
			fmt.Fprintf(&b, "Resources: %s\n", strings.Join(t.RequiredResources, ", "))
		}
		if t.Body != "" {
			fmt.Fprintln(&b)
			fmt.Fprintln(&b, t.Body)
		}
		fmt.Fprintln(&b)
	}
	if extra != "" {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, extra)
	}
	return b.String()
}

// renderTaskPrompt turns a Task into the user prompt that gets typed into
// Claude's pane. Ends with "\n" so cmux submits it as one message.
func renderTaskPrompt(t store.Task, extra string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[chief task %s] %s\n", t.ID, t.Title)
	if t.Category != "" {
		fmt.Fprintf(&b, "Category: %s\n", t.Category)
	}
	if t.Priority != 0 {
		fmt.Fprintf(&b, "Priority: %d\n", t.Priority)
	}
	if len(t.RequiredResources) > 0 {
		fmt.Fprintf(&b, "Resources: %s\n", strings.Join(t.RequiredResources, ", "))
	}
	if t.Body != "" {
		b.WriteString("\n")
		b.WriteString(t.Body)
		b.WriteString("\n")
	}
	b.WriteString("\nPlease work on this backlog item. When it's done, mark it complete in backlog.md by changing `- [ ]` to `- [x]` on the line beginning with `{id:")
	b.WriteString(t.ID)
	b.WriteString("}`.\n")
	if extra != "" {
		b.WriteString("\n")
		b.WriteString(extra)
		b.WriteString("\n")
	}
	return b.String()
}

func toWireSurfaces(in []cmux.Surface) []methods.CmuxSurface {
	out := make([]methods.CmuxSurface, 0, len(in))
	for _, s := range in {
		out = append(out, methods.CmuxSurface{
			Ref: s.Ref, Title: s.Title, CWD: s.CWD, Type: s.Type,
			Focused: s.Focused, IsClaude: s.IsClaude,
			ClaudeSessionID: s.ClaudeSessionID, AgentName: s.AgentName,
			Launcher: s.Launcher,
		})
	}
	return out
}

func handleTaskAdd(mgr *project.Manager) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.TaskAddRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.ProjectID == "" || req.Title == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "project_id and title required"}
		}
		p, err := mgr.Store.GetProject(context.Background(), req.ProjectID)
		if err != nil {
			return nil, err
		}
		t, err := mgr.AddTask(context.Background(), p.ID, project.AddTaskOpts{
			Title: req.Title, Body: req.Body, Category: req.Category,
			Priority: req.Priority, RequiredResources: req.RequiredResources, Due: req.Due,
		})
		if err != nil {
			return nil, err
		}
		_ = mgr.Store.InsertEvent(context.Background(), p.ID, "", "task.added", map[string]any{
			"task_id": t.ID, "title": t.Title, "priority": t.Priority,
			"category": t.Category, "outcome": "ok",
		})
		return methods.TaskAddResponse{Task: t}, nil
	}
}

func handleTaskUpdate(mgr *project.Manager) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.TaskUpdateRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.TaskID == "" || req.Title == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "task_id and title required"}
		}
		opts := project.UpdateTaskOpts{
			Title: req.Title, Priority: req.Priority, Category: req.Category,
			RequiredResources: req.RequiredResources, Due: req.Due,
		}
		if req.UpdateBody {
			b := req.Body
			opts.Body = &b
		}
		t, err := mgr.UpdateTask(context.Background(), req.TaskID, opts)
		if err != nil {
			return nil, err
		}
		_ = mgr.Store.InsertEvent(context.Background(), t.ProjectID, "", "task.updated", map[string]any{
			"task_id": t.ID, "title": t.Title, "priority": t.Priority,
			"category": t.Category, "outcome": "ok",
		})
		return methods.TaskUpdateResponse{Task: t}, nil
	}
}

func handleTaskReorder(mgr *project.Manager) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.TaskReorderRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.TaskID == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "task_id required"}
		}
		before, err := mgr.Store.GetTask(context.Background(), req.TaskID)
		if err != nil {
			return nil, err
		}
		after, err := mgr.MoveTask(context.Background(), req.TaskID, req.Direction)
		if err != nil {
			return nil, err
		}
		applied := before.SourceLine != after.SourceLine
		_ = mgr.Store.InsertEvent(context.Background(), after.ProjectID, "", "task.reordered", map[string]any{
			"task_id":   after.ID,
			"direction": req.Direction,
			"applied":   applied,
			"outcome":   "ok",
		})
		return methods.TaskReorderResponse{Task: after, Applied: applied}, nil
	}
}

func handleTaskDelete(mgr *project.Manager) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.TaskDeleteRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.TaskID == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "task_id required"}
		}
		// Look up before delete so we can log the project/title.
		before, _ := mgr.Store.GetTask(context.Background(), req.TaskID)
		if err := mgr.DeleteTask(context.Background(), req.TaskID); err != nil {
			return nil, err
		}
		_ = mgr.Store.InsertEvent(context.Background(), before.ProjectID, "", "task.deleted", map[string]any{
			"task_id": before.ID, "title": before.Title, "outcome": "ok",
		})
		return methods.TaskDeleteResponse{Deleted: true}, nil
	}
}

// ---------- ping ----------

type pingResult struct {
	Pong    bool   `json:"pong"`
	Version string `json:"version"`
	PID     int    `json:"pid"`
	Time    string `json:"time"`
}

func handlePing(_ ipc.HandlerContext, _ json.RawMessage) (any, error) {
	return pingResult{
		Pong:    true,
		Version: Version,
		PID:     os.Getpid(),
		Time:    time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// ---------- project.add ----------

func handleProjectAdd(st *store.Store, mgr *project.Manager, w *fswatch.Watcher) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		start := time.Now()
		var req methods.ProjectAddRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.Path == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "path required"}
		}
		p, err := mgr.Register(context.Background(), project.RegisterOpts{
			Path: req.Path, Name: req.Name, SpawnMode: req.SpawnMode,
		})
		if err != nil {
			return nil, err
		}
		// Start watching the new project's dir now that it's persisted.
		if err := w.Add(p.ID, p.Path); err != nil {
			slog.Warn("watcher.Add failed", "project", p.ID, "err", err)
		}
		tasks, err := st.ListTasks(context.Background(), store.TaskFilter{ProjectID: p.ID})
		if err != nil {
			return nil, err
		}
		_ = st.InsertEvent(context.Background(), p.ID, "", "project.added", map[string]any{
			"path": p.Path, "name": p.Name, "spawn_mode": p.SpawnMode,
			"tasks_imported": len(tasks),
			"duration_ms":    time.Since(start).Milliseconds(),
			"outcome":        "ok",
		})
		return methods.ProjectAddResponse{Project: p, TasksImported: len(tasks)}, nil
	}
}

// ---------- project.list ----------

func handleProjectList(st *store.Store) ipc.Handler {
	return func(_ ipc.HandlerContext, _ json.RawMessage) (any, error) {
		ctx := context.Background()
		projs, err := st.ListProjects(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]methods.ProjectSummary, 0, len(projs))
		for _, p := range projs {
			sum := methods.ProjectSummary{Project: p}
			tasks, err := st.ListTasks(ctx, store.TaskFilter{ProjectID: p.ID})
			if err != nil {
				return nil, err
			}
			for _, t := range tasks {
				switch t.Status {
				case store.TaskDone:
					sum.DoneTasks++
				case store.TaskDeferred:
					sum.DeferredTasks++
				default:
					sum.PendingTasks++
				}
			}
			out = append(out, sum)
		}
		return methods.ProjectListResponse{Projects: out}, nil
	}
}

// ---------- project.remove ----------

func handleProjectRemove(mgr *project.Manager, w *fswatch.Watcher) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		start := time.Now()
		var req methods.ProjectRemoveRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		p, err := mgr.Store.GetProject(context.Background(), req.IDOrPath)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return methods.ProjectRemoveResponse{Removed: false}, nil
			}
			return nil, err
		}
		if err := mgr.Remove(context.Background(), p.ID); err != nil {
			return nil, err
		}
		w.Remove(p.ID)
		_ = mgr.Store.InsertEvent(context.Background(), p.ID, "", "project.removed", map[string]any{
			"path": p.Path, "name": p.Name,
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "ok",
		})
		return methods.ProjectRemoveResponse{Removed: true}, nil
	}
}

// ---------- project.rescan ----------

func handleProjectRescan(mgr *project.Manager) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		start := time.Now()
		var req methods.ProjectRescanRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		p, err := mgr.Store.GetProject(context.Background(), req.IDOrPath)
		if err != nil {
			return nil, err
		}
		tasks, err := mgr.Rescan(context.Background(), p.ID)
		if err != nil {
			return nil, err
		}
		_ = mgr.Store.InsertEvent(context.Background(), p.ID, "", "project.rescanned", map[string]any{
			"tasks":       len(tasks),
			"trigger":     "cli",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "ok",
		})
		return methods.ProjectRescanResponse{Tasks: len(tasks)}, nil
	}
}

// ---------- backlog.list ----------

func handleBacklogList(st *store.Store) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.BacklogListRequest
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
			}
		}
		ctx := context.Background()
		filter := store.TaskFilter{}
		if req.ProjectID != "" {
			p, err := st.GetProject(ctx, req.ProjectID)
			if err != nil {
				return nil, err
			}
			filter.ProjectID = p.ID
		}
		if req.Status != "" {
			filter.Status = store.TaskStatus(req.Status)
		}
		tasks, err := st.ListTasks(ctx, filter)
		if err != nil {
			return nil, err
		}
		// Enrich each row with the project name. Cache lookups by id.
		nameByID := map[string]string{}
		if projs, err := st.ListProjects(ctx); err == nil {
			for _, p := range projs {
				nameByID[p.ID] = p.Name
			}
		}
		rows := make([]methods.BacklogRow, 0, len(tasks))
		for _, t := range tasks {
			rows = append(rows, methods.BacklogRow{Task: t, ProjectName: nameByID[t.ProjectID]})
		}
		return methods.BacklogListResponse{Tasks: rows}, nil
	}
}

// ---------- backlog.next ----------

// handleBacklogNext returns the top-N pending tasks system-wide ranked
// by (priority DESC, source_line ASC). Powers `chief backlog next` +
// the UI's Next Up modal.
func handleBacklogNext(st *store.Store) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.BacklogNextRequest
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
			}
		}
		limit := req.Limit
		if limit <= 0 {
			limit = 10
		}
		ctx := context.Background()
		tasks, err := st.NextPending(ctx, limit)
		if err != nil {
			return nil, err
		}
		// Enrich with project names in one pass.
		nameByID := map[string]string{}
		if projs, err := st.ListProjects(ctx); err == nil {
			for _, p := range projs {
				nameByID[p.ID] = p.Name
			}
		}
		rows := make([]methods.BacklogRow, 0, len(tasks))
		for _, t := range tasks {
			rows = append(rows, methods.BacklogRow{Task: t, ProjectName: nameByID[t.ProjectID]})
		}
		return methods.BacklogNextResponse{Tasks: rows}, nil
	}
}

// ---------- digest.preview / digest.fire ----------

func handleDigestPreview(d *orchestrator.DigestSweeper) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.DigestPreviewRequest
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &req)
		}
		w := time.Duration(req.WindowHours) * time.Hour
		if w <= 0 {
			w = 24 * time.Hour
		}
		body, err := d.Compose(context.Background(), w)
		if err != nil {
			return nil, err
		}
		return methods.DigestPreviewResponse{Body: body}, nil
	}
}

func handleDigestFire(d *orchestrator.DigestSweeper) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.DigestFireRequest
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &req)
		}
		if req.WindowHours > 0 {
			d.Window = time.Duration(req.WindowHours) * time.Hour
		}
		if err := d.Fire(context.Background()); err != nil {
			return nil, err
		}
		return methods.DigestFireResponse{Sent: true}, nil
	}
}

// ---------- search ----------

func handleSearch(st *store.Store) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.SearchRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.Query == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "query required"}
		}
		ctx := context.Background()
		tasks, err := st.SearchTasks(ctx, req.Query, req.Limit)
		if err != nil {
			return nil, err
		}
		nameByID := map[string]string{}
		if projs, err := st.ListProjects(ctx); err == nil {
			for _, p := range projs {
				nameByID[p.ID] = p.Name
			}
		}
		rows := make([]methods.BacklogRow, 0, len(tasks))
		for _, t := range tasks {
			rows = append(rows, methods.BacklogRow{Task: t, ProjectName: nameByID[t.ProjectID]})
		}
		return methods.SearchResponse{Tasks: rows}, nil
	}
}

// ---------- stats.summary ----------

func handleStatsSummary(st *store.Store) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.StatsSummaryRequest
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &req)
		}
		window := req.WindowDays
		if window <= 0 {
			window = 7
		}
		ctx := context.Background()
		projs, err := st.ListProjects(ctx)
		if err != nil {
			return nil, err
		}
		since := time.Now().UTC().Add(-time.Duration(window) * 24 * time.Hour)
		lastEvent, _ := st.LastEventPerProject(ctx)
		globalRecent, _ := st.CountEventsByKindSince(ctx, "", since)
		totalEvents, _ := st.CountEvents(ctx)

		resp := methods.StatsSummaryResponse{
			WindowDays: window,
			Global: methods.StatsGlobal{
				Projects:        len(projs),
				CompletedWindow: int(globalRecent["rescan.completed"]),
				EventsTotal:     totalEvents,
			},
		}
		for _, p := range projs {
			row := methods.StatsPerProject{
				ID: p.ID, Name: p.Name, Path: p.Path, State: p.State,
			}
			tasks, err := st.ListTasks(ctx, store.TaskFilter{ProjectID: p.ID})
			if err == nil {
				for _, t := range tasks {
					switch t.Status {
					case store.TaskPending:
						row.Pending++
					case store.TaskActive:
						row.Active++
					case store.TaskDeferred:
						row.Deferred++
					case store.TaskBlocked:
						row.Blocked++
					case store.TaskDone:
						row.Done++
					}
				}
			}
			if n, err := st.CountOpenFlags(ctx, p.ID); err == nil {
				row.FlagsOpen = n
			}
			if t, ok := lastEvent[p.ID]; ok {
				row.LastActivity = t.Format(time.RFC3339)
			}
			perProjRecent, _ := st.CountEventsByKindSince(ctx, p.ID, since)
			row.CompletedWindow = int(perProjRecent["rescan.completed"])

			resp.Global.TasksPending += row.Pending
			resp.Global.TasksActive += row.Active
			resp.Global.TasksDeferred += row.Deferred
			resp.Global.TasksBlocked += row.Blocked
			resp.Global.TasksDone += row.Done
			resp.Global.FlagsOpen += row.FlagsOpen
			resp.Projects = append(resp.Projects, row)
		}
		return resp, nil
	}
}

// ---------- task.show ----------

func handleTaskShow(st *store.Store) ipc.Handler {
	return func(_ ipc.HandlerContext, raw json.RawMessage) (any, error) {
		var req methods.TaskShowRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: err.Error()}
		}
		if req.ID == "" {
			return nil, &ipc.RPCError{Code: ipc.ErrCodeInvalidArgs, Message: "id required"}
		}
		ctx := context.Background()
		t, err := st.GetTask(ctx, req.ID)
		if err != nil {
			return nil, err
		}
		p, _ := st.GetProject(ctx, t.ProjectID)
		return methods.TaskShowResponse{Task: t, ProjectName: p.Name}, nil
	}
}

// buildMessagingRouter constructs the messaging.Router from config +
// a per-project rules resolver that reads .chief/project.yaml. Always
// registers a LogBackend under the name "local-log" so there's a
// baseline audit trail even if the user hasn't configured anything.
func buildMessagingRouter(cfg config.Config, mgr *project.Manager) *messaging.Router {
	logDir, _ := ipc.LogDir()
	r := &messaging.Router{}
	r.Register(&messaging.LogBackend{
		NameStr: "local-log",
		Path:    filepath.Join(logDir, "messages.jsonl"),
	})
	// Configured backends. Unknown types are logged + skipped so a
	// typo in config doesn't wedge boot.
	for _, bc := range cfg.Messaging.Backends {
		switch bc.Type {
		case "log":
			r.Register(&messaging.LogBackend{NameStr: bc.Name, Path: bc.Path})
		case "ntfy":
			r.Register(&messaging.NtfyBackend{
				NameStr: bc.Name, Server: bc.Server, Topic: bc.Topic,
			})
		case "pushover":
			r.Register(&messaging.PushoverBackend{
				NameStr: bc.Name, Token: bc.Token, User: bc.User,
			})
		case "telegram":
			r.Register(&messaging.TelegramBackend{
				NameStr: bc.Name, Token: bc.Token, ChatID: bc.ChatID,
			})
		case "slack":
			r.Register(&messaging.SlackBackend{
				NameStr: bc.Name, Webhook: bc.Webhook,
			})
		default:
			slog.Warn("messaging: skipping backend with unknown type",
				"name", bc.Name, "type", bc.Type)
		}
	}
	// Global rules — default to LogBackend when nothing is configured
	// so at least the local audit trail runs.
	global := messaging.Rules{
		Default:    cfg.Messaging.Routing.Default,
		PerUrgency: convertPerUrgency(cfg.Messaging.Routing.PerUrgency),
		Disable:    cfg.Messaging.Routing.Disable,
	}
	if len(global.Default) == 0 && len(global.PerUrgency) == 0 && !global.Disable {
		global.Default = []string{"local-log"}
	}
	r.GlobalRules = global

	// Per-project override — read .chief/project.yaml on each dispatch.
	// Cheap enough for the message rate we're at; hot-reloadable
	// without a chiefd restart.
	r.PerProjectRules = func(projectID string) messaging.Rules {
		pf, err := mgr.ReadYAML(context.Background(), projectID)
		if err != nil {
			return messaging.Rules{}
		}
		if pf.Messaging.Disable {
			return messaging.Rules{Disable: true}
		}
		if len(pf.Messaging.Default) == 0 && len(pf.Messaging.PerUrgency) == 0 {
			return messaging.Rules{}
		}
		return messaging.Rules{
			Default:    pf.Messaging.Default,
			PerUrgency: convertPerUrgency(pf.Messaging.PerUrgency),
		}
	}
	return r
}

// convertPerUrgency turns config's map[string][]string into the typed
// map[messaging.Urgency][]string the router expects.
func convertPerUrgency(in map[string][]string) map[messaging.Urgency][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[messaging.Urgency][]string, len(in))
	for k, v := range in {
		out[messaging.Urgency(k)] = v
	}
	return out
}
