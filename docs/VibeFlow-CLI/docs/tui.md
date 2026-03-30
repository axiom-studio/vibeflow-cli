# Interactive TUI

Running `vibeflow` with no subcommand starts the **Bubble Tea** full-screen terminal UI: session list, creation wizard, worktree management, and conflict resolution.

## Global behavior

- **Single TUI instance** — A PID lock prevents two TUI processes from running at once; if another instance is active, the CLI exits gracefully.
- **tmux socket** — Sessions use the configured socket name (default `vibeflow`) so they do not collide with your personal tmux server.
- **Server health** — On startup the CLI may warn if the VibeFlow server URL is unreachable (non-blocking).

## Session list

- Navigate with **`j`** / **`k`** (or arrow keys where supported).
- **`Enter`** — Attach to the selected session or toggle a collapsed group (grouped view).
- **`n`** — New session (opens the wizard).
- **`d`** — Delete the selected session (and optional worktree cleanup per config).
- **`D`** — Detach from the TUI (sessions keep running).
- **`r`** — Retry recovery for a failed session or refresh the list.
- **`g`** — Toggle **flat** vs **grouped** view (sessions grouped by repository root).
- **`w`** — Worktree management.
- **`?`** — Help.
- **`q`** — Quit (may prompt if sessions are active).

## Inside tmux (agent session)

| Key | Action |
|-----|--------|
| `Ctrl+Q` | Open VibeFlow menu overlay |
| `Ctrl+\` | Alternate VibeFlow menu shortcut |

From the overlay you can jump between sessions and operations without stopping long-running agents.

## Dead session restart

If the CLI finds **cached launch parameters** for sessions that are no longer in tmux, it can offer a **restart** multiselect on startup so you can relaunch with the same provider, branch, VibeFlow init prompt, and permission flags.

## Next steps

- [Session wizard](session-wizard.md)
- [CLI reference](cli-reference.md)
