# Configuration

## Config file location

Default path:

```
~/.vibeflow-cli/config.yaml
```

Override with the global flag:

```bash
vibeflow --config /path/to/config.yaml
```

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

## CLI overrides

When launching the TUI:

```bash
vibeflow --server-url https://example.com --project my-project
```

## Logs and data files

| Path | Purpose |
|------|---------|
| `~/.vibeflow-cli/vibeflow-cli.log` | Rotating log (1 MB) |
| `~/.vibeflow-cli/sessions.json` | Session metadata (file-locked) |
| `~/.vibeflow-cli/session_cache.json` | Cache for restart-after-exit (optional) |
| `~/.vibeflow-cli/vibeflow.pid` | PID lock so only one TUI instance runs |

## Next steps

- [CLI reference](cli-reference.md)
- [Troubleshooting](troubleshooting.md)
