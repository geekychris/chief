// Package inbound parses text commands received from messaging
// IncomingBackends (Telegram, later Slack). Keeps command handling
// out of the transport-specific code so a Slack /slash-command and
// a Telegram bot message go through the same dispatch.
//
// Supported commands (v1):
//
//	/add <project> <title...>       — append a task
//	/list [<project>]               — list open tasks
//	/status                         — global stats snapshot
//	/inbox                          — list open attention flags
//	/answer <flag-id> <reply...>    — resolve a flag
//	/next [<project>]               — show the next-up task
//
// Voice memos ("[voice:<file_id>]") are surfaced as unrecognised
// commands with a friendly hint; Whisper transcription lands as a
// follow-up (see 04b7).
package inbound

import (
	"context"
	"fmt"
	"strings"

	"github.com/geekychris/chief/internal/attention"
	"github.com/geekychris/chief/internal/messaging"
	"github.com/geekychris/chief/internal/project"
	"github.com/geekychris/chief/internal/store"
)

// Router dispatches parsed commands against chiefd's core services.
// Return value from Handle is the text to send back to the user via
// the same backend the message arrived on; empty string = no reply.
type Router struct {
	Store    *store.Store
	Projects *project.Manager
	Att      *attention.Manager
	// SendBackend is what we use to reply. Kept as a func so the
	// caller wires the right per-message backend (each IncomingBackend
	// knows how to Send).
	SendBackend messaging.Backend
}

// Handle parses a single inbound message and executes the matching
// command. Unknown commands produce a friendly help hint.
func (r *Router) Handle(ctx context.Context, m messaging.InboundMessage) error {
	if !strings.HasPrefix(m.Text, "/") {
		return r.reply(ctx, m, "hint: commands start with `/`. Try /help.")
	}
	if strings.HasPrefix(m.Text, "[voice:") {
		return r.reply(ctx, m, "voice memo received — transcription isn't wired yet (04b7 voice follow-up).")
	}
	parts := strings.Fields(m.Text)
	if len(parts) == 0 {
		return nil
	}
	cmd := parts[0]
	args := parts[1:]

	switch cmd {
	case "/help", "/start":
		return r.reply(ctx, m, helpText)
	case "/add":
		if len(args) < 2 {
			return r.reply(ctx, m, "usage: /add <project> <title...>")
		}
		return r.handleAdd(ctx, m, args[0], strings.Join(args[1:], " "))
	case "/list":
		proj := ""
		if len(args) > 0 {
			proj = args[0]
		}
		return r.handleList(ctx, m, proj)
	case "/status":
		return r.handleStatus(ctx, m)
	case "/inbox":
		return r.handleInbox(ctx, m)
	case "/answer":
		if len(args) < 2 {
			return r.reply(ctx, m, "usage: /answer <flag-id> <reply...>")
		}
		return r.handleAnswer(ctx, m, args[0], strings.Join(args[1:], " "))
	case "/next":
		proj := ""
		if len(args) > 0 {
			proj = args[0]
		}
		return r.handleNext(ctx, m, proj)
	default:
		return r.reply(ctx, m, "unknown command "+cmd+". Try /help.")
	}
}

const helpText = `Chief inbound commands:
/add <project> <title>   — new task
/list [project]          — pending tasks
/status                  — global rollup
/inbox                   — open attention flags
/answer <flag-id> <text> — resolve a flag
/next [project]          — top of the queue
/help                    — this message`

func (r *Router) handleAdd(ctx context.Context, m messaging.InboundMessage, projectSel, title string) error {
	p, err := r.Store.GetProject(ctx, projectSel)
	if err != nil {
		return r.reply(ctx, m, fmt.Sprintf("project %q: %v", projectSel, err))
	}
	t, err := r.Projects.AddTask(ctx, p.ID, project.AddTaskOpts{Title: title})
	if err != nil {
		return r.reply(ctx, m, fmt.Sprintf("add failed: %v", err))
	}
	return r.reply(ctx, m, fmt.Sprintf("added %s in %s: %s", t.ID, p.Name, t.Title))
}

