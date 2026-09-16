// chief is the command-line client for chiefd. All state lives in chiefd;
// this binary is a thin wrapper around the unix-socket RPC.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
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
		statsCmd(),
		searchCmd(),
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
	cmd.AddCommand(backlogListCmd(), backlogNextCmd())
	return cmd
}

func backlogNextCmd() *cobra.Command {
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "next",
		Short: "Show the top pending tasks ranked across all projects.",
		Long: `Rank across ALL projects by (priority DESC, source_line ASC).
Answers "which project should I focus on right now?".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.BacklogNextResponse
			if err := c.Call("backlog.next", methods.BacklogNextRequest{Limit: limit}, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			if len(resp.Tasks) == 0 {
				fmt.Println("no pending tasks anywhere")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "RANK\tID\tPRIO\tPROJECT\tCATEGORY\tTITLE")
			for i, r := range resp.Tasks {
				title := r.Title
				if len(title) > 60 {
					title = title[:57] + "..."
				}
				fmt.Fprintf(tw, "%d\t%s\t%d\t%s\t%s\t%s\n",
					i+1, r.ID, r.Priority, r.ProjectName, r.Category, title)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 10, "how many tasks to return")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON result")
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
	cmd.AddCommand(taskShowCmd(), taskNewCmd(), taskImportCmd())
	return cmd
}

// backlogTemplates are the built-in scaffolds for `chief task new
// --template`. Kept as a plain map so adding a new one is a single-line
// change; no external files, no wiring. Each template's Body becomes the
// checkbox body (indented sub-bullets) in backlog.md — the shape the
// parser already understands.
var backlogTemplates = map[string]struct {
	Category string
	Body     string
}{
	"feature": {
		Category: "Features",
		Body: `**Goal:** why does this exist and who benefits.

**Acceptance criteria:**
- [ ]
- [ ]
- [ ]

**Test plan:** what proves this works.

**Non-goals:** what this deliberately does NOT do.

**Rollout:** flag, gate, or ship direct.`,
	},
	"bug": {
		Category: "Bugs",
		Body: `**Symptom:** what the user sees.

**Repro steps:**
1.
2.
3.

**Expected:**
**Actual:**

**Root cause hypothesis:**

**Fix approach:**

**Regression test:** how we prove it doesn't come back.`,
	},
	"refactor": {
		Category: "Refactors",
		Body: `**Motivation:** why now.

**Current shape:** what's in the code today.

**Target shape:** what it should look like after.

**Migration steps:**
1.
2.
3.

**Rollback plan:** how to revert if something goes wrong.

**Risk:** what could break.`,
	},
	"chore": {
		Category: "Chores",
		Body: `**What:** the specific change.

**Why:** the trigger (dep bump, deprecation, cleanup, etc.).

**Verify:** how we know it's done.`,
	},
}

func taskNewCmd() *cobra.Command {
	var project, template, category, body string
	var priority int
	var jsonOut, dryRun bool
	cmd := &cobra.Command{
		Use:   "new <title...>",
		Short: "Create a new task, optionally from a template scaffold.",
		Long: `Append a new task to a project's backlog.md.

With --template <name>, prefills the body with a scaffold (feature|bug|refactor|chore).
Without --template, creates a plain task with an empty body (or --body TEXT).

Examples:
  chief task new "add auth callback" --template feature --project chief
  chief task new "fix login redirect" -t bug -p 5
  chief task new "bump go modules" -t chore --dry-run`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			title := strings.Join(args, " ")
			if project == "" {
				p, err := os.Getwd()
				if err != nil {
					return err
				}
				project = p
			}
			// Resolve template scaffold.
			if template != "" {
				t, ok := backlogTemplates[template]
				if !ok {
					names := make([]string, 0, len(backlogTemplates))
					for k := range backlogTemplates {
						names = append(names, k)
					}
					return fmt.Errorf("unknown template %q — available: %s", template, strings.Join(names, ", "))
				}
				if category == "" {
					category = t.Category
				}
				if body == "" {
					body = t.Body
				}
			}
			if dryRun {
				fmt.Printf("[dry-run] would add to %s:\n", project)
				fmt.Printf("  Title:    %s\n", title)
				fmt.Printf("  Category: %s\n", category)
				fmt.Printf("  Priority: %d\n", priority)
				if body != "" {
					fmt.Println("  Body:")
					for _, line := range strings.Split(body, "\n") {
						fmt.Println("    " + line)
					}
				}
				return nil
			}
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.TaskAddResponse
			if err := c.Call("task.add", methods.TaskAddRequest{
				ProjectID: project, Title: title, Body: body,
				Category: category, Priority: priority,
			}, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			fmt.Printf("added task %s in %s\n", resp.Task.ID, resp.Task.ProjectID)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project id/name/path (default: cwd)")
	cmd.Flags().StringVarP(&template, "template", "t", "", "scaffold: feature|bug|refactor|chore")
	cmd.Flags().StringVar(&category, "category", "", "backlog.md section header (default: template's category, else 'Backlog')")
	cmd.Flags().StringVar(&body, "body", "", "task body (indented sub-bullets); overrides template body")
	cmd.Flags().IntVarP(&priority, "priority", "p", 0, "priority tag")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON result")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the scaffolded task and exit without writing")
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

// taskImportCmd splits freeform text into structured tasks via a scoped
// `claude -p` call, previews the result, and (on confirm) appends each
// item to the target project's backlog.md via the existing task.add RPC.
//
// The scoped prompt asks Claude to return a JSON array only, so we can
// parse without hand-rolling markdown extraction. Two safety valves:
// --dry-run to see the split without writing, and --yes to skip the
// interactive confirm (for scripting).
func taskImportCmd() *cobra.Command {
	var project, file, claudeBin, categoryOverride string
	var priorityOverride int
	var dryRun, yes, jsonOut bool
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Split freeform text into backlog items via Claude, then append them.",
		Long: `Read freeform text from --file or stdin, shell to Claude to split
it into structured tasks, preview the result, and (on confirm) append
each item to the target project's backlog.md.

Examples:
  chief task import --project chief < ideas.txt
  echo "auth callback, dark mode toggle" | chief task import -p 3
  chief task import -f ideas.md --dry-run
  chief task import -f ideas.md --yes            # scripting

Requires 'claude' (or --claude-bin PATH) on PATH.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Read input.
			var raw []byte
			var err error
			if file != "" {
				raw, err = os.ReadFile(file)
			} else {
				raw, err = readAllStdin()
			}
			if err != nil {
				return fmt.Errorf("read input: %w", err)
			}
			text := strings.TrimSpace(string(raw))
			if text == "" {
				return errors.New("no input text (use --file or pipe via stdin)")
			}

			// Resolve project (default cwd).
			if project == "" {
				p, err := os.Getwd()
				if err != nil {
					return err
				}
				project = p
			}

			// Split via Claude.
			bin := claudeBin
			if bin == "" {
				bin = "claude"
			}
			items, err := splitTextViaClaude(bin, text)
			if err != nil {
				return fmt.Errorf("split via claude: %w", err)
			}
			if len(items) == 0 {
				return errors.New("claude returned zero tasks — try clarifying the input")
			}

			// Apply overrides.
			for i := range items {
				if categoryOverride != "" {
					items[i].Category = categoryOverride
				}
				if priorityOverride != 0 {
					items[i].Priority = priorityOverride
				}
			}

			// Preview.
			if jsonOut {
				return jsonPrint(struct {
					Items []importItem `json:"items"`
				}{items})
			}
			fmt.Printf("would add %d task(s) to %s:\n\n", len(items), project)
			for i, it := range items {
				fmt.Printf("  %d. [%s | prio %d] %s\n", i+1, it.Category, it.Priority, it.Title)
				if it.Body != "" {
					for _, line := range strings.Split(it.Body, "\n") {
						fmt.Println("      " + line)
					}
				}
			}
			fmt.Println()

			if dryRun {
				fmt.Println("(dry-run — nothing written)")
				return nil
			}
			if !yes {
				fmt.Print("Proceed? [y/N] ")
				var ans string
				_, _ = fmt.Scanln(&ans)
				if !strings.EqualFold(strings.TrimSpace(ans), "y") &&
					!strings.EqualFold(strings.TrimSpace(ans), "yes") {
					fmt.Println("aborted.")
					return nil
				}
			}

			// Add each via existing task.add RPC.
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			added := 0
			for _, it := range items {
				var resp methods.TaskAddResponse
				if err := c.Call("task.add", methods.TaskAddRequest{
					ProjectID: project, Title: it.Title, Body: it.Body,
					Category: it.Category, Priority: it.Priority,
				}, &resp); err != nil {
					fmt.Fprintf(os.Stderr, "skip %q: %v\n", it.Title, err)
					continue
				}
				fmt.Printf("added %s: %s\n", resp.Task.ID, resp.Task.Title)
				added++
			}
			fmt.Printf("\ndone — %d/%d task(s) added.\n", added, len(items))
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project id/name/path (default: cwd)")
	cmd.Flags().StringVarP(&file, "file", "f", "", "read text from a file instead of stdin")
	cmd.Flags().StringVar(&claudeBin, "claude-bin", "", "override the claude binary (default: 'claude' on PATH)")
	cmd.Flags().StringVar(&categoryOverride, "category", "", "override the category on every generated task")
	cmd.Flags().IntVarP(&priorityOverride, "priority", "p", 0, "override the priority on every generated task")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the split and exit without writing")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the interactive confirm")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON items and exit (implies dry-run)")
	return cmd
}

// importItem is the JSON shape we ask Claude to emit + parse on our side.
type importItem struct {
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Category string `json:"category,omitempty"`
	Priority int    `json:"priority,omitempty"`
}

// splitTextViaClaude shells to `claude -p '<prompt>'` with a scoped
// system-like prompt that demands JSON-only output. We accept either
// {"items":[...]} or a bare array. The prompt is intentionally strict
// about "no prose, no markdown fences" — Claude will still add fences
// sometimes so we strip the outermost pair defensively before parsing.
func splitTextViaClaude(bin, text string) ([]importItem, error) {
	prompt := "You will split a freeform brain-dump into individual backlog items " +
		"for a task tracker.\n\n" +
		"Rules:\n" +
		"- Output ONLY a single JSON array. No prose, no explanation, no markdown fences.\n" +
		"- Each item must have: title (short imperative, <70 chars), body (detail as " +
		"markdown sub-bullets or paragraphs, may be empty), category (short section name), " +
		"priority (integer 0-9; 5=high, 3=med, 0=low, guess based on urgency wording).\n" +
		"- Preserve the user's original wording where possible.\n" +
		"- Do not invent tasks that aren't in the input.\n\n" +
		"Input text:\n---\n" + text + "\n---\n\n" +
		"Return only the JSON array."

	cmd := exec.Command(bin, "-p", prompt)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%s -p: %w (stderr: %s)", bin, err, string(ee.Stderr))
		}
		return nil, fmt.Errorf("%s -p: %w", bin, err)
	}
	body := strings.TrimSpace(string(out))
	body = stripFences(body)
	// Accept either bare array or {"items":[...]}.
	var arr []importItem
	if err := json.Unmarshal([]byte(body), &arr); err == nil {
		return arr, nil
	}
	var wrapped struct {
		Items []importItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &wrapped); err == nil {
		return wrapped.Items, nil
	}
	return nil, fmt.Errorf("claude returned non-JSON output (first 200 chars): %q", clip(body, 200))
}

// stripFences removes a wrapping ``` or ```json ... ``` if present, so
// we don't need Claude to be perfectly obedient.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop first line (```json or ```) and last line if it's the closing fence.
	lines := strings.SplitN(s, "\n", 2)
	if len(lines) < 2 {
		return s
	}
	rest := lines[1]
	if idx := strings.LastIndex(rest, "```"); idx >= 0 {
		return strings.TrimSpace(rest[:idx])
	}
	return strings.TrimSpace(rest)
}

func readAllStdin() ([]byte, error) {
	// Only read when stdin is piped/redirected — a bare invocation on a
	// tty would hang waiting for EOF.
	if fi, err := os.Stdin.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
		return nil, errors.New("stdin is a terminal; use --file or pipe input")
	}
	return io.ReadAll(os.Stdin)
}

// clip truncates a string for error messages.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func searchCmd() *cobra.Command {
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "search <query...>",
		Short: "Search tasks across ALL projects (case-insensitive, LIKE-based).",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.Join(args, " ")
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.SearchResponse
			if err := c.Call("search", methods.SearchRequest{Query: query, Limit: limit}, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			if len(resp.Tasks) == 0 {
				fmt.Printf("no matches for %q\n", query)
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
	cmd.Flags().IntVar(&limit, "limit", 50, "max results")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON result")
	return cmd
}

// statsCmd renders a per-project + global rollup useful for the daily
// glance ("how much did I get done, what needs attention, which
// project's been quiet"). Also the data source for the future daily
// digest (218b) once messaging backends land.
func statsCmd() *cobra.Command {
	var window int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Cross-project rollup: task counts, open flags, last activity, velocity.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.StatsSummaryResponse
			if err := c.Call("stats.summary", methods.StatsSummaryRequest{WindowDays: window}, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			g := resp.Global
			fmt.Printf("Chief · %d project(s) · %d events total · window %d day(s)\n",
				g.Projects, g.EventsTotal, resp.WindowDays)
			fmt.Printf("  Tasks: %d pending · %d active · %d blocked · %d deferred · %d done\n",
				g.TasksPending, g.TasksActive, g.TasksBlocked, g.TasksDeferred, g.TasksDone)
			fmt.Printf("  Attention: %d open flag(s) · %d completion sweep(s) in window\n\n",
				g.FlagsOpen, g.CompletedWindow)
			if len(resp.Projects) == 0 {
				fmt.Println("no projects registered")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSTATE\tPEND\tACT\tBLK\tDEF\tDONE\tFLAGS\tSWEEPS/W\tLAST ACTIVITY")
			for _, p := range resp.Projects {
				last := "—"
				if p.LastActivity != "" {
					if t, err := time.Parse(time.RFC3339, p.LastActivity); err == nil {
						last = relativeTime(time.Since(t))
					}
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
					p.Name, p.State, p.Pending, p.Active, p.Blocked, p.Deferred, p.Done,
					p.FlagsOpen, p.CompletedWindow, last)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().IntVar(&window, "window", 7, "rolling window in days for velocity numbers")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON result")
	return cmd
}

// relativeTime formats a duration ago as "3h", "2d", "just now".
func relativeTime(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
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
			case resp.InDND:
				hint = " (in DND window — no notification)"
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
