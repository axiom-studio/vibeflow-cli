# Providers

A **provider** is a configured AI agent CLI: display name, binary name, launch template, optional environment, and whether it integrates with VibeFlow session files.

## Built-in providers

| Key | Display name | Binary (default) | Autonomous launch flags (when skip-permissions) |
|-----|----------------|------------------|-----------------------------------------------|
| `claude` | Claude Code | `claude` | `--dangerously-skip-permissions` |
| `codex` | OpenAI Codex CLI | `codex` | `--yolo` |
| `gemini` | Google Gemini CLI | `gemini` | `--yolo` |
| `cursor` | Cursor Agent | `agent` | `--yolo --approve-mcps` |
| `qwen` | Qwen Code | `qwen` | `--yolo` |

The **Cursor** provider uses the official Cursor CLI binary name **`agent`**, not `cursor`. Install the CLI from Cursor’s documentation if `agent` is not on your `PATH`.

**Qwen Code** is Alibaba's open-source coding agent, based on Google Gemini CLI with parser-level adaptations for Qwen-Coder models. Install with `npm install -g @qwen-code/qwen-code@latest`. The `--yolo` flag selects Qwen's `yolo` approval mode (full autonomous); the other modes (`default`, `plan`, `auto_edit`) are not exposed via the wizard in v1 — edit `~/.qwen/settings.json` or define a custom launch template if you need a middle-ground mode.

## VibeFlow-integrated providers

**Claude** and **Cursor** are marked VibeFlow-integrated in the default config (session file templates align with autonomous flows). **Codex**, **Gemini**, and **Qwen** remain available with their own launch templates; gateway and env behavior may differ by product.

## Prompt passing

- **Claude / Codex / Gemini / Qwen** — VibeFlow init prompts are passed in the style each CLI expects (positional vs flags); the wizard and launch path handle this.
- **Cursor** — Uses **`AGENTS.md`** for embedded rules alongside other agent docs where applicable.

## LLM Gateway

When enabled in config or the wizard, the CLI can set **per-provider environment variables** so traffic goes through your VibeFlow server’s LLM gateway (where supported). Routing for Cursor may evolve; if gateway env mapping is empty for a provider, the CLI leaves gateway vars unset for that agent.

**Qwen Code** uses the OpenAI-compatible env vars (`OPENAI_API_KEY`, `OPENAI_BASE_URL`) — same wiring as Codex and Gemini. Note that the `qwen` CLI auto-loads `.env` files from the current working directory and `~/.qwen/.env` at startup. If you have `OPENAI_BASE_URL` set in either of those, it can interact with the value the wizard sets for the tmux process: process-level env normally takes precedence, but users running mixed direct/gateway setups should double-check that the gateway is actually being used (e.g. by checking the request URL in the gateway server logs). Qwen also supports DashScope, Anthropic, Gemini, Ollama, vLLM, and BailianCoding auth modes for direct use; these are not touched by the gateway wiring — you own the corresponding env vars (`DASHSCOPE_API_KEY` etc.).

## Custom providers

You can add entries under `providers:` in `config.yaml` with:

- `name`, `binary`
- `launch_template` (Go text template with fields such as `Binary`, `SkipPermissions`)
- Optional `env`, `session_file`, `default`

Defaults from the built-in set are merged with your file; see the source `DefaultConfig()` in `internal/vibeflowcli/config.go` for the canonical templates.

## Next steps

- [Session wizard](session-wizard.md)
- [Configuration](configuration.md)