func (r *Router) handleList(ctx context.Context, m messaging.InboundMessage, projectSel string) error {
	filter := store.TaskFilter{Status: store.TaskPending}
	if projectSel != "" {
		p, err := r.Store.GetProject(ctx, projectSel)
		if err != nil {
			return r.reply(ctx, m, fmt.Sprintf("project %q: %v", projectSel, err))
		}
		filter.ProjectID = p.ID
	}
	tasks, err := r.Store.ListTasks(ctx, filter)
	if err != nil {
		return r.reply(ctx, m, "list failed: "+err.Error())
	}
	if len(tasks) == 0 {
		return r.reply(ctx, m, "no pending tasks")
	}
	if len(tasks) > 20 {
		tasks = tasks[:20]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "pending (%d):\n", len(tasks))
	for _, t := range tasks {
		fmt.Fprintf(&b, "· [%s] %s\n", t.ID, t.Title)
	}
	return r.reply(ctx, m, strings.TrimRight(b.String(), "\n"))
}

func (r *Router) handleStatus(ctx context.Context, m messaging.InboundMessage) error {
	projs, _ := r.Store.ListProjects(ctx)
	var pending, done int
	for _, p := range projs {
		ts, _ := r.Store.ListTasks(ctx, store.TaskFilter{ProjectID: p.ID})
		for _, t := range ts {
			switch t.Status {
			case store.TaskPending:
				pending++
			case store.TaskDone:
				done++
			}
		}
	}
	flags, _ := r.Store.CountOpenFlags(ctx, "")
	return r.reply(ctx, m, fmt.Sprintf(
		"Chief · %d project(s) · %d pending · %d done · %d open flag(s)",
		len(projs), pending, done, flags,
	))
}

func (r *Router) handleInbox(ctx context.Context, m messaging.InboundMessage) error {
	flags, err := r.Store.ListFlags(ctx, store.FlagFilter{OpenOnly: true, Limit: 10})
	if err != nil {
		return r.reply(ctx, m, "inbox failed: "+err.Error())
	}
	if len(flags) == 0 {
		return r.reply(ctx, m, "inbox empty")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "open flags (%d):\n", len(flags))
	for _, f := range flags {
		clip := f.Question
		if len(clip) > 80 {
			clip = clip[:77] + "…"
		}
		fmt.Fprintf(&b, "· [%s | %s] %s\n", f.ID, f.Urgency, clip)
	}
	return r.reply(ctx, m, strings.TrimRight(b.String(), "\n"))
}

func (r *Router) handleAnswer(ctx context.Context, m messaging.InboundMessage, flagID, reply string) error {
	f, err := r.Att.Answer(ctx, flagID, reply)
	if err != nil {
		return r.reply(ctx, m, fmt.Sprintf("answer failed: %v", err))
	}
	return r.reply(ctx, m, fmt.Sprintf("answered %s (%s)", f.ID, f.Resolution))
}

func (r *Router) handleNext(ctx context.Context, m messaging.InboundMessage, projectSel string) error {
	if projectSel != "" {
		p, err := r.Store.GetProject(ctx, projectSel)
		if err != nil {
			return r.reply(ctx, m, fmt.Sprintf("project %q: %v", projectSel, err))
		}
		tasks, _ := r.Store.ListTasks(ctx, store.TaskFilter{
			ProjectID: p.ID, Status: store.TaskPending,
		})
		if len(tasks) == 0 {
			return r.reply(ctx, m, "no pending tasks in "+p.Name)
		}
		t := tasks[0]
		return r.reply(ctx, m, fmt.Sprintf("[%s | prio %d] %s", t.ID, t.Priority, t.Title))
	}
	tasks, _ := r.Store.NextPending(ctx, 1)
	if len(tasks) == 0 {
		return r.reply(ctx, m, "no pending tasks anywhere")
	}
	t := tasks[0]
	return r.reply(ctx, m, fmt.Sprintf("next up: [%s | prio %d] %s", t.ID, t.Priority, t.Title))
}

func (r *Router) reply(ctx context.Context, in messaging.InboundMessage, text string) error {
	if r.SendBackend == nil {
		return nil
	}
	return r.SendBackend.Send(ctx, messaging.Message{
		Urgency: messaging.UrgencyInfo,
		Body:    text,
	})
}
