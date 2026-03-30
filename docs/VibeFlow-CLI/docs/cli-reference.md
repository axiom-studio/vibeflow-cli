# CLI reference

The binary name is **`vibeflow`**. Root command with no subcommand runs the **TUI**.

## Global flags (root)

| Flag | Description |
|------|-------------|
| `--config` | Path to config file (default `~/.vibeflow-cli/config.yaml`) |
| `--server-url` | Override VibeFlow server URL |
| `--project` | Default project name for VibeFlow |

## Commands

### `vibeflow version`

Prints build version, commit, and build date.

### `vibeflow launch`

Create and launch a session without the full wizard (flags vary). Examples:

```bash
vibeflow launch --provider claude --branch main
vibeflow launch --worktree --new-branch --provider codex
vibeflow launch --skip-permissions
```

### `vibeflow list` (alias: `ls`)

List active sessions.

### `vibeflow switch <session-name>`

Attach to a tmux session by name.

### `vibeflow kill <session-name>`

Terminate a session.

### `vibeflow delete <session-name>` (alias: `rm`)

Remove session metadata and session file; may interact with worktree cleanup per config.

### `vibeflow restart <session-name>`

Restart using cached parameters when available (see [Advanced topics](advanced-topics.md)).

### `vibeflow worktrees` (alias: `wt`)

List or manage git worktrees related to the tool.

### `vibeflow check [directory]`

Check for **session conflicts** (`.vibeflow-session*` files vs active tmux).

### `vibeflow config`

Re-run interactive configuration.

### `vibeflow agent-doc <provider>`

Ensure embedded agent documentation templates are written for the given provider (used by vibeflow-cli to sync `CLAUDE.md`, `AGENTS.md`, `GEMINI.md`, etc., depending on provider).

Use `vibeflow --help` and `vibeflow <command> --help` for the exact flag set in your installed version.

## Next steps

- [Providers](providers.md)
- [Worktrees & session files](worktrees-session-files.md)
