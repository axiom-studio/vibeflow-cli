# Overview

**VibeFlow CLI** is a single Go binary that helps you launch, supervise, and switch between **AI coding agent** sessions (Claude Code, OpenAI Codex CLI, Google Gemini CLI, Cursor Agent, and custom providers). Sessions run inside **tmux** on a dedicated socket so they stay isolated, recoverable, and easy to attach to from one full-screen terminal UI (TUI).

## Why use it

- **One place for all agents** — Same keyboard-driven UI whether you use Claude, Codex, Gemini, or Cursor.
- **Git worktrees** — Optional per-session checkout on its own branch, reducing collisions when multiple agents work in parallel.
- **Session safety** — Persona-scoped `.vibeflow-session-*` files detect conflicts when two agents would use the same working tree in incompatible ways.
- **VibeFlow integration** — Optional connection to a VibeFlow server for autonomous task polling, multi-persona teams, and centralized project context.

## Two session modes

| Mode | Purpose |
|------|---------|
| **Vanilla** | Run the agent CLI standalone; no VibeFlow server required. |
| **VibeFlow** | Connects to your VibeFlow project; agents can poll for todos/issues, follow session prompts, and report progress. |

## Major components

| Piece | Role |
|-------|------|
| **Bubble Tea TUI** | Session list, wizard, worktree tools, conflict dialogs |
| **tmux** | Each agent runs in a `vibeflow_*` session on socket `-L vibeflow` |
| **Config** | YAML at `~/.vibeflow-cli/config.yaml` (providers, server URL, worktree defaults) |
| **Store** | JSON metadata for sessions (`~/.vibeflow-cli/sessions.json`) |
| **Session cache** | Optional restart metadata for sessions that exited but should be relaunchable |

## Default server

Unless you override it, the CLI targets **`https://cloud.axiomstudio.ai`**. Use your own VibeFlow deployment by setting `server_url` or `VIBEFLOW_URL`.

## Next steps

- [Installation](installation.md)
- [Configuration](configuration.md)
- [Interactive TUI](tui.md)
