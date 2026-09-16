## Backlog
- [ ] {id:de35} smoke-test undo

## One-shots (proposed via 40fd)

## Proactive & scheduled (proposed via 40fd)
- [ ] {id:1b72} Kill idle Claude sessions — for cmux surfaces with a Claude session_id and no output/heartbeat in >N hours (per project.yaml), prompt to close (or auto-close per config). Saves API budget when a session is left running after work stops.

## Ergonomics + intelligence (proposed via 40fd)
- [ ] {id:bb54} Model migration helper — after a new Claude model ships, `chief model-migrate` scans open backlog items and asks Claude "given this task + the new model's capabilities, is it now a candidate for X approach or a shorter path?". Add responses as suggestion metadata.
- [ ] {id:8e23} Project bookmarks — hotkey (⌘1..⌘9) in Chief.app to jump between pinned projects. Also surface via `chief goto 1..9` CLI. Matches the "many projects, one focused at a time" workflow.

## Attention & notifications

## Operations logging & analytics

## Messaging integrations

## Multi-project coordination

## Agentic ergonomics

## Chief-internal
