# @axiom-studio/vibeflow-setup

One-command setup for VibeFlow. Replaces the manual Setup-page steps with:

```bash
npx @axiom-studio/vibeflow-setup --api-key <API_KEY>
```

It will:

1. Download the matching `vibeflow-cli` release binary for your OS/arch into
   `~/.vibeflow/bin`, after verifying its SHA-256 against the release's published
   `checksums.txt`. A mismatch deletes the download and aborts — the binary is
   executed in step 3, so an unverified artifact is never run.
2. Install **tmux** if it is missing (apt / dnf / yum / apk / brew). Skipped on
   Windows. If tmux is still missing afterwards the installer says so explicitly,
   because `vibeflow launch` cannot work without it.
3. Run `vibeflow bootstrap --all --api-key <key>`, which writes **7 targets**: the
   MCP server config for all six supported agents — Claude CLI, Claude Desktop,
   Gemini CLI, Cursor, Codex, **Kiro CLI** — plus the `vibeflow-cli` `config.yaml`
   that stores your API key.

   Each agent config carries the endpoint and a `${MCP_TOKEN}` reference rather
   than the key itself. Four of the five JSON agents also get the 300000 ms
   client timeout; **Claude CLI deliberately gets none**, because it honors the
   `MCP_TIMEOUT` environment variable instead (bootstrap prints a note about
   this). Codex uses TOML with `bearer_token_env_var`. Claude Desktop is the one
   exception that stores the real key, in its own `env` block at mode 0600 —
   it is a GUI app, so it cannot receive a variable injected at launch.

## Options

| Flag | Default | Meaning |
|---|---|---|
| `--api-key <key>` | (required) | VibeFlow API key, from Account > API Keys |
| `--base-url <url>` | `https://cloud.axiomstudio.ai` | VibeFlow base URL |
| `--agents <csv>` | all | Configure only these agents |
| `--all` | on | Configure all supported agents |
| `--version <tag>` | latest | Pin a specific `vibeflow-cli` release |
| `--skip-checksum` | off | Skip SHA-256 verification of the download (not recommended) |

## Verify

The configs reference the token as `${MCP_TOKEN}`, so the variable must be set in
whatever shell you verify from:

```bash
MCP_TOKEN=<your-api-key> claude mcp list   # should show: vibeflow ... ✓ Connected
```

Without it you get `[Warning] mcpServers.vibeflow: Missing environment variables:
MCP_TOKEN` and no connection. That is expected, not a failed setup — `vibeflow
launch` and the TUI inject `MCP_TOKEN` into every agent they start, so **only
hand-started agents need the export**. To verify through the supported path
instead, run `vibeflow launch` and let the agent call `list_projects`.

## Notes

- Requires Node >= 18. No npm dependencies (built-in modules only).
- Linux, macOS, and Windows (amd64/arm64) are supported.
- On Windows the MCP config is written normally, so Claude Desktop and the other
  agents connect — but `vibeflow launch` needs **tmux**, which has no Windows
  port, so run session launching under WSL. Step 2 (tmux install) is skipped.
- Model selection from the Setup UI is handled at agent-launch time, not by
  `bootstrap`, so it is not a flag here.
