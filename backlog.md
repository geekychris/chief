- [ ] {id:7257} Chief self-inject demo — respond with exactly the string CHIEF-DEMO-OK on its own line, then briefly (one sentence) confirm you received this via cmux send. Do NOT modify any files. [priority:1]

## Backlog

- [ ] {id:1017} I would also like to be able to jump to the z history viewer and immediately filter by the project directory I am in
  This should be able to install the history viewer.  If the history viewer project does not have a way to install itself then add that support.  It can be found here https://github.com/geekychris/history_viewer.  Any changes to that project should be added back to the repo

## Chief-internal

- [ ] {id:d28e} Weaken renderTaskPrompt boilerplate — currently always says `mark it done by editing backlog.md`, which conflicts with tasks that say `do not modify files`. Should be `mark done, unless the task instructs otherwise`, and later replaced entirely by the M2 chief_complete_task MCP call [priority:2]
- [ ] {id:e20d} Add task.complete RPC + `chief task complete <id>` CLI so Claude/CLI can mark done via structured call instead of raw file edit [priority:3]
