## Backlog

## One-shots (proposed via 40fd)

## Proactive & scheduled (proposed via 40fd)
- [ ] {id:1b72} Kill idle Claude sessions — for cmux surfaces with a Claude session_id and no output/heartbeat in >N hours (per project.yaml), prompt to close (or auto-close per config). Saves API budget when a session is left running after work stops.
- [ ] {id:d2d9} Stuck-task suggester — periodic sweep for tasks that have been `active` (or in Claude's hands per `task.sent` event) longer than the per-project SLA. Raise attention-urgency flag suggesting `chief task split <id>` or a manual review. Complements 531e outlier CLI.

## Ergonomics + intelligence (proposed via 40fd)
- [ ] {id:c302} Auto-triage inbox — for each new attention flag, shell to Claude with the flag body + recent related flags and get: (a) suggested urgency reclassification, (b) grouping suggestion (link to related flag), (c) 1-line summary for the menu. Store as flag metadata; UI shows the suggestion but doesn't auto-apply. Turns the inbox into a triaged queue.
- [ ] {id:9b5f} Cross-project dedup — pairwise-similarity sweep across all pending backlog titles (Jaccard on token sets, cheap). Surface likely duplicates as info-urgency flags with `chief backlog list --project A id-1  vs  --project B id-2` recipe.
- [ ] {id:8fa3} Task estimator — for each newly-added task (or on demand), shell to Claude with the task body + relevant context (project constitution, category) and get an estimate (S/M/L) + suggested priority. Stored as metadata; visible in `chief backlog list`.
- [ ] {id:e4ef} Rollback safety net — capture backlog.md + completedlog.md content-hash before every chief-driven mutation (task.add, task.update, task.reorder, task.delete, Rescan sweep). `chief undo` restores the last N snapshots per project. Uses the events table as the log.
- [ ] {id:3206} Time tracking per project — cmux + claudetrace already know which session is active; attribute wall-clock time to whichever project owns the session's cwd. Feeds into `chief cost` for a "$/hour spent" view and into project.state ("worked on today").
- [ ] {id:bb54} Model migration helper — after a new Claude model ships, `chief model-migrate` scans open backlog items and asks Claude "given this task + the new model's capabilities, is it now a candidate for X approach or a shorter path?". Add responses as suggestion metadata.
- [ ] {id:a452} Deps graph across projects — parse `.chief/project.yaml` files for a `depends_on:` field (new); render a graph in the analytics dashboard showing which projects block which. Feeds a "release order" computation.
- [ ] {id:8e23} Project bookmarks — hotkey (⌘1..⌘9) in Chief.app to jump between pinned projects. Also surface via `chief goto 1..9` CLI. Matches the "many projects, one focused at a time" workflow.

## Attention & notifications

## Operations logging & analytics

## Messaging integrations

## Multi-project coordination

## Agentic ergonomics

## Chief-internal
