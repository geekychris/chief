// chief is the command-line client for chiefd. All state lives in chiefd;
// this binary is a thin wrapper around the unix-socket RPC.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/geekychris/chief/internal/ipc"
	"github.com/geekychris/chief/internal/methods"
	"github.com/spf13/cobra"
)

// Version is stamped at build time via -ldflags. Defaults to "dev".
var Version = "dev"

func main() {
	root := &cobra.Command{
		Use:           "chief",
		Short:         "Chief — multi-project Claude Code orchestrator",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		pingCmd(),
		versionCmd(),
		projectCmd(),
		backlogCmd(),
		taskCmd(),
		flagCmd(),
		inboxCmd(),
		answerCmd(),
		nextCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "chief:", err)
		os.Exit(1)
	}
}

// ---------- ping / version ----------

func pingCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "ping",
		Short: "Ping the chiefd daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var raw json.RawMessage
			if err := c.Call("ping", nil, &raw); err != nil {
				return err
			}
			if jsonOut {
				fmt.Println(string(raw))
				return nil
			}
			var r struct {
				Version string `json:"version"`
				PID     int    `json:"pid"`
				Time    string `json:"time"`
			}
			if err := json.Unmarshal(raw, &r); err != nil {
				return err
			}
			fmt.Printf("pong (chiefd version=%s pid=%d time=%s)\n", r.Version, r.PID, r.Time)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON result")
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print chief CLI version",
		Run:   func(cmd *cobra.Command, args []string) { fmt.Println("chief", Version) },
	}
}

// ---------- project ----------

func projectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage registered projects",
	}
	cmd.AddCommand(projectAddCmd(), projectListCmd(), projectRemoveCmd(), projectRescanCmd())
	return cmd
}

func projectAddCmd() *cobra.Command {
	var name, spawnMode string
	cmd := &cobra.Command{
		Use:   "add [path]",
		Short: "Register a project directory. Defaults to cwd if path is omitted.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolvePath(args)
			if err != nil {
				return err
			}
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.ProjectAddResponse
			if err := c.Call("project.add", methods.ProjectAddRequest{
				Path: path, Name: name, SpawnMode: spawnMode,
			}, &resp); err != nil {
				return err
			}
			fmt.Printf("registered %s (%s) at %s — imported %d task(s)\n",
				resp.Project.Name, resp.Project.ID, resp.Project.Path, resp.TasksImported)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "override project name (default: dir basename)")
	cmd.Flags().StringVar(&spawnMode, "spawn-mode", "", "attach|headless|spawn-interactive (default: attach)")
	return cmd
}

func projectListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.ProjectListResponse
			if err := c.Call("project.list", nil, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			if len(resp.Projects) == 0 {
				fmt.Println("no projects registered. try: chief project add <path>")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tID\tSTATE\tPENDING\tDONE\tDEFERRED\tMODE\tPATH")
			for _, p := range resp.Projects {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\n",
					p.Name, p.ID, p.State, p.PendingTasks, p.DoneTasks, p.DeferredTasks, p.SpawnMode, p.Path)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON result")
	return cmd
}

func projectRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id-or-name-or-path>",
		Short: "Remove a project. Leaves .chief/ on disk.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.ProjectRemoveResponse
			if err := c.Call("project.remove", methods.ProjectRemoveRequest{IDOrPath: args[0]}, &resp); err != nil {
				return err
			}
			if resp.Removed {
				fmt.Println("removed")
			} else {
				fmt.Println("not found (no-op)")
			}
			return nil
		},
	}
}

func projectRescanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rescan <id-or-name-or-path>",
		Short: "Force a re-parse of a project's markdown files.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.ProjectRescanResponse
			if err := c.Call("project.rescan", methods.ProjectRescanRequest{IDOrPath: args[0]}, &resp); err != nil {
				return err
			}
			fmt.Printf("rescanned — %d task(s)\n", resp.Tasks)
			return nil
		},
	}
}

// ---------- backlog ----------

func backlogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backlog",
		Short: "Query the merged backlog across projects",
	}
	cmd.AddCommand(backlogListCmd())
	return cmd
}

