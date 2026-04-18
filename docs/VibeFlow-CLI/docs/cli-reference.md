# CLI reference

The binary name is **`vibeflow`**. Root command with no subcommand runs the **TUI**.

## Global flags (root)

| Flag | Description |
|------|-------------|
| `--config` | Path to config file (default `<root>/config.yaml`) |
| `--root` | Root directory for config, sessions, and logs (default `~/.vibeflow-cli`). Also settable via `VIBEFLOW_ROOT` env var. Enables isolated parallel instances. |
| `--server-url` | Override VibeFlow server URL |
| `--project` | Default project name for VibeFlow |

## Commands

### `vibeflow version`

Prints build version, commit, and build date.

### `vibeflow launch`

Create and launch a session without the full wizard. Key flags:

| Flag | Description |
|------|-------------|
| `--provider` | Provider key: `claude`, `codex`, `cursor`, `gemini`, or a custom key from `config.yaml` |
| `--branch` | Git branch (default `main`) |
| `--worktree` | Create a new git worktree for the session |
| `--new-branch` | Create a new git branch (used with `--worktree`) |
| `--worktree-name` | Custom worktree directory name (default: auto-generated) |
| `--skip-permissions` | Skip permission prompts (autonomous mode) |
| `--llm-gateway` | Route LLM requests through the VibeFlow server's LLM Gateway |

Examples:

```bash
vibeflow launch --provider claude --branch main
vibeflow launch --provider cursor --worktree --new-branch
vibeflow launch --provider codex --skip-permissions --llm-gateway
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

Kill the existing tmux session and re-launch the agent with the same provider, branch, worktree, working directory, environment, and **stored `SkipPermissions` value** — so an autonomous session stays autonomous after restart. Looks the session up in the active store first, then falls back to the session cache for dead sessions.

| Flag | Description |
|------|-------------|
| `--skip-permissions` | Explicitly override the stored autonomous setting. Pass `--skip-permissions=true` to force autonomous mode or `--skip-permissions=false` to force interactive mode; omit the flag to preserve whatever the session was launched with. |

See [Advanced topics](advanced-topics.md) for the session cache behavior that enables restart after tmux exits.

### `vibeflow worktrees` (alias: `wt`)

List or manage git worktrees related to the tool.

### `vibeflow check [directory]`

Check for **session conflicts** (`.vibeflow-session*` files vs active tmux).

### `vibeflow config`

Re-run interactive configuration.

### `vibeflow agent-doc <provider>`

Print the embedded agent documentation template for the given provider to stdout. Provider keys: `claude` → `CLAUDE.md`, `codex` → `AGENTS.md`, `cursor` → `AGENTS.md`, `gemini` → `GEMINI.md`. Useful for inspecting or piping the embedded template outside of the normal launch flow (launch automatically writes these files via `EnsureAllAgentDocs`, deduplicating `AGENTS.md` when both Codex and Cursor are configured).

Use `vibeflow --help` and `vibeflow <command> --help` for the exact flag set in your installed version.

## Next steps

- [Providers](providers.md)
- [Worktrees & session files](worktrees-session-files.md)
