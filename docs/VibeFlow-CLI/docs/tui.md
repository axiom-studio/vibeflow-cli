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
- **`b`** — **Quick branch switch** for the selected running session. Skips the full wizard; only re-runs the Branch → Worktree steps and inherits project / persona / provider / permissions from the current session. Refuses to switch in place when the working tree is dirty so uncommitted changes cannot be lost.
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
| `Enter` in an exited pane | Resume the agent in the same pane |

From the overlay you can jump between sessions and operations without stopping long-running agents.

## Dead session restart

If you accidentally exit an agent with **Ctrl+C**, press **Enter** in its dead pane to recover it.
The pane shows **Press Enter to resume**, and recovery keeps the same pane and attached terminal, including inside a workbench.
Enter continues to work normally in running agents.

Claude and Codex resume the exact conversation when its ID is available in the final exit hint.
Otherwise, recovery opens the harness's saved-conversation picker so you can choose the conversation to continue.
This works with Claude, Codex, Cursor, Qwen, Kiro, and Copilot's resume options, and Gemini's `/resume` browser.
Picker recovery waits for your selection and does not send a new VibeFlow initialization prompt.
The previous pane output is saved before replacement so its recovery information remains available.

On startup, the CLI offers a **restart** multiselect for cached sessions whose tmux session is missing or whose agent pane has exited.
Select sessions with **Space**, then press **Enter** to restart with the same provider, branch, VibeFlow init prompt, and permission flags.

Each entry shows whether it **resumes conversation** or starts fresh.
Claude and Codex can resume an exact conversation identified by their final exit hint, including Codex's multiline hint with an optional conversation name.
The startup restart picker and `vibeflow restart` start fresh when no exact conversation ID is available.
Use **Enter inside the dead pane** to choose a saved conversation instead.

## Next steps

- [Session wizard](session-wizard.md)
- [CLI reference](cli-reference.md)
