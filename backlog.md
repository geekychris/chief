## Backlog
- [ ] {id:1c88} For completed work I want the timestamp when it was reported
- [ ] {id:f06a} add support for code graph search
  ensure that we can install https://github.com/geekychris/code_graph_search (may need to modify that project).  Should support a singular UI surface without a web browser like the go/wails combo).  That code search should be something you can launch on a project.  This should include setting up codesearch to index the project.  Make sure the code search is buildable and installable and you can install from chief (this is a recurring theme for all other tools we ant to integrate).  Make sure changes to code graph search are pushed back to the project.  Here is where the code search is https://github.com/geekychris/code_graph_search

## Attention & notifications

## Operations logging & analytics
- [ ] {id:6db5} Analytics dashboard in the Wails UI [priority:med]
  Per-project charts: task velocity (completed/week), avg time-to-complete, defer rate, top-flagged categories. Global: cross-project time distribution, wake-up-poke frequency, resource contention timeline, notification volume by urgency. Reuse chart patterns from claude-session-analyzer where sensible.
- [ ] {id:531e} Surface task-duration + revive-count outliers in the UI [priority:med]
  A task claimed 3+ times without completion is probably stuck — flag with a "needs human review" badge and suggest defer or split. Same treatment for tasks whose active duration crosses a per-project SLA (default 4h).

## Messaging integrations
- [ ] {id:588b} Telegram backend as first implementation of the plugin arch [priority:high]
  Long-poll bot; inbound commands: `/status`, `/list <project>`, `/next <project>`, `/defer <task_id>`, `/answer <flag_id> <text>`, `/add <project> <title>`. Outbound: attention notifications with inline buttons (Approve/Skip/Snooze) that round-trip through `chief_reply_to_flag`. Bot token from config; ACL by allowed `chat_id` list.
- [ ] {id:1834} Slack backend
  Same pattern as Telegram; supports thread-per-project so each project's chatter stays organized.

## Multi-project coordination
- [ ] {id:e38e} Task splitting proposal flow
  New MCP tool `chief_propose_split(task_id, subtasks[])`. Claude can propose that a task is too large; chief presents the proposal for human ack; on approve, replaces the original checkbox with the children (preserving history in `dropped.md`).
- [ ] {id:ce12} Per-project token / cost tracking [priority:med]
  Ingest usage from Claude's stream-json (headless mode) or shell out to `ccusage`. Show $/tokens per project and per task. Alert on budget overage. Feeds into DND ("stop pokes once daily budget hit").
- [ ] {id:218b} Daily / weekly digest notification
  Rollup: N tasks done, M flagged, top time-sinks, projects with no activity, upcoming due dates. Send via all configured messaging backends. Schedule via `launchd`.

## Agentic ergonomics
- [ ] {id:04b7} Voice / phone quick-add via Telegram
  Send a voice memo or text to the Telegram bot → Whisper transcription → new backlog item in the currently-focused project (or explicit `--project`). Extends the Telegram backend.
- [ ] {id:e98f} Auto-branch-per-task
  When a session claims a task, chief runs `git switch -c chief/<task-id>-<slug>` in the project's cwd (guarded: skip if repo dirty). On `chief_complete_task`, prompt in the UI: merge / open PR / stay on branch.
- [ ] {id:6fff} PR gating: don't mark task done until an associated PR is opened
  `chief_complete_task` warns (or blocks, per config) if no open PR references the task id. Uses `gh` CLI. Configurable per project.
- [ ] {id:249e} Constitution linter
  Periodic sweep + post-turn hook: does the session's activity match `constitution.md`? Flag obvious divergences (e.g., constitution forbids network calls but Claude ran `curl`). Surface in the answer inbox as `info` urgency.
- [ ] {id:d00a} Sync between machines
  Git- or rsync-based sync of the markdown files + a subset of `chief.db`. Conflict resolution on task state (done-vs-done → done; deferred-vs-done → done wins; both active → last-write-wins with warning). Keeps desktop and laptop in step.

## Chief-internal