func backlogListCmd() *cobra.Command {
	var projectSel, status string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tasks. Merges across projects when --project is not set.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.BacklogListResponse
			if err := c.Call("backlog.list", methods.BacklogListRequest{ProjectID: projectSel, Status: status}, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			if len(resp.Tasks) == 0 {
				fmt.Println("no tasks")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "STATUS\tID\tPRIO\tPROJECT\tCATEGORY\tTITLE")
			for _, r := range resp.Tasks {
				glyph := statusGlyph(string(r.Status))
				title := r.Title
				if len(title) > 60 {
					title = title[:57] + "..."
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n",
					glyph, r.ID, r.Priority, r.ProjectName, r.Category, title)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&projectSel, "project", "", "filter to one project (id|name|path)")
	cmd.Flags().StringVar(&status, "status", "", "filter by status: pending|active|blocked|deferred|done")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON result")
	return cmd
}

// ---------- task ----------

func taskCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "task", Short: "Inspect individual tasks"}
	cmd.AddCommand(taskShowCmd())
	return cmd
}

func taskShowCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show full task detail",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.TaskShowResponse
			if err := c.Call("task.show", methods.TaskShowRequest{ID: args[0]}, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			t := resp.Task
			fmt.Printf("Task %s  [%s]\n", t.ID, t.Status)
			fmt.Printf("  Project:  %s (%s)\n", resp.ProjectName, t.ProjectID)
			fmt.Printf("  Title:    %s\n", t.Title)
			if t.Category != "" {
				fmt.Printf("  Category: %s\n", t.Category)
			}
			fmt.Printf("  Priority: %d\n", t.Priority)
			if len(t.RequiredResources) > 0 {
				fmt.Printf("  Resources: %s\n", strings.Join(t.RequiredResources, ", "))
			}
			if t.Due != nil {
				fmt.Printf("  Due:      %s\n", *t.Due)
			}
			fmt.Printf("  Source:   %s (line-hash %s)\n", t.SourceFile, t.SourceLineHash)
			fmt.Printf("  Created:  %s\n", t.CreatedAt.Format(time.RFC3339))
			if t.CompletedAt != nil {
				fmt.Printf("  Done:     %s\n", t.CompletedAt.Format(time.RFC3339))
			}
			if t.Body != "" {
				fmt.Println("  Body:")
				for _, line := range strings.Split(t.Body, "\n") {
					fmt.Println("    " + line)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON result")
	return cmd
}

// ---------- flag / inbox / answer / next ----------
//
// The attention pipeline. `chief flag` is what a Claude session (or a human)
// invokes to raise a question; `chief inbox` and `chief answer` are how the
// user reads and resolves them; `chief next-*` handles the completion-follow-up
// notifications chiefd raises when tasks transition to done.
//
// MCP wrappers around these RPCs are deferred to M2 — for now, sessions call
// them via Bash. The IPC contract is the source of truth.

func flagCmd() *cobra.Command {
	var project, session, urgency, kind, suggested string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "flag <question>",
		Short: "Raise an attention flag for the human. Optionally scoped to a project.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			question := strings.Join(args, " ")
			if project == "" {
				// Fall back to cwd. If cwd isn't a registered project, chiefd
				// will surface a not-found error the user can act on.
				p, err := os.Getwd()
				if err != nil {
					return err
				}
				project = p
			}
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.FlagRaiseResponse
			if err := c.Call("flag.raise", methods.FlagRaiseRequest{
				ProjectID:   project,
				SessionID:   session,
				Kind:        kind,
				Urgency:     urgency,
				Question:    question,
				SuggestedID: suggested,
			}, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			hint := ""
			switch {
			case resp.SnoozedProject:
				hint = " (snoozed — no notification)"
			case resp.RateLimited:
				hint = " (rate-limited — no notification)"
			case resp.Coalesced:
				hint = " (coalesced into existing flag)"
			case resp.NotifiedMacOS:
				hint = " (macOS notification sent)"
			}
			fmt.Printf("flag %s raised for %s%s\n", resp.Flag.ID, resp.Flag.ProjectName, hint)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project id/name/path (default: cwd)")
	cmd.Flags().StringVar(&session, "session", "", "session id (optional)")
	cmd.Flags().StringVar(&urgency, "urgency", "attention", "info|attention|urgent")
	cmd.Flags().StringVar(&kind, "kind", "question", "question|next_task")
	cmd.Flags().StringVar(&suggested, "suggested-id", "", "for kind=next_task: task id being suggested")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON result")
	return cmd
}

func inboxCmd() *cobra.Command {
	var project string
	var all, jsonOut bool
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "List unresolved attention flags (default: all projects, open only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.FlagListResponse
			if err := c.Call("flag.list", methods.FlagListRequest{
				ProjectID: project, OpenOnly: !all,
			}, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			if len(resp.Flags) == 0 {
				fmt.Println("no attention flags")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tPROJECT\tURGENCY\tKIND\tAGE\tSTATE\tQUESTION")
			now := time.Now()
			for _, f := range resp.Flags {
				age := now.Sub(f.CreatedAt).Round(time.Second).String()
				state := "open"
				if f.AckAt != nil {
					state = string(f.Resolution)
					if state == "" {
						state = "ack"
					}
				}
				q := f.Question
				if len(q) > 60 {
					q = q[:57] + "..."
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					f.ID, f.ProjectName, f.Urgency, f.Kind, age, state, q)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "filter to one project (id/name/path)")
	cmd.Flags().BoolVar(&all, "all", false, "include resolved flags")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON result")
	return cmd
}

func answerCmd() *cobra.Command {
	var resolution string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "answer <flag-id> <reply text...>",
		Short: "Answer an attention flag (marks it resolved).",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			flagID := args[0]
			reply := strings.Join(args[1:], " ")
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.FlagAnswerResponse
			if err := c.Call("flag.answer", methods.FlagAnswerRequest{
				FlagID: flagID, Reply: reply, Resolution: resolution,
			}, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			fmt.Printf("flag %s answered (%s)\n", resp.Flag.ID, resp.Flag.Resolution)
			return nil
		},
	}
	cmd.Flags().StringVar(&resolution, "resolution", "answered", "answered|approved|skipped|snoozed|dismissed")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON result")
	return cmd
}

func nextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "next",
		Short: "Act on a chief-issued next-task suggestion (approve/skip/snooze).",
	}
	cmd.AddCommand(nextApproveCmd(), nextSkipCmd(), nextSnoozeCmd())
	return cmd
}

func nextApproveCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "approve <flag-id>",
		Short: "Approve the suggested next task — pokes the project's Claude session.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.NextApproveResponse
			if err := c.Call("next.approve", methods.NextApproveRequest{FlagID: args[0]}, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			fmt.Printf("approved — sent task to surface %s\n", resp.SurfaceRef)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON result")
	return cmd
}

func nextSkipCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "skip <flag-id>",
		Short: "Skip the suggested next task (marks the flag resolved, leaves the task pending).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.NextSkipResponse
			if err := c.Call("next.skip", methods.NextSkipRequest{FlagID: args[0], Reason: reason}, &resp); err != nil {
				return err
			}
			fmt.Printf("skipped flag %s\n", resp.Flag.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "reason for skipping (recorded in ack_reply)")
	return cmd
}

func nextSnoozeCmd() *cobra.Command {
	var flagID, project string
	var minutes int
	cmd := &cobra.Command{
		Use:   "snooze",
		Short: "Suppress next-task notifications for a project. Requires --minutes plus --flag or --project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if minutes <= 0 {
				return errors.New("--minutes > 0 required")
			}
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.NextSnoozeResponse
			if err := c.Call("next.snooze", methods.NextSnoozeRequest{
				FlagID: flagID, Project: project, Minutes: minutes,
			}, &resp); err != nil {
				return err
			}
			fmt.Printf("snoozed project %s until %s\n", resp.ProjectID, resp.UntilRFC3339)
			return nil
		},
	}
	cmd.Flags().StringVar(&flagID, "flag", "", "flag id to snooze (also acks the flag)")
	cmd.Flags().StringVar(&project, "project", "", "project id/name/path to snooze")
	cmd.Flags().IntVar(&minutes, "minutes", 0, "duration in minutes")
	return cmd
}

// ---------- helpers ----------

func statusGlyph(s string) string {
	switch s {
	case "pending":
		return "[ ]"
	case "active":
		return "[*]"
	case "blocked":
		return "[⚠]"
	case "deferred":
		return "[~]"
	case "done":
		return "[x]"
	case "dropped":
		return "[!]"
	default:
		return "[?]"
	}
}

func resolvePath(args []string) (string, error) {
	if len(args) == 0 {
		return os.Getwd()
	}
	p, err := filepath.Abs(args[0])
	if err != nil {
		return "", err
	}
	return p, nil
}

func jsonPrint(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// dial opens a connection to chiefd's unix socket.
func dial() (*ipc.Client, error) {
	sockPath, err := ipc.SocketPath()
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		if _, statErr := os.Stat(sockPath); os.IsNotExist(statErr) {
			return nil, fmt.Errorf("chiefd is not running (socket missing at %s). Start it with: chiefd &  # or launchctl load ~/Library/LaunchAgents/com.chris.chiefd.plist", sockPath)
		}
		return nil, fmt.Errorf("dial %s: %w", sockPath, err)
	}
	return ipc.NewClient(conn), nil
}
