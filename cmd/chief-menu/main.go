// chief-menu is a lightweight macOS menu bar app for Chief.
//
// It polls chiefd every ~5s over the same unix socket the CLI uses and shows:
//
//   ♦ 7            <- title, glyph + total pending count across all projects
//     chiefd: up (dev)
//     3 projects · 7 pending · 2 deferred
//     ─────
//     alpha (3 pending)  ▶  Rescan / Copy path / Open in Finder / Open backlog.md
//     beta  (4 pending)  ▶  ...
//     ─────
//     Refresh now
//     Open data dir
//     Open logs
//     Quit
//
// This is the "M3-lite" shell. The full main window (backlog table, timeline,
// notification tray) lands in the follow-on M3 milestone.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/geekychris/chief/internal/ipc"
	"github.com/geekychris/chief/internal/methods"
	"github.com/getlantern/systray"
)

// Version is stamped at build time via -ldflags.
var Version = "dev"

// projectSlot is one reusable menu row. We pre-allocate a pool so we can
// hide/show slots as project count changes (systray only supports add,
// not remove).
type projectSlot struct {
	root    *systray.MenuItem
	rescan  *systray.MenuItem
	copyP   *systray.MenuItem
	openFin *systray.MenuItem
	openBk  *systray.MenuItem

	// Snapshot of the project this slot currently represents. Guarded by
	// slotsMu when mutated.
	cur methods.ProjectSummary
}

const maxProjectSlots = 20

var (
	mStatus   *systray.MenuItem
	mTotals   *systray.MenuItem
	mOpenUI   *systray.MenuItem
	mRefresh  *systray.MenuItem
	mDataDir  *systray.MenuItem
	mLogs     *systray.MenuItem
	mQuit     *systray.MenuItem
	slots     [maxProjectSlots]*projectSlot
	slotsMu   sync.Mutex
	pollEvery = 5 * time.Second
)

func main() {
	runtime.LockOSThread()
	systray.Run(onReady, onExit)
}

func onReady() {
	// Menu bar title. Icon (PNG bytes) would be nicer than a glyph; punt to
	// M3-full when we ship an .app bundle with proper icons.
	systray.SetTitle("♦")
	systray.SetTooltip("Chief")

	mStatus = systray.AddMenuItem("chiefd: (checking…)", "chiefd daemon status")
	mStatus.Disable()
	mTotals = systray.AddMenuItem("—", "cross-project totals")
	mTotals.Disable()
	systray.AddSeparator()

	// Project slot pool. Filled lazily each refresh; unused slots stay hidden.
	for i := 0; i < maxProjectSlots; i++ {
		root := systray.AddMenuItem("", "")
		root.Hide()
		slot := &projectSlot{root: root}
		slot.rescan = root.AddSubMenuItem("Rescan", "chief project rescan")
		slot.copyP = root.AddSubMenuItem("Copy path", "copy repo path to clipboard")
		slot.openFin = root.AddSubMenuItem("Open in Finder", "reveal in Finder")
		slot.openBk = root.AddSubMenuItem("Open backlog.md", "edit the file directly")
		slots[i] = slot
		go handleSlotClicks(slot)
	}

	systray.AddSeparator()
	mOpenUI = systray.AddMenuItem("Open Chief window", "open the main Chief.app window")
	mRefresh = systray.AddMenuItem("Refresh now", "poll chiefd immediately")
	mDataDir = systray.AddMenuItem("Open data dir", "reveal ~/Library/Application Support/Chief")
	mLogs = systray.AddMenuItem("Open logs", "reveal ~/Library/Logs/Chief")
	systray.AddSeparator()
	mQuit = systray.AddMenuItem("Quit chief-menu", "exit the menu bar app")

	go pollLoop()
	go actionLoop()
}

func onExit() {}

