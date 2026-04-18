# Configuration

## Config file location

Default path:

```
~/.vibeflow-cli/config.yaml
```

Override with either the `--config` flag (individual file) or the `--root` flag (entire state directory):

```bash
vibeflow --config /path/to/config.yaml
vibeflow --root /path/to/custom-root   # config at <root>/config.yaml, sessions at <root>/sessions.json, etc.
```

The `--root` flag enables fully isolated parallel instances with independent config, sessions, logs, PID lock, tmux socket, and session cache — useful for running multiple vibeflow-cli installations from different repository checkouts without interference.

## Common settings

Example structure (not exhaustive):

```yaml
server_url: https://cloud.axiomstudio.ai
api_token: your-api-token
default_provider: claude
default_project: my-project
default_work_dir: /path/to/projects
tmux_socket: vibeflow
poll_interval_seconds: 5
view_mode: flat   # flat or grouped

llm_gateway_enabled: false  # optional: route LLM traffic via server gateway when supported

worktree:
  base_dir: .claude/worktrees
  auto_create: true
  cleanup_on_kill: ask   # ask | always | never

error_recovery:
  enabled: true
  max_retries: 10
  debounce_seconds: 5
  backoff_multiplier: 2
  max_backoff_seconds: 300

providers:
  claude:
    name: Claude Code
    binary: claude
    vibeflow_integrated: true
    default: true
  # codex, gemini, cursor — see defaults in repo; merge overrides here
```

Built-in provider keys include **`claude`**, **`codex`**, **`gemini`**, and **`cursor`**. You can add custom providers by extending the `providers` map (see [Providers](providers.md)).

## Environment variable overrides

| Variable | Effect |
|----------|--------|
| `VIBEFLOW_URL` | Overrides `server_url` |
| `VIBEFLOW_TOKEN` | Overrides `api_token` |
| `VIBEFLOW_ROOT` | Overrides the root directory for config, sessions, and logs (equivalent to `--root`). The `--root` flag takes precedence when both are set. |

## CLI overrides

When launching the TUI:

```bash
vibeflow --server-url https://example.com --project my-project
```

## Logs and data files

All paths below are resolved relative to the root directory (default `~/.vibeflow-cli`, overridable via `--root` or `VIBEFLOW_ROOT`).

| Path | Purpose |
|------|---------|
| `<root>/vibeflow-cli.log` | Rotating log (1 MB) |
| `<root>/sessions.json` | Session metadata (file-locked) |
| `<root>/session_cache.json` | Cache for restart-after-exit; persists full launch parameters so `vibeflow restart` works after a session exits tmux |
| `<root>/vibeflow.pid` | PID lock so only one TUI instance runs per root |

## Next steps

- [CLI reference](cli-reference.md)
- [Troubleshooting](troubleshooting.md)
