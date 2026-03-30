# Troubleshooting

## `vibeflow` says another instance is running

Only one TUI instance is allowed. Close the other process or delete the stale PID file only if you are sure no other `vibeflow` is running (`~/.vibeflow-cli/vibeflow.pid`).

## Agent binary not found

Install the provider CLI and ensure it is on **`PATH`**, or set an **absolute path** in `config.yaml` under `providers.<key>.binary`. For Cursor, the binary name is **`agent`**.

## Cannot reach VibeFlow server

- Check `server_url` / `VIBEFLOW_URL` and network/VPN.
- Verify `api_token` / `VIBEFLOW_TOKEN`.
- Server warnings on startup are non-blocking; Vanilla mode still works without the API.

## Session conflict dialog

Another agent already owns this directory/persona. Choose **switch to existing**, **new worktree**, or **clean stale session file** after confirming tmux no longer has that session.

## tmux issues

- Require **tmux 3.2+** for features the CLI relies on.
- Custom **`tmux_socket`** helps isolate vibeflow sessions from your personal tmux server.

## Recovery loops

If an agent keeps hitting API errors, check provider status, token quotas, and **error_recovery** settings. Reduce noise by tuning `debounce_seconds` and `max_retries`.

## Getting help

- Open an issue on the [vibeflow-cli repository](https://github.com/axiom-studio/vibeflow-cli).
- Review logs at `~/.vibeflow-cli/vibeflow-cli.log`.

## Next steps

- [Installation](installation.md)
- [Configuration](configuration.md)