// actionLoop handles clicks on the fixed menu items (not per-project slots).
func actionLoop() {
	home, _ := os.UserHomeDir()
	for {
		select {
		case <-mOpenUI.ClickedCh:
			// `open -a` launches (or activates) the app bundle. If Chief.app
			// isn't installed yet this fails silently; nothing else to do.
			_ = exec.Command("open", "-a", filepath.Join(home, "Applications", "Chief.app")).Start()
		case <-mRefresh.ClickedCh:
			refresh()
		case <-mDataDir.ClickedCh:
			_ = openInFinder(filepath.Join(home, "Library", "Application Support", "Chief"))
		case <-mLogs.ClickedCh:
			_ = openInFinder(filepath.Join(home, "Library", "Logs", "Chief"))
		case <-mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

// handleSlotClicks runs one goroutine per slot, dispatching its four
// submenu-item click channels. Snapshot the current project inside the mutex
// so the click acts on whatever's shown right now, not a stale value.
func handleSlotClicks(s *projectSlot) {
	for {
		select {
		case <-s.rescan.ClickedCh:
			slotsMu.Lock()
			id := s.cur.ID
			slotsMu.Unlock()
			if id != "" {
				go func() {
					if err := callRescan(id); err != nil {
						log.Printf("rescan %s: %v", id, err)
					}
					refresh()
				}()
			}
		case <-s.copyP.ClickedCh:
			slotsMu.Lock()
			path := s.cur.Path
			slotsMu.Unlock()
			if path != "" {
				_ = pbcopy(path)
			}
		case <-s.openFin.ClickedCh:
			slotsMu.Lock()
			path := s.cur.Path
			slotsMu.Unlock()
			if path != "" {
				_ = openInFinder(path)
			}
		case <-s.openBk.ClickedCh:
			slotsMu.Lock()
			path := s.cur.Path
			slotsMu.Unlock()
			if path != "" {
				_ = openFile(filepath.Join(path, "backlog.md"))
			}
		}
	}
}

// pollLoop refreshes on a timer plus once immediately at boot.
func pollLoop() {
	// Kick once right away so the menu isn't stale.
	refresh()
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for range ticker.C {
		refresh()
	}
}

// refresh queries chiefd (via a fresh short-lived connection each time) and
// updates all menu items. Errors show as "chiefd: down"; the app stays alive.
func refresh() {
	pingResp, err := callPing()
	if err != nil {
		mStatus.SetTitle("chiefd: down")
		mTotals.SetTitle("chiefd unreachable — click Refresh")
		systray.SetTitle("♦!")
		systray.SetTooltip("Chief — chiefd is not running")
		hideAllSlots()
		return
	}
	mStatus.SetTitle(fmt.Sprintf("chiefd: up (v%s, pid %d)", pingResp.Version, pingResp.PID))

	projs, err := callProjectList()
	if err != nil {
		mTotals.SetTitle("list error — click Refresh")
		hideAllSlots()
		return
	}

	var totalPending, totalDeferred int
	for _, p := range projs {
		totalPending += p.PendingTasks
		totalDeferred += p.DeferredTasks
	}
	mTotals.SetTitle(fmt.Sprintf("%d project(s) · %d pending · %d deferred",
		len(projs), totalPending, totalDeferred))

	// Menu bar title: glyph + total pending as a mini badge.
	badge := ""
	if totalPending > 0 {
		badge = fmt.Sprintf(" %d", totalPending)
	}
	systray.SetTitle("♦" + badge)
	systray.SetTooltip(fmt.Sprintf("Chief — %d pending across %d project(s)", totalPending, len(projs)))

	// Fill slots.
	slotsMu.Lock()
	defer slotsMu.Unlock()
	for i := 0; i < maxProjectSlots; i++ {
		s := slots[i]
		if i < len(projs) {
			p := projs[i]
			s.cur = p
			s.root.SetTitle(fmt.Sprintf("%s  (%d pending / %d done)", p.Name, p.PendingTasks, p.DoneTasks))
			s.root.SetTooltip(p.Path)
			s.root.Show()
		} else {
			s.cur = methods.ProjectSummary{}
			s.root.Hide()
		}
	}
}

func hideAllSlots() {
	slotsMu.Lock()
	defer slotsMu.Unlock()
	for i := 0; i < maxProjectSlots; i++ {
		slots[i].cur = methods.ProjectSummary{}
		slots[i].root.Hide()
	}
}

// ---------- RPC calls: fresh connection per call to avoid tying menu ----------
// ---------- state to a socket that might drop during chiefd bounces ----------

func dial() (*ipc.Client, error) {
	sockPath, err := ipc.SocketPath()
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", sockPath, 500*time.Millisecond)
	if err != nil {
		return nil, err
	}
	return ipc.NewClient(conn), nil
}

type pingResp struct {
	Pong    bool   `json:"pong"`
	Version string `json:"version"`
	PID     int    `json:"pid"`
	Time    string `json:"time"`
}

func callPing() (pingResp, error) {
	c, err := dial()
	if err != nil {
		return pingResp{}, err
	}
	defer c.Close()
	var raw json.RawMessage
	if err := c.Call("ping", nil, &raw); err != nil {
		return pingResp{}, err
	}
	var r pingResp
	_ = json.Unmarshal(raw, &r)
	return r, nil
}

func callProjectList() ([]methods.ProjectSummary, error) {
	c, err := dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var resp methods.ProjectListResponse
	if err := c.Call("project.list", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Projects, nil
}

func callRescan(idOrPath string) error {
	c, err := dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var resp methods.ProjectRescanResponse
	return c.Call("project.rescan", methods.ProjectRescanRequest{IDOrPath: idOrPath}, &resp)
}

// ---------- macOS helpers ----------

func openInFinder(path string) error { return exec.Command("open", path).Start() }
func openFile(path string) error     { return exec.Command("open", path).Start() }

func pbcopy(text string) error {
	cmd := exec.Command("pbcopy")
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	_, _ = in.Write([]byte(text))
	_ = in.Close()
	return cmd.Wait()
}

// Suppress the "context is imported and not used" warning if we shrink the
// build later. Kept for future SSE subscription work.
var _ = context.Background
