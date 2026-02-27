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

<p align="center">
  <img src="assets/illust-vibeflow.svg" alt="VibeFlow — AI Coding Session Manager" width="600" />
</p>

A terminal session manager for AI coding agents. Launch, manage, and switch between Claude Code, OpenAI Codex CLI, and Google Gemini CLI sessions from a single TUI — with git worktree isolation, session conflict detection, persona-based multi-agent workflows, and autonomous task execution via VibeFlow.

## Supported Agents

| Agent | Binary | Autonomous Flag | Prompt Style |
|-------|--------|-----------------|--------------|
| **Claude Code** | `claude` | `--dangerously-skip-permissions` | Positional argument |
| **OpenAI Codex CLI** | `codex` | `--full-auto` | Positional argument |
| **Google Gemini CLI** | `gemini` | `--yolo` | `-p` flag |

All three agents support both **Vanilla** (standalone) and **VibeFlow** (server-connected autonomous) session modes. Custom providers can be added via configuration.

## Features

- **Multi-agent TUI** — Interactive terminal UI built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) for launching and managing sessions
- **Two session modes** — *VibeFlow mode* connects agents to a VibeFlow server for autonomous task polling and execution; *Vanilla mode* launches agents standalone
- **Persona-based sessions** — Assign specialized personas (Developer, Architect, QA Lead, Security Lead, Product Manager, Project Manager, Customer) to VibeFlow sessions. Multiple personas can work concurrently in the same repository without conflict.
- **tmux-based session management** — Each agent runs in an isolated tmux session on a dedicated socket (`-L vibeflow`), with clipboard passthrough for macOS paste support
- **Git worktree isolation** — Run multiple agents on different branches simultaneously without conflicts, with automatic worktree creation and cleanup
- **Session conflict detection** — Detects when multiple agents target the same directory and offers resolution options (switch to existing, create worktree, clean up stale session, or cancel)
- **Grouped view mode** — Toggle between flat list and sessions grouped by git repository root, with collapsible groups
- **Error recovery** — Automatic error pattern detection with provider-specific recovery messages, exponential backoff, and configurable retry limits
- **Agent doc embedding** — Automatically copies vibeflow session rules (CLAUDE.md, AGENTS.md, GEMINI.md) to the working directory so agents pick up project conventions on startup
- **Provider environment variable management** — Resolves required env vars (e.g. bearer token for Codex from `~/.codex/config.toml`, `GEMINI_API_KEY` for Gemini) from saved config, environment, or interactive prompt
- **10-step session wizard** — Guided session creation with directory history, session type, project/persona selection, provider picking, branch selection, worktree strategy, and permission level
- **Server health check** — Non-blocking reachability check on startup with warning if the VibeFlow server is unreachable

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

- **Go 1.25+**
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
vibeflow delete <name>   # Delete a session (alias: rm)
vibeflow worktrees       # List git worktrees (alias: wt)
vibeflow check [dir]     # Check for session conflicts
vibeflow config          # Re-run interactive configuration setup
vibeflow version         # Print version information
```

### Launch Flags

```bash
vibeflow launch --provider claude --branch main
vibeflow launch --worktree --new-branch --provider codex
vibeflow launch --skip-permissions  # Autonomous mode
```

### TUI Keybindings

| Key | Action |
|-----|--------|
| `n` | New session (opens wizard) |
| `Enter` | Attach to selected session / toggle group collapse |
| `d` | Delete selected session |
| `D` | Detach (quit TUI, sessions keep running) |
| `r` | Retry recovery for failed session / refresh list |
| `g` | Toggle grouped/flat view |
| `w` | Manage worktrees |
| `j` / `k` | Move down / up |
| `?` | Show help |
| `q` | Quit (with confirmation if sessions exist) |

**Inside an agent session (tmux):**

| Key | Action |
|-----|--------|
| `Ctrl+Q` | Open VibeFlow menu popup |
| `Ctrl+\` | Open VibeFlow menu (backup key) |

## Configuration

Config file: `~/.vibeflow-cli/config.yaml`

```yaml
server_url: https://cloud.axiomstudio.ai
api_token: your-api-token
default_provider: claude
default_project: my-project
default_work_dir: /path/to/projects
tmux_socket: vibeflow
poll_interval_seconds: 5
view_mode: flat  # flat or grouped

worktree:
  base_dir: .claude/worktrees
  auto_create: true
  cleanup_on_kill: ask    # ask, always, or never

error_recovery:
  enabled: true
  max_retries: 3
  debounce_seconds: 5
  backoff_multiplier: 2

providers:
  claude:
    name: Claude Code
    binary: claude
    vibeflow_integrated: true
    default: true
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

1. **Working directory** — Select from recent history or enter a new path
2. **Session type** — VibeFlow (server-connected) or Vanilla (standalone)
3. **Project** — Select VibeFlow project (VibeFlow mode only)
4. **Persona** — Developer, Architect, QA Lead, Security Lead, Product Manager, Project Manager, or Customer (VibeFlow mode only)
5. **Provider** — Choose agent (Claude, Codex, Gemini) with availability detection
6. **Environment token** — Enter required API keys if not already saved
7. **Branch** — Select git branch or create new
8. **Worktree** — Use current directory, create new worktree, or specify custom path
9. **Permissions** — Skip permission prompts for autonomous mode
10. **Confirm** — Review and launch

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for details.
