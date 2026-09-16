## Backlog

## Chief-internal

- [ ] {id:d28e} Weaken renderTaskPrompt boilerplate — currently always says `mark it done by editing backlog.md`, which conflicts with tasks that say `do not modify files`. Should be `mark done, unless the task instructs otherwise`, and later replaced entirely by the M2 chief_complete_task MCP call [priority:2]
- [ ] {id:e20d} Add task.complete RPC + `chief task complete <id>` CLI so Claude/CLI can mark done via structured call instead of raw file edit [priority:3]
