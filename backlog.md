## Backlog

## Attention & notifications

## Operations logging & analytics

## Messaging integrations

## Multi-project coordination
- [ ] {id:ce12} Per-project token / cost tracking [priority:med]
  Ingest usage from Claude's stream-json (headless mode) or shell out to `ccusage`. Show $/tokens per project and per task. Alert on budget overage. Feeds into DND ("stop pokes once daily budget hit").

## Agentic ergonomics
- [ ] {id:d00a} Sync between machines
  Git- or rsync-based sync of the markdown files + a subset of `chief.db`. Conflict resolution on task state (done-vs-done → done; deferred-vs-done → done wins; both active → last-write-wins with warning). Keeps desktop and laptop in step.

## Chief-internal
