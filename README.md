```
 __      __ _  _            _____  _
 \ \    / /(_)| |          |  ___|| |
  \ \  / /  _ | |__    ___ | |_   | |  ___  __      __
   \ \/ /  | ||  _ \  / _ \|  _|  | | / _ \ \ \ /\ / /
    \  /   | || |_) ||  __/| |    | || (_) | \ V  V /
     \/    |_||_.__/  \___||_|    |_| \___/   \_/\_/
```

**by [axiomstudio.ai](https://axiomstudio.ai) | Copyright 2026**

---

# vibeflow-cli

A terminal session manager for multiple vibecoding agent CLIs. Launch, manage, and switch between AI coding agent sessions from a single TUI — with support for git worktrees, conflict detection, and both vanilla and VibeFlow-integrated modes.

## Supported Agents

| Agent | Binary | Mode |
|-------|--------|------|
| **Claude Code** | `claude` | VibeFlow-integrated (autonomous polling) |
| **OpenAI Codex CLI** | `codex` | Vanilla (standalone) |
| **Google Gemini CLI** | `gemini` | Vanilla (standalone) |

Custom providers can be added via configuration.

## Features

- **Multi-agent TUI** — Interactive terminal UI built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) for launching and managing sessions
- **Two session modes** — *VibeFlow mode* connects agents to the VibeFlow server for autonomous task polling; *Vanilla mode* launches agents standalone
- **tmux-based session management** — Each agent runs in an isolated tmux session on a dedicated socket (`-L vibeflow`), with clipboard passthrough for macOS paste support
- **Git worktree isolation** — Run multiple agents on different branches simultaneously without conflicts, with automatic worktree creation and cleanup
- **Session conflict detection** — Detects when multiple agents target the same directory and offers resolution (worktree, switch, or skip)
- **Provider environment variable management** — Automatically resolves required env vars (e.g. `MCP_TOKEN` for Codex, `GEMINI_API_KEY` for Gemini) from config or prompts interactively
- **Error recovery** — Built-in error pattern detection with automatic retry and backoff for common agent failures
- **Setup wizard** — First-run configuration wizard and guided session creation with directory history, branch selection, and persona picking

## Installation

```bash
go install vibeflow-cli/cmd/vibeflow@latest
```

Or build from source:

```bash
git clone https://github.com/axiom-studio/vibeflow-cli.git
cd vibeflow-cli
go build -o vibeflow ./cmd/vibeflow
```

### Prerequisites

- **Go 1.21+**
- **tmux 3.2+** (for `-e` env var passthrough)
- At least one supported agent CLI installed (`claude`, `codex`, or `gemini`)

## Usage

### Interactive TUI (default)

```bash
vibeflow
```

Launches the full TUI with session list, creation wizard, and management controls.

### CLI Subcommands

```bash
vibeflow launch          # Create and launch a new session
vibeflow list            # List active sessions (alias: ls)
vibeflow switch <name>   # Attach to a session
vibeflow kill <name>     # Kill a session
vibeflow worktrees       # List git worktrees (alias: wt)
vibeflow check [dir]     # Check for session conflicts
vibeflow config          # Re-run interactive configuration setup
vibeflow version         # Print version information
```

### Launch flags

```bash
vibeflow launch --provider claude --branch main
vibeflow launch --worktree --new-branch --provider codex
vibeflow launch --skip-permissions  # Autonomous mode
```

### TUI Keybindings

| Key | Action |
|-----|--------|
| `n` | New session (opens wizard) |
| `Enter` | Attach to selected session |
| `k` / `d` | Kill selected session |
| `r` | Refresh session list |
| `g` | Toggle grouped/flat view |
| `Ctrl+Q` | Open TUI popup (inside tmux session) |
| `q` / `Esc` | Quit |

## Configuration

Config file: `~/.vibeflow-cli/config.yaml`

```yaml
server_url: https://cloud.axiomstudio.ai
default_provider: claude
tmux_socket: vibeflow
default_work_dir: /path/to/projects

worktree:
  base_dir: .claude/worktrees
  auto_create: true
  cleanup_on_kill: ask    # ask, always, or never

error_recovery:
  enabled: true
  max_retries: 3
  debounce_seconds: 5

providers:
  claude:
    name: Claude Code
    binary: claude
    vibeflow_integrated: true
  codex:
    name: OpenAI Codex CLI
    binary: codex
  gemini:
    name: Google Gemini CLI
    binary: gemini
```

Environment variable overrides:
- `VIBEFLOW_URL` — Override `server_url`
- `VIBEFLOW_TOKEN` — Override `api_token`

## Session Wizard Steps

The interactive wizard walks through:

1. **Working directory** — Select from history or enter a path
2. **Session type** — VibeFlow (server-connected) or Vanilla (standalone)
3. **Project** — Select VibeFlow project (VibeFlow mode only)
4. **Persona** — Developer, architect, QA lead, etc. (VibeFlow mode only)
5. **Provider** — Choose agent (Claude, Codex, Gemini)
6. **Environment token** — Enter required API keys if missing
7. **Branch** — Select git branch or create new
8. **Worktree** — Use existing directory, create worktree, or select existing
9. **Permissions** — Skip permission prompts (autonomous mode)
10. **Confirm** — Review and launch

## License

Copyright 2026 Axiom Studio AI. All rights reserved.
