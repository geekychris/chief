## Backlog
- [ ] {id:f06a} add support for code graph search
  ensure that we can install https://github.com/geekychris/code_graph_search (may need to modify that project).  Should support a singular UI surface without a web browser like the go/wails combo).  That code search should be something you can launch on a project.  This should include setting up codesearch to index the project.  Make sure the code search is buildable and installable and you can install from chief (this is a recurring theme for all other tools we ant to integrate).  Make sure changes to code graph search are pushed back to the project.  Here is where the code search is https://github.com/geekychris/code_graph_search

## Attention & notifications

## Operations logging & analytics
- [ ] {id:6db5} Analytics dashboard in the Wails UI [priority:med]
  Per-project charts: task velocity (completed/week), avg time-to-complete, defer rate, top-flagged categories. Global: cross-project time distribution, wake-up-poke frequency, resource contention timeline, notification volume by urgency. Reuse chart patterns from claude-session-analyzer where sensible.

## Messaging integrations

## Multi-project coordination
- [ ] {id:ce12} Per-project token / cost tracking [priority:med]
  Ingest usage from Claude's stream-json (headless mode) or shell out to `ccusage`. Show $/tokens per project and per task. Alert on budget overage. Feeds into DND ("stop pokes once daily budget hit").

## Agentic ergonomics
- [ ] {id:04b7} Voice / phone quick-add via Telegram
  Send a voice memo or text to the Telegram bot → Whisper transcription → new backlog item in the currently-focused project (or explicit `--project`). Extends the Telegram backend.
- [ ] {id:249e} Constitution linter
  Periodic sweep + post-turn hook: does the session's activity match `constitution.md`? Flag obvious divergences (e.g., constitution forbids network calls but Claude ran `curl`). Surface in the answer inbox as `info` urgency.
- [ ] {id:d00a} Sync between machines
  Git- or rsync-based sync of the markdown files + a subset of `chief.db`. Conflict resolution on task state (done-vs-done → done; deferred-vs-done → done wins; both active → last-write-wins with warning). Keeps desktop and laptop in step.

## Chief-internal
