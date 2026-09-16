package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/geekychris/chief/internal/methods"
	"github.com/spf13/cobra"
)

// ---------- undo (e4ef) ----------

func undoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "undo",
		Short: "List / restore backlog snapshots taken before chief mutations.",
	}
	cmd.AddCommand(undoListCmd(), undoRestoreCmd())
	return cmd
}

func undoListCmd() *cobra.Command {
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list <project>",
		Short: "Show recent snapshots for a project (newest first).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.UndoListResponse
			if err := c.Call("undo.list", methods.UndoListRequest{IDOrPath: args[0], Limit: limit}, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			if len(resp.Snapshots) == 0 {
				fmt.Println("(no snapshots yet — mutate a task via chief and re-run)")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tWHEN\tREASON\tBACKLOG_HASH")
			for _, s := range resp.Snapshots {
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n",
					s.ID, s.Ts.Local().Format("2006-01-02 15:04:05"),
					s.Reason, s.BacklogHash[:8])
			}
			return tw.Flush()
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "max snapshots to list")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON")
	return cmd
}

func undoRestoreCmd() *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "restore <snapshot-id>",
		Short: "Restore backlog.md + completedlog.md from a snapshot. Default is dry-run.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var id int64
			if _, err := fmt.Sscanf(args[0], "%d", &id); err != nil {
				return fmt.Errorf("snapshot-id must be numeric")
			}
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.UndoRestoreResponse
			if err := c.Call("undo.restore", methods.UndoRestoreRequest{
				SnapshotID: id, DryRun: !apply,
			}, &resp); err != nil {
				return err
			}
			fmt.Printf("snapshot %d — reason=%s\n", id, resp.Reason)
			fmt.Printf("target: %s\n", resp.ProjectPath)
			fmt.Printf("backlog.md: %d bytes\n", resp.BacklogBytes)
			fmt.Printf("completedlog.md: %d bytes\n", resp.CompletedBytes)
			if resp.Restored {
				fmt.Println("✔ restored (rescanned)")
			} else {
				fmt.Println("(dry run — pass --apply to write the files)")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "actually restore (default is dry-run)")
	return cmd
}

// ---------- deps-graph (a452) ----------

func depsGraphCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "deps-graph",
		Short: "Show inter-project dependencies from .chief/project.yaml `depends_on:` fields.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.DepsGraphResponse
			if err := c.Call("deps.graph", methods.DepsGraphRequest{}, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			// Re-marshal → generic map so we can print the ASCII locally
			// without importing the internal orchestrator type.
			b, _ := json.Marshal(resp.Graph)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			nodes, _ := m["nodes"].([]any)
			fmt.Printf("Projects: %d\n\n", len(nodes))
			nameByID := map[string]string{}
			for _, n := range nodes {
				nn, _ := n.(map[string]any)
				id, _ := nn["id"].(string)
				name, _ := nn["name"].(string)
				nameByID[id] = name
			}
			resolve := func(id string) string {
				if n, ok := nameByID[id]; ok {
					return n
				}
				return id
			}
			for _, n := range nodes {
				nn, _ := n.(map[string]any)
				name, _ := nn["name"].(string)
				deps, _ := nn["depends_on"].([]any)
				blocks, _ := nn["blocks"].([]any)
				if len(deps) == 0 && len(blocks) == 0 {
					fmt.Printf("  %s  (isolated)\n", name)
					continue
				}
				fmt.Printf("  %s\n", name)
				for _, d := range deps {
					fmt.Printf("    depends on → %s\n", resolve(d.(string)))
				}
				for _, d := range blocks {
					fmt.Printf("    blocks     ← %s\n", resolve(d.(string)))
				}
			}
			if order, ok := m["release_order"].([]any); ok && len(order) > 0 {
				fmt.Println("\nRelease order:")
				for i, id := range order {
					fmt.Printf("  %d. %s\n", i+1, resolve(id.(string)))
				}
			}
			if cyc, ok := m["cycles"].([]any); ok && len(cyc) > 0 {
				fmt.Println("\n⚠ Dependency cycles detected — release-order unavailable")
			}
			if unk, ok := m["unknown_refs"].([]any); ok && len(unk) > 0 {
				fmt.Printf("\nUnknown refs in depends_on: ")
				for i, u := range unk {
					if i > 0 {
						fmt.Print(", ")
					}
					fmt.Print(u)
				}
				fmt.Println()
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON")
	return cmd
}

// ---------- triage / estimate (c302 + 8fa3) ----------

func triageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "triage <flag-id>",
		Short: "Shell Claude to enrich a specific attention flag with summary + urgency-reclass suggestion.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.TriageRunResponse
			if err := c.Call("triage.run", methods.TriageRunRequest{FlagID: args[0]}, &resp); err != nil {
				return err
			}
			if resp.Error != "" {
				return fmt.Errorf("triage: %s", resp.Error)
			}
			fmt.Printf("summary:  %s\n", resp.Summary)
			fmt.Printf("urgency:  %s (reason: %s)\n", resp.Urgency, resp.Reason)
			if resp.Related != "" {
				fmt.Printf("related:  {%s}\n", resp.Related)
			}
			return nil
		},
	}
	return cmd
}

func estimateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "estimate <task-id>",
		Short: "Shell Claude to size a task (S/M/L) + suggest priority.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.EstimateRunResponse
			if err := c.Call("estimate.run", methods.EstimateRunRequest{TaskID: args[0]}, &resp); err != nil {
				return err
			}
			if resp.Error != "" {
				return fmt.Errorf("estimate: %s", resp.Error)
			}
			fmt.Printf("size:     %s\n", resp.Size)
			fmt.Printf("priority: %d\n", resp.Priority)
			fmt.Printf("reason:   %s\n", resp.Reason)
			return nil
		},
	}
	return cmd
}

// ---------- time report (3206) ----------

func timeReportCmd() *cobra.Command {
	var days int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "timerep",
		Short: "Show per-project wall-clock hours attributed to Claude sessions.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.TimeReportResponse
			if err := c.Call("time.report", methods.TimeReportRequest{Days: days}, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			if len(resp.Projects) == 0 {
				fmt.Println("(no attributed time yet — TimeTracker samples every 60s while a Claude cmux surface is running)")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "PROJECT\tHOURS\tLAST_ACTIVE\n")
			for _, p := range resp.Projects {
				last := "-"
				if p.LastActive != nil {
					last = humanDur2(time.Since(*p.LastActive)) + " ago"
				}
				fmt.Fprintf(tw, "%s\t%.2f\t%s\n", p.ProjectName, p.Hours, last)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "look-back window in days")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON")
	return cmd
}

// ---------- bookmarks (8e23) ----------

func bookmarkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bookmark",
		Short: "Pin projects to slots 1..9 for `chief goto <N>` + Chief.app ⌘1..⌘9.",
	}
	cmd.AddCommand(bookmarkListCmd(), bookmarkSetCmd(), bookmarkClearCmd())
	return cmd
}

func bookmarkListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show all 9 bookmark slots (empty slots included).",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.BookmarkListResponse
			if err := c.Call("bookmark.list", methods.BookmarkListRequest{}, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			for _, s := range resp.Slots {
				switch {
				case s.Ref == "":
					fmt.Printf("  ⌘%d  (empty)\n", s.Slot)
				case s.ProjectID == "":
					fmt.Printf("  ⌘%d  %s  ← stale (project not registered)\n", s.Slot, s.Ref)
				default:
					fmt.Printf("  ⌘%d  %s  (%s)\n", s.Slot, s.ProjectName, s.ProjectPath)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON")
	return cmd
}

func bookmarkSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <slot 1-9> <project>",
		Short: "Assign a project to a bookmark slot.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var slot int
			if _, err := fmt.Sscanf(args[0], "%d", &slot); err != nil {
				return fmt.Errorf("slot must be 1..9")
			}
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.BookmarkSetResponse
			if err := c.Call("bookmark.set", methods.BookmarkSetRequest{Slot: slot, Ref: args[1]}, &resp); err != nil {
				return err
			}
			fmt.Printf("⌘%d → %s\n", resp.Slot, resp.ProjectName)
			return nil
		},
	}
	return cmd
}

func bookmarkClearCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clear <slot 1-9>",
		Short: "Remove the bookmark from a slot.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var slot int
			if _, err := fmt.Sscanf(args[0], "%d", &slot); err != nil {
				return fmt.Errorf("slot must be 1..9")
			}
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.BookmarkClearResponse
			if err := c.Call("bookmark.clear", methods.BookmarkClearRequest{Slot: slot}, &resp); err != nil {
				return err
			}
			if resp.Cleared {
				fmt.Printf("cleared ⌘%d\n", slot)
			} else {
				fmt.Printf("⌘%d was already empty\n", slot)
			}
			return nil
		},
	}
	return cmd
}

func gotoCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "goto <slot 1-9>",
		Short: "Print details for the bookmarked project (Chief.app also focuses its main window).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var slot int
			if _, err := fmt.Sscanf(args[0], "%d", &slot); err != nil {
				return fmt.Errorf("slot must be 1..9")
			}
			c, err := dial()
			if err != nil {
				return err
			}
			defer c.Close()
			var resp methods.BookmarkGotoResponse
			if err := c.Call("bookmark.goto", methods.BookmarkGotoRequest{Slot: slot}, &resp); err != nil {
				return err
			}
			if jsonOut {
				return jsonPrint(resp)
			}
			fmt.Printf("⌘%d → %s\n", resp.Slot, resp.ProjectName)
			fmt.Printf("  id:   %s\n", resp.ProjectID)
			fmt.Printf("  path: %s\n", resp.ProjectPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON")
	return cmd
}

func humanDur2(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
