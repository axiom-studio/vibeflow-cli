# @axiom-studio/vibeflow-setup

One-command setup for VibeFlow. Replaces the manual Setup-page steps with:

```bash
npx @axiom-studio/vibeflow-setup --api-key <API_KEY>
```

It will:

1. Download the matching `vibeflow-cli` release binary for your OS/arch into `~/.vibeflow/bin`.
2. Install **tmux** if it is missing (apt / dnf / yum / brew).
3. Run `vibeflow bootstrap --all --api-key <key>`, which writes the VibeFlow MCP
   server config for every supported agent (Claude CLI, Claude Desktop, Gemini,
   Cursor, Codex) — endpoint, token, and the 300000 ms client timeout.

## Options

| Flag | Default | Meaning |
|---|---|---|
| `--api-key <key>` | (required) | VibeFlow API key, from Account > API Keys |
| `--base-url <url>` | `https://cloud.axiomstudio.ai` | VibeFlow base URL |
| `--agents <csv>` | all | Configure only these agents |
| `--all` | on | Configure all supported agents |
| `--version <tag>` | latest | Pin a specific `vibeflow-cli` release |

## Verify

```bash
claude mcp list   # should show: vibeflow ... ✓ Connected
```

## Notes

- Requires Node >= 18. No npm dependencies (built-in modules only).
- macOS and Linux (amd64/arm64) are supported. Windows falls back to the manual
  Setup-page steps for now.
- Model selection from the Setup UI is handled at agent-launch time, not by
  `bootstrap`, so it is not a flag here.
