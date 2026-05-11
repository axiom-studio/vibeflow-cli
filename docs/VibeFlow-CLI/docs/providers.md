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

VibeFlow init prompts are passed in the argument shape each CLI expects so the agent process stays running for the autonomous polling loop. The wizard and launch path pick this automatically:

- **Claude / Codex / Cursor** — positional argument (`claude '<prompt>'`). These CLIs treat a positional prompt as the initial input and stay interactive.
- **Gemini** — `-p '<prompt>'` (non-interactive headless mode).
- **Qwen** — `-i '<prompt>'` (`--prompt-interactive`: execute the prompt and continue in interactive mode). Qwen's positional argument is **one-shot mode** (process the prompt, then exit) — wrong for vibeflow autonomous sessions, which need the agent to remain running.

## LLM Gateway

When enabled in config or the wizard, the CLI can set **per-provider environment variables** so traffic goes through your VibeFlow server’s LLM gateway (where supported). Routing for Cursor may evolve; if gateway env mapping is empty for a provider, the CLI leaves gateway vars unset for that agent.

**Qwen Code** uses the OpenAI-compatible env vars (`OPENAI_API_KEY`, `OPENAI_BASE_URL`) — same wiring as Codex and Gemini. Note that the `qwen` CLI auto-loads `.env` files from the current working directory and `~/.qwen/.env` at startup. If you have `OPENAI_BASE_URL` set in either of those, it can interact with the value the wizard sets for the tmux process: process-level env normally takes precedence, but users running mixed direct/gateway setups should double-check that the gateway is actually being used (e.g. by checking the request URL in the gateway server logs). Qwen also supports DashScope, Anthropic, Gemini, Ollama, vLLM, and BailianCoding auth modes for direct use; these are not touched by the gateway wiring — you own the corresponding env vars (`DASHSCOPE_API_KEY` etc.).

## Qwen launch config (API-key mode)

When you launch a qwen session **without** the LLM Gateway, the wizard inserts a dedicated **Qwen launch config** step that captures the OpenAI-compatible launch environment for the tmux process:

| Env var | Source |
|---|---|
| `OPENAI_API_KEY` | `StepEnvToken` (saved → shell → prompt). Persists in `cfg.SavedEnvVars`. |
| `OPENAI_BASE_URL` | `StepQwenLaunchConfig` vendor preset, editable. |
| `OPENAI_MODEL` | `StepQwenLaunchConfig` vendor preset, editable. |

The step is **skipped** for any other provider, and skipped for qwen when the LLM Gateway is enabled (the gateway provides its own `OPENAI_API_KEY` + `OPENAI_BASE_URL`).

### Vendor presets

| Vendor | Model | Base URL |
|---|---|---|
| OpenAI | `gpt-4o-mini` | `https://api.openai.com/v1` |
| Qwen (DashScope) | `qwen3-coder-plus` | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` |
| z.ai | `glm-4.6` | `https://api.z.ai/api/paas/v4` |
| Custom | _(empty)_ | _(empty)_ |

**Behavior**

- `j` / `k` cycles vendor rows. The model + base URL inputs auto-fill from the focused vendor's preset _until you start typing_ — once edited, vendor row navigation preserves your input.
- Press `r` on any row to reset both inputs to the current vendor's preset (clears the "edited" flag).
- Move the cursor to the **Model** or **Base URL** rows below the vendor list to type custom values.
- Pressing `enter` writes `OPENAI_BASE_URL` and `OPENAI_MODEL` to the launch env block. Empty inputs (e.g. on the **Custom** row before the user types) are not written, so they will not override values inherited from `~/.qwen/.env`.

### Launch example (DashScope)

1. Run `vibeflow launch` and pick your working directory.
2. Pick **Vanilla** session type.
3. Pick **Qwen Code** in the provider step.
4. The env-token step prompts for `OPENAI_API_KEY` (saved on first run; subsequent launches skip the prompt).
5. The new **Qwen launch config** step opens. Highlight **Qwen (DashScope)** — both inputs auto-fill with `qwen3-coder-plus` and `https://dashscope-intl.aliyuncs.com/compatible-mode/v1`. Press `enter`.
6. Pick a branch / worktree / permissions, confirm, and the tmux session starts with `qwen --yolo` (when skip-permissions is selected) and the three OpenAI-compatible env vars exported.

The **LLM Gateway** path bypasses this step; `BuildLLMGatewayEnv("qwen", …)` injects the gateway-derived `OPENAI_API_KEY` and `OPENAI_BASE_URL` instead.

## Custom providers

You can add entries under `providers:` in `config.yaml` with:

- `name`, `binary`
- `launch_template` (Go text template with fields such as `Binary`, `SkipPermissions`)
- Optional `env`, `session_file`, `default`

Defaults from the built-in set are merged with your file; see the source `DefaultConfig()` in `internal/vibeflowcli/config.go` for the canonical templates.

## Next steps

- [Session wizard](session-wizard.md)
- [Configuration](configuration.md)
