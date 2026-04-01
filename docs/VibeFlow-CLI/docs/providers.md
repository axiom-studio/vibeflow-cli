# Providers

A **provider** is a configured AI agent CLI: display name, binary name, launch template, optional environment, and whether it integrates with VibeFlow session files.

## Built-in providers

| Key | Display name | Binary (default) | Autonomous launch flags (when skip-permissions) |
|-----|----------------|------------------|-----------------------------------------------|
| `claude` | Claude Code | `claude` | `--dangerously-skip-permissions` |
| `codex` | OpenAI Codex CLI | `codex` | `--yolo` |
| `gemini` | Google Gemini CLI | `gemini` | `--yolo` |
| `cursor` | Cursor Agent | `agent` | `--yolo --approve-mcps` |

The **Cursor** provider uses the official Cursor CLI binary name **`agent`**, not `cursor`. Install the CLI from Cursor’s documentation if `agent` is not on your `PATH`.

## VibeFlow-integrated providers

**Claude** and **Cursor** are marked VibeFlow-integrated in the default config (session file templates align with autonomous flows). Codex and Gemini remain available with their own launch templates; gateway and env behavior may differ by product.

## Prompt passing

- **Claude / Codex / Gemini** — VibeFlow init prompts are passed in the style each CLI expects (positional vs flags); the wizard and launch path handle this.
- **Cursor** — Uses **`AGENTS.md`** for embedded rules alongside other agent docs where applicable.

## LLM Gateway

When enabled in config or the wizard, the CLI can set **per-provider environment variables** so traffic goes through your VibeFlow server’s LLM gateway (where supported). Routing for Cursor may evolve; if gateway env mapping is empty for a provider, the CLI leaves gateway vars unset for that agent.

## Custom providers

You can add entries under `providers:` in `config.yaml` with:

- `name`, `binary`
- `launch_template` (Go text template with fields such as `Binary`, `SkipPermissions`)
- Optional `env`, `session_file`, `default`

Defaults from the built-in set are merged with your file; see the source `DefaultConfig()` in `internal/vibeflowcli/config.go` for the canonical templates.

## Next steps

- [Session wizard](session-wizard.md)
- [Configuration](configuration.md)
