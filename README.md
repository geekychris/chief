# Chief

Multi-project Claude Code orchestrator. Chief knows about every project you
work in, tracks their backlogs and completions, and can hand the next task
to the appropriate Claude session running inside [cmux][].

Runs as a launchd-supervised daemon (`chiefd`), a menu-bar helper (Chief-Menu.app),
a native Wails window (Chief.app), and a `chief` CLI — all sharing state via
a unix socket.

## What it gives you

- **One inbox across N projects.** Backlogs live as `backlog.md` in each repo
  (human-authored, git-versioned). Chief mirrors them into SQLite so you can
  query and act across every project at once.
- **In-place completion history.** When a `[x]` task appears in a backlog,
  Chief moves it into `completedlog.md` (newest-first) with a timestamp,
  keeping the working backlog lean.
- **Send-to-Claude.** From the Chief UI, click a task → "Send to cmux Claude".
  Chief finds the cmux surface running Claude in that project's cwd and
  injects the task prompt into its pty. First send-per-project prompts you
  to pick the surface; the choice is saved in `.chief/project.yaml`.
- **Analyzer deep-link.** Chief detects [claude-session-analyzer][]. If
  installed, "Open in Analyzer" launches the app pre-navigated to the
  focused project. If not, one click clones + builds it into
  `~/.local/bin/ct` and `~/Applications/claude-trace.app`.

[cmux]: https://cmux.com
[claude-session-analyzer]: https://github.com/geekychris/claude-session-analyzer

## Install

```bash
git clone https://github.com/geekychris/chief.git
cd chief
make install        # chiefd + chief CLI under launchctl
make install-menu   # Chief-Menu.app in menu bar
make install-ui     # Chief.app main window
```

Binaries land in `~/.local/bin/`, apps in `~/Applications/`, launchd plists
in `~/Library/LaunchAgents/`. Reversible with `make uninstall uninstall-menu
uninstall-ui`.

## cmux socket auth (one-time)

Chief drives cmux via the socket CLI, which by default only accepts calls
from processes spawned inside cmux. Chief runs under launchd, so you need to
enable password-mode:

1. Add to `~/.config/cmux/cmux.json`:

   ```jsonc
   "automation": {
     "socketControlMode": "password",
     "socketPassword": "<generate a 32-byte urlsafe-b64 token>"
   }
   ```

2. Mirror the same token into `~/Library/Application Support/Chief/config.yaml`:

   ```yaml
   cmux:
     socket_password: <same token>
   ```

3. `cmux reload-config && launchctl kickstart -k gui/$(id -u)/com.chris.chiefd`.

A `chief cmux init-password` subcommand automating this is on the backlog.

## Register a project

```bash
chief project add                        # cwd
chief project add /path/to/other/repo
chief project list
chief backlog list --project my-repo --status pending
chief task show 16ec
```

## Repo layout

```
cmd/
├── chiefd/        # daemon: SQLite + fsnotify + JSON-RPC unix socket
├── chief/         # Cobra CLI (thin client of chiefd)
├── chief-menu/    # systray helper → bundled as Chief-Menu.app
├── chief-mcp/     # stdio → socket bridge (reserved for M2 MCP work)
└── chief-ui/      # Wails app → Chief.app
internal/
├── store/         # SQLite (WAL) + DAO + migrations
├── backlog/       # round-trip markdown parser + writer (echo-safe)
├── project/       # register/rescan/add-task/completion sweep
├── fswatch/       # fsnotify + debounce + hash-based echo suppression
├── cmux/          # thin wrapper on cmux CLI (list-panels, send)
├── claudetrace/   # slug math + AnalyzerAppBinary probe
├── installer/     # analyzer clone + build (chief-controlled install)
├── config/        # ~/Library/Application Support/Chief/config.yaml loader
├── methods/       # shared RPC request/response schemas
└── ipc/           # unix-socket JSON-RPC protocol
```

## Status

Milestones from the design plan:

- ✅ **M0** skeleton (daemon + CLI + ping)
- ✅ **M1** projects + backlog markdown mirror + fsnotify + ID minting
- ✅ **M3** menu bar (systray, LSUIElement app) + main window (Wails)
- ✅ Manual "Send to cmux Claude" (pre-MCP)
- ✅ completedlog.md sweep
- ✅ Analyzer detection, one-click install, and deep-link
- ⬜ **M2** MCP server for structured Claude-side pull
- ⬜ **M4** auto wake-up on backlog changes
- ⬜ **M5** shared resource leases (iPhone simulator, ports, ...)
- ⬜ **M6** notifications / push / `chief notify --wait`

See design plan and detailed history in commit log.
