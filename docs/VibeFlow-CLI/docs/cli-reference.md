# CLI reference

The binary name is **`vibeflow`**. Root command with no subcommand runs the **TUI**.

## Global flags (root)

| Flag | Description |
|------|-------------|
| `--config` | Path to config file (default `<root>/config.yaml`) |
| `--root` | Root directory for config, sessions, and logs (default `~/.vibeflow-cli`). Also settable via `VIBEFLOW_ROOT` env var. Enables isolated parallel instances. |
| `--server-url` | Override VibeFlow server URL |
| `--project` | Default project name for VibeFlow |
| `--mcp` | MCP server tool name used in the agent init prompt (default: `vibeflow`). Override if you run a renamed or forked MCP server. |

## Commands

### `vibeflow version`

Prints build version, commit, and build date.

### `vibeflow launch`

Create and launch a session without the full wizard. Key flags:

| Flag | Description |
|------|-------------|
| `--provider` | Provider key: `claude`, `codex`, `cursor`, `gemini`, `qwen`, or a custom key from `config.yaml` |
| `--branch` | Git branch (default `main`) |
| `--worktree` | Create a new git worktree for the session |
| `--new-branch` | Create a new git branch (used with `--worktree`) |
| `--worktree-name` | Custom worktree directory name (default: auto-generated) |
| `--skip-permissions` | Skip permission prompts (autonomous mode) |
| `--model` | Model id to pass to each launched provider session |
| `--models` | Comma-separated `persona=model` overrides for team launches |
| `--llm-gateway` | Route LLM requests through the VibeFlow server's LLM Gateway |
| `--openshell` | Run the agent command inside an NVIDIA OpenShell sandbox |
| `--openshell-sandbox` | OpenShell sandbox name |
| `--openshell-from` | OpenShell sandbox image/base |
| `--openshell-policy` | OpenShell policy YAML path |
| `--openshell-provider` | Comma-separated OpenShell provider names to attach |
| `--openshell-no-auto-providers` | Disable OpenShell credential auto-provider discovery |

Examples:

```bash
vibeflow launch --provider claude --branch main
vibeflow launch --provider cursor --worktree --new-branch
vibeflow launch --provider codex --skip-permissions --llm-gateway
vibeflow launch --provider claude --personas developer,architect --model sonnet --models developer=gpt-5.1-codex,architect=opus
vibeflow launch --provider qwen --skip-permissions
vibeflow launch --provider codex --openshell --openshell-sandbox vf-main
```

Model flags apply when the provider process starts and are stored in session metadata so `vibeflow restart` reuses the same model. They do not rewrite a model inside an already-running provider process. Built-in providers validate model ids against the CLI's curated catalog; custom providers accept any model string.

### `vibeflow models [provider]`

List curated model ids for the built-in providers. Pass a provider key to show one provider:

```bash
vibeflow models
vibeflow models codex
```

### `vibeflow list` (alias: `ls`)

List active sessions.

### `vibeflow switch <session-name>`

Attach to a tmux session by name.

### `vibeflow kill <session-name>`

Terminate a session.

| Flag | Description |
|------|-------------|
| `--cleanup-worktree` | Also remove the git worktree associated with the session |

### `vibeflow delete <session-name>` (alias: `rm`)

Remove session metadata and session file; may interact with worktree cleanup per config.

| Flag | Description |
|------|-------------|
| `--cleanup-worktree` | Also remove the git worktree associated with the session |

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

Print the embedded agent documentation template for the given provider to stdout. Provider keys: `claude` → `CLAUDE.md`, `codex` → `AGENTS.md`, `cursor` → `AGENTS.md`, `gemini` → `GEMINI.md`, `qwen` → `QWEN.md`. Useful for inspecting or piping the embedded template outside of the normal launch flow (launch automatically writes these files via `EnsureAllAgentDocs`, deduplicating `AGENTS.md` when both Codex and Cursor are configured).

Use `vibeflow --help` and `vibeflow <command> --help` for the exact flag set in your installed version.

## Next steps

- [Providers](providers.md)
- [Worktrees & session files](worktrees-session-files.md)
