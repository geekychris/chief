## Backlog

## Attention & notifications
- [ ] {id:e0f5} Menu-bar counter of pending attention items across all projects
  Extend `chief-menu`'s badge to show total flag count and the top-of-queue project name. Click opens the answer inbox.
- [ ] {id:535c} Per-project + global Do-Not-Disturb schedule [priority:med]
  Config lets me set "no pokes or notifications between 22:00–08:00", globally or per project. Notifications queue silently during DND and drain on lift.

## Operations logging & analytics
- [ ] {id:19f8} Log every daemon operation to the SQLite `events` table with structured JSON payload [priority:high]
  Cover at minimum: task claim/complete/defer, session heartbeat, notification sent, wake-up poke, resource claim/release/renew/expire, MCP tool call (name + latency), fswatch write, backlog parse round-trip. Payload as JSON. Include ts, duration_ms, project_id, session_id, kind, outcome.
- [ ] {id:2546} Retention policy for the events table
  Configurable rolling window (default 90 days). On rotation, archive to gzipped JSONL under `~/Library/Logs/Chief/archive/YYYY-MM.jsonl.gz` before delete. `chief events export --since <t>` CLI to dump for external analysis (feeds later dashboards + weekly digest).
- [ ] {id:6db5} Analytics dashboard in the Wails UI [priority:med]
  Per-project charts: task velocity (completed/week), avg time-to-complete, defer rate, top-flagged categories. Global: cross-project time distribution, wake-up-poke frequency, resource contention timeline, notification volume by urgency. Reuse chart patterns from claude-session-analyzer where sensible.
- [ ] {id:f7de} `chief stats` CLI subcommand for terminal-friendly analytics
  One line per project (velocity, active tasks, last activity, flags-outstanding); `--json` for scripting. Powers the weekly-digest and Telegram `/status` command.
- [ ] {id:531e} Surface task-duration + revive-count outliers in the UI [priority:med]
  A task claimed 3+ times without completion is probably stuck — flag with a "needs human review" badge and suggest defer or split. Same treatment for tasks whose active duration crosses a per-project SLA (default 4h).

## Messaging integrations
- [ ] {id:fe1c} Plugin architecture for messaging backends [priority:high]
  Define `internal/messaging/backend.go` interface: `Send(project, urgency, text, actions[])`, `OnIncoming(fn)`. Backends registered via `config.yaml`. Each runs in its own goroutine and calls back into `chiefd` via unix socket. Ship a docs page for writing a new backend and a fake backend for tests.
- [ ] {id:588b} Telegram backend as first implementation of the plugin arch [priority:high]
  Long-poll bot; inbound commands: `/status`, `/list <project>`, `/next <project>`, `/defer <task_id>`, `/answer <flag_id> <text>`, `/add <project> <title>`. Outbound: attention notifications with inline buttons (Approve/Skip/Snooze) that round-trip through `chief_reply_to_flag`. Bot token from config; ACL by allowed `chat_id` list.
- [ ] {id:5802} ntfy.sh + Pushover backends [priority:med]
  Simpler outbound-only. Map chief's urgency tiers to their native priorities. Nice for phone push without running a bot.
- [ ] {id:1834} Slack backend
  Same pattern as Telegram; supports thread-per-project so each project's chatter stays organized.
- [ ] {id:9496} Message routing rules
  Which urgency → which backend(s). Example: `urgent → telegram+macos; attention → telegram; info → macos-only`. Per-project overrides in `.chief/project.yaml`.

## Multi-project coordination
- [ ] {id:02a6} Unified "next up" queue across all projects [priority:high]
  Rank pending tasks system-wide by (priority DESC, age ASC, resource-availability). CLI: `chief next --any`; UI shows top 10 with a one-click "assign to Project X's session". Solves "which project should I focus on right now?".
- [ ] {id:c09c} Cross-project full-text search across all backlogs [priority:med]
  `chief search "auth"` returns matching tasks with project + status + category. Backed by SQLite FTS5.
- [ ] {id:ad14} Task dependencies (blocks / blocked-by) [priority:med]
  `[blocks:id:a3f1]` and `[blocked-by:id:b7c2]` tags in `backlog.md`. Parser writes them to a `task_edges` table; scheduler skips blocked tasks when choosing what to hand out; UI shows a dependency graph per project.
- [ ] {id:e38e} Task splitting proposal flow
  New MCP tool `chief_propose_split(task_id, subtasks[])`. Claude can propose that a task is too large; chief presents the proposal for human ack; on approve, replaces the original checkbox with the children (preserving history in `dropped.md`).
- [ ] {id:ce12} Per-project token / cost tracking [priority:med]
  Ingest usage from Claude's stream-json (headless mode) or shell out to `ccusage`. Show $/tokens per project and per task. Alert on budget overage. Feeds into DND ("stop pokes once daily budget hit").
- [ ] {id:218b} Daily / weekly digest notification
  Rollup: N tasks done, M flagged, top time-sinks, projects with no activity, upcoming due dates. Send via all configured messaging backends. Schedule via `launchd`.
- [ ] {id:1241} Idle-session detection + auto-recovery [priority:med]
  Session in `working` state with no heartbeat for 30min → mark stale. Per `.chief/project.yaml` either notify me or restart (`spawn_mode`-dependent). Include the last-known active task in the notification for context.

## Agentic ergonomics
- [ ] {id:2af2} Backlog templates (`chief task new --template feature|bug|refactor`)
  Scaffolds the sub-bullet body with template-specific sections (acceptance criteria, test plan, rollback plan, non-goals).
- [ ] {id:04b7} Voice / phone quick-add via Telegram
  Send a voice memo or text to the Telegram bot → Whisper transcription → new backlog item in the currently-focused project (or explicit `--project`). Extends the Telegram backend.
- [ ] {id:e98f} Auto-branch-per-task
  When a session claims a task, chief runs `git switch -c chief/<task-id>-<slug>` in the project's cwd (guarded: skip if repo dirty). On `chief_complete_task`, prompt in the UI: merge / open PR / stay on branch.
- [ ] {id:6fff} PR gating: don't mark task done until an associated PR is opened
  `chief_complete_task` warns (or blocks, per config) if no open PR references the task id. Uses `gh` CLI. Configurable per project.
- [ ] {id:e155} Bulk task import — "paste a bunch of ideas, chief splits"
  `chief task import < ideas.txt` uses a scoped Claude call to split freeform text into individual backlog items with suggested categories and priorities. Preview before committing to `backlog.md`.
- [ ] {id:249e} Constitution linter
  Periodic sweep + post-turn hook: does the session's activity match `constitution.md`? Flag obvious divergences (e.g., constitution forbids network calls but Claude ran `curl`). Surface in the answer inbox as `info` urgency.
- [ ] {id:d00a} Sync between machines
  Git- or rsync-based sync of the markdown files + a subset of `chief.db`. Conflict resolution on task state (done-vs-done → done; deferred-vs-done → done wins; both active → last-write-wins with warning). Keeps desktop and laptop in step.

## Chief-internal
