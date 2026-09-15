- [ ] {id:7257} Chief self-inject demo — respond with exactly the string CHIEF-DEMO-OK on its own line, then briefly (one sentence) confirm you received this via cmux send. Do NOT modify any files. [priority:1]

## Backlog

- [ ] {id:e724} Chief ui should show backlog items and completed items.  completed should perhaps show in a different color to show thats the completed list.
  Chief ui should show backlog items and completed items.  completed should perhaps show in a different color to show thats the completed list.  Further you should be able to edit backlog items.  You should also be able to reorder items.  
  since we are primarily using claude I would like to be able to jump to claude session analyzer to analyze the session associated with any of the projects managed under chief. https://github.com/geekychris/claude-session-analyzer

## Chief-internal

- [ ] {id:e20d} Add task.complete RPC + `chief task complete <id>` CLI so Claude/CLI can mark done via structured call instead of raw file edit [priority:3]
- [ ] {id:d28e} Weaken renderTaskPrompt boilerplate — currently always says `mark it done by editing backlog.md`, which conflicts with tasks that say `do not modify files`. Should be `mark done, unless the task instructs otherwise`, and later replaced entirely by the M2 chief_complete_task MCP call [priority:2]
