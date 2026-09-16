## Backlog

## One-shots (proposed via 40fd)
- [ ] {id:850d} `chief run ensure-clean` — walk every registered project, report uncommitted changes / unpushed commits / stale branches; optional --autocommit "chief autocommit" + push. One command that answers "what's not yet on the remote across everything I own?".
- [ ] {id:86cc} `chief run tests` — detect language per project (go.mod → go test, package.json → npm test, Cargo.toml → cargo test, pyproject.toml → pytest) and run in parallel with per-project timeout. Report pass/fail summary; on failure, raise attention-urgency flag with the stdout tail.
- [ ] {id:b153} `chief run pr-status` — for every task marked done with an autobranch, check `gh pr status`; if no PR exists, offer to `gh pr create` with title `chief/<id>: <task title>` and body prefilled from the task body. Also list stalled open PRs (>N days, no update).
- [ ] {id:9760} `chief run deps-fresh` — per project: `go mod tidy && go list -u -m all`, `npm outdated`, `cargo outdated`. Report count of stale deps + suggest scheduling an update-deps task.
- [ ] {id:5756} `chief run review-diff` — for projects with uncommitted changes, shell to Claude with the diff and get a 2-3 sentence summary + risk callouts. Emit as info-urgency flags. Complements ensure-clean.
- [ ] {id:a0e8} `chief run backup` — snapshot chief.db + every project's backlog/completedlog/dropped/constitution/PROJECT to a timestamped tarball under ~/Library/Backups/Chief/. Restore command too.
- [ ] {id:8e4c} `chief run refresh <project>` — one-shot "reset the workspace": git pull, rescan backlog, reload constitution, poke the bound cmux Claude with "please pull the latest changes and re-orient".
- [ ] {id:2b3f} `chief run focus <project> [--mins N]` — mute notifications for every OTHER project for N minutes, snooze all non-flagged tasks elsewhere, promote this project in the menu-bar counter. Anti-context-switch mode.

## Proactive & scheduled (proposed via 40fd)
- [ ] {id:12da} Morning briefing — daily launchd trigger at HH:MM local: "since yesterday: X completions, Y flags raised, Z projects with new activity. Top 3 tasks to focus on: …". Delivered via messaging.Router (ntfy/Telegram/etc.). More opinionated than the existing daily digest.
- [ ] {id:8db7} Watch mode — for a project, tail its Claude session jsonl in real time; surface concerning tool_use events (rm -rf, curl to non-allowlisted hosts, edits outside the project dir, force pushes). Raises urgent-urgency flag on hit. Piggybacks on the constitution linter's forbidden-patterns list.
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
