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
| `kiro` | Kiro CLI | `kiro-cli` | `--trust-all-tools` |
| `copilot` | GitHub Copilot CLI | `copilot` | `--yolo --autopilot --no-ask-user --max-autopilot-continues 1000` |

The **Cursor** provider uses the official Cursor CLI binary name **`agent`**, not `cursor`. Install the CLI from Cursor’s documentation if `agent` is not on your `PATH`.

**Qwen Code** is Alibaba's open-source coding agent, based on Google Gemini CLI with parser-level adaptations for Qwen-Coder models. Install with `npm install -g @qwen-code/qwen-code@latest`. The `--yolo` flag selects Qwen's `yolo` approval mode (full autonomous); the other modes (`default`, `plan`, `auto_edit`) are not exposed via the wizard in v1 — edit `~/.qwen/settings.json` or define a custom launch template if you need a middle-ground mode.

**Kiro CLI** ([kiro.dev/docs/cli](https://kiro.dev/docs/cli/)) is AWS's terminal agent, a companion CLI to the Kiro VS Code-based IDE. The `--trust-all-tools` flag pre-authorizes all tool calls — required for autonomous sessions since Kiro's non-interactive mode has no human to approve tool use. There is no documented `--model` flag for `kiro-cli chat`, so vibeflow-cli does not expose model selection for Kiro (unlike Claude/Codex/Cursor). See [Kiro CLI caveats](#kiro-cli-caveats) below for open questions and feature gaps versus other providers.

**GitHub Copilot CLI** ([github/copilot-cli](https://github.com/github/copilot-cli)) is GitHub's terminal agent, backed by the user's Copilot subscription.
Install with `npm install -g @github/copilot`.
Auth is GitHub login (`copilot login`) or a `COPILOT_GITHUB_TOKEN`/`GH_TOKEN`/`GITHUB_TOKEN` env var — vibeflow-cli does no key handling for it.
Copilot is the one provider where permissions and autonomy are **separate flags**, so an autonomous session needs four of them rather than one (verified on v1.0.80).
`--yolo` expands to `--allow-all-tools --allow-all-paths --allow-all-urls`, but it grants permissions only — GitHub's docs are explicit that "with `--allow-all`, you are still in the normal interactive flow", so a `--yolo`-only session runs every tool without prompting and then goes idle at the prompt as soon as it finishes a turn.
`--autopilot` adds the continuation loop that makes the agent resume after end-of-turn, which is what keeps a vibeflow session returning to `wait_for_work`.
`--no-ask-user` drops Copilot's `ask_user` tool, since nobody is watching an unattended pane — vibeflow agents raise questions through the `prompt_user` MCP tool instead.
`--max-autopilot-continues` caps continuations and **defaults to 5**, which would stop a session that is meant to poll indefinitely, so vibeflow-cli raises it to 1000. Each continuation spends Copilot AI credits with no human in the loop; lower it in `~/.vibeflow-cli/config.yaml` if you want a tighter budget, and note that vibeflow-cli never rewrites a `launch_template` you have already customised.
Model selection uses `--model`; `auto` works on every plan, while concrete slugs are plan-gated server-side.
vibeflow-cli pre-seeds `~/.copilot/config.json` (trusted folder + first-run nudge markers) at launch so unattended sessions never stall on Copilot's first-run dialogs, and sets `COPILOT_AUTO_UPDATE=false` so a mid-session self-update cannot break the tmux session.
The customer's Copilot org policy must allow Copilot CLI and MCP servers; every session turn consumes Copilot AI credits (premium requests).

## VibeFlow-integrated providers

**Claude**, **Cursor**, and **Copilot** are marked VibeFlow-integrated in the default config (session file templates align with autonomous flows). **Codex**, **Gemini**, **Qwen**, and **Kiro** remain available with their own launch templates; gateway and env behavior may differ by product.

## Prompt passing

VibeFlow init prompts are passed in the argument shape each CLI expects so the agent process stays running for the autonomous polling loop. The wizard and launch path pick this automatically:

- **Claude / Codex / Cursor / Kiro** — positional argument (`claude '<prompt>'`). These CLIs treat a positional prompt as the initial input and stay interactive. Kiro's shape is verified — see [Kiro CLI caveats](#kiro-cli-caveats).
- **Gemini** — `-p '<prompt>'` (non-interactive headless mode).
- **Qwen** — `-i '<prompt>'` (`--prompt-interactive`: execute the prompt and continue in interactive mode). Qwen's positional argument is **one-shot mode** (process the prompt, then exit) — wrong for vibeflow autonomous sessions, which need the agent to remain running.
- **Copilot** — `-i '<prompt>'` (`--interactive`: start interactive mode and auto-execute the prompt; verified on v1.0.79). Copilot's `-p/--prompt` is **one-shot mode** (exits after completion) and there is no positional prompt argument, so copilot must not use the default positional shape.

## Kiro CLI caveats

Kiro CLI was originally added without access to a real `kiro-cli` binary to test against, so several things were flagged rather than guessed. The interactive-mode assumption below has since been verified against a real binary; the rest remain open:

- **Interactive-mode assumption — verified.** vibeflow-cli assumes `kiro-cli chat '<prompt>'` (no extra flags) behaves like Claude/Codex/Cursor: takes the prompt as initial input and stays running. Confirmed against `kiro-cli` v2.15.2: a scripted session sent the initial prompt, received an answer, then sent a second distinct follow-up prompt in the same process and received a second answer — the CLI returns to its interactive composer after each turn rather than exiting. Also cross-checked against live vibeflow-launched Kiro sessions using this exact launch shape. `--no-interactive` remains a separate, confirmed **one-shot** mode (process the prompt, print a result, exit) — incompatible with vibeflow's persistent tmux session model, so it is deliberately **not** used in Kiro's launch template or prompt-passing shape.
- **No model-selection flag.** No `--model` (or similar) flag is documented for `kiro-cli chat`, so vibeflow-cli's wizard does not offer a Kiro model picker, unlike Claude/Codex/Cursor.
- **No LLM Gateway routing.** Kiro authenticates via its own `KIRO_API_KEY` env var (Pro-tier+ only, per its docs) with no documented OpenAI-compatible or custom-endpoint mechanism vibeflow's LLM Gateway could target — Kiro is intentionally left out of `BuildLLMGatewayEnv`/`ClearLLMGatewayEnv`.
- **MCP config scope.** `vibeflow bootstrap` writes to Kiro's **user-level** `~/.kiro/settings/mcp.json`. Kiro also supports a **workspace-level** `.kiro/settings/mcp.json` per project (`kiro-cli mcp add --scope workspace`), which vibeflow-cli does not write — not implemented in this pass.
- **Unattended-launch gotcha: `--trust-all-tools` shows a one-time interactive confirmation banner.** The first time `--trust-all-tools` is used, Kiro prints a warning ("Kiro is running in trust all tools mode...") and blocks on an interactive menu (`No, exit` / `Yes, I accept` / `Yes, and don't ask again`) before proceeding. This is Kiro's own safety gate, independent of vibeflow. Because vibeflow-cli launches Kiro inside tmux with no TTY interaction, a VibeFlow-mode Kiro session on a machine that has never answered this prompt will **hang indefinitely** — no timeout, no error, just stuck on an unanswerable menu. Workaround (verified): Kiro persists the "don't ask again" choice as its own setting, which can be set ahead of time —
  ```
  kiro-cli settings chat.disableTrustAllConfirmation true
  ```
  `vibeflow bootstrap --agent kiro` does **not** set this automatically today; it is currently a manual one-time prerequisite per machine, not something vibeflow-cli's bootstrap wires up.
- **Not currently used by vibeflow-cli** (informational, no behavior implied): `KIRO_HOME` (env var overriding `~/.kiro` — could matter for future per-session/per-persona state isolation, similar to the Rooms feature's per-participant `CLAUDE_CONFIG_DIR` pattern) and `--effort <low|medium|high|xhigh|max>` (a `kiro-cli chat` reasoning-effort flag, not wired into Kiro's `LaunchTemplate`).

### What Kiro CLI can't do that Claude Code / Codex CLI can

These are gaps in Kiro CLI itself (not vibeflow-cli integration gaps), based on kiro.dev's docs:

- **No *documented* persistent interactive session seeded by a positional prompt** — Claude Code, Codex CLI, and Cursor Agent all document this shape explicitly; Kiro's own docs only document one-shot `--no-interactive`. vibeflow-cli has since verified `kiro-cli chat '<prompt>'` behaves this way in practice (see [Kiro CLI caveats](#kiro-cli-caveats)), so this is a documentation gap in Kiro's own docs, not an open question for vibeflow-cli's integration.
- **No mid-session prompt injection in `--no-interactive` mode.** That one-shot mode takes one prompt upfront and cannot receive further input during the run — Claude Code and Codex CLI both support ongoing interactive input when run without a one-shot flag. (Not relevant to vibeflow-cli's launch shape, since `--no-interactive` is deliberately never used — see above.)
- **No scoped/tag-based tool trust in v1 vibeflow wiring.** Kiro documents a `--trust-tools=<categories>` flag for granular pre-authorization (e.g. `read`, `grep`); vibeflow-cli's `SkipPermissions` toggle only maps to the blanket `--trust-all-tools`, same coarseness as Claude's `--dangerously-skip-permissions` and Codex's `--yolo` — not a Kiro-specific gap, but worth knowing the finer-grained flag exists if you hand-edit a custom launch template.
- **No MCP server hosting.** Kiro CLI is MCP **client-only** (connects out to MCP servers, including vibeflow's). Neither Claude Code nor Codex CLI expose themselves as MCP servers either, so this is not a relative gap — noted only because Kiro's own IDE product does more (see below).

### What's IDE-only (Kiro's VS Code product) vs CLI

Kiro's primary product is a VS Code-based IDE; the CLI is a companion, and some capabilities described in Kiro's docs appear to be authored through the IDE's GUI rather than the CLI:

- **Specs** (`requirements.md` / `design.md` / `tasks.md` structured planning artifacts) are documented as created via a `+` button in the IDE's Kiro pane. Kiro's docs describe specs as available in both IDE and CLI conceptually, but do not document a CLI-driven spec creation/review workflow — none of this matters for vibeflow-cli's integration (vibeflow only launches the CLI process), but it means Kiro users accustomed to spec-driven planning in the IDE won't get that workflow through a vibeflow-launched Kiro session.
- **Visual multi-file diff review, autonomy-mode toggles, and chat history UI** are IDE-native concepts in Kiro's docs with no CLI equivalent described — a vibeflow-launched Kiro CLI session is a plain terminal chat, same tradeoff as every other provider here (none of Claude Code, Codex CLI, Cursor, Gemini CLI, or Qwen Code expose a GUI through vibeflow's tmux launch either, so this is not Kiro-specific, just worth naming since Kiro is unusual in having a full IDE sibling product).

## LLM Gateway

When enabled in config or the wizard, the CLI can set **per-provider environment variables** so traffic goes through your VibeFlow server’s LLM gateway (where supported). Routing for Cursor may evolve; if gateway env mapping is empty for a provider, the CLI leaves gateway vars unset for that agent.

**Qwen Code** uses the OpenAI-compatible env vars (`OPENAI_API_KEY`, `OPENAI_BASE_URL`) — same wiring as Codex and Gemini — plus a `QWEN_CUSTOM_API_KEY_{PROTOCOL}_{ENCODED_ENDPOINT}` variable that binds the gateway endpoint via qwen-code's custom-API-key mechanism (the variable's *name* encodes the protocol and endpoint URL, e.g. `QWEN_CUSTOM_API_KEY_OPENAI_HTTPS_API_Z_AI_API_PAAS_V4` for `https://api.z.ai/api/paas/v4`; its *value* is the bearer token). In gateway mode the wizard still shows the **Qwen launch config** step so you can pick the model the gateway routes to (`OPENAI_MODEL`, e.g. `glm-4.6` for z.ai); the endpoint and key fields come from the gateway, so the base URL input is ignored there. For headless `vibeflow launch` and session restarts, export `OPENAI_MODEL` in your shell — it is passed through to the session and mirrored onto qwen's `--model` flag. Note that the `qwen` CLI auto-loads `.env` files from the current working directory and `~/.qwen/.env` at startup. If you have `OPENAI_BASE_URL` set in either of those, it can interact with the value the wizard sets for the tmux process: process-level env normally takes precedence, but users running mixed direct/gateway setups should double-check that the gateway is actually being used (e.g. by checking the request URL in the gateway server logs). Qwen also supports DashScope, Anthropic, Gemini, Ollama, vLLM, and BailianCoding auth modes for direct use; these are not touched by the gateway wiring — you own the corresponding env vars (`DASHSCOPE_API_KEY` etc.).

## Qwen launch config (API-key mode)

When you launch a qwen session **without** the LLM Gateway, the wizard inserts a dedicated **Qwen launch config** step that captures the OpenAI-compatible launch environment for the tmux process:

| Env var | Source |
|---|---|
| `OPENAI_API_KEY` | `StepEnvToken` (saved → shell → prompt). Persists in `cfg.SavedEnvVars`. |
| `OPENAI_BASE_URL` | `StepQwenLaunchConfig` vendor preset, editable. |
| `OPENAI_MODEL` | `StepQwenLaunchConfig` vendor preset, editable. |

The step is **skipped** for any other provider, and for qwen when the session routes to a compatible endpoint or to an endpoint detected in the shell (see [Routing](#routing)). For qwen it also runs when the LLM Gateway is enabled, but in that mode only the **model** selection is committed (`OPENAI_MODEL`) — the gateway provides its own `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `QWEN_CUSTOM_API_KEY_*` endpoint binding, so the base URL input is ignored.

### Vendor presets

| Vendor | Model | Base URL |
|---|---|---|
| OpenAI | `gpt-4o-mini` | `https://api.openai.com/v1` |
| Qwen (DashScope) | `qwen3-coder-plus` | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` |
| z.ai | `glm-4.6` | `https://api.z.ai/api/coding/paas/v4` |
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

On the **LLM Gateway** path, `BuildLLMGatewayEnv("qwen", …)` injects the gateway-derived `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and the `QWEN_CUSTOM_API_KEY_*` endpoint binding instead; the launch-config step still runs there, but only to capture the model (see [LLM Gateway](#llm-gateway)).

## Routing

After you pick a harness, the wizard's **Routing** step ("Configure routing for your coding agent") decides how it reaches its model. Headless launches use `--routing`.

| Option | `--routing` | When it is offered |
|---|---|---|
| **Axiom Studio AI Gateway** | `gateway` (or `--llm-gateway`) | VibeFlow sessions with an API token, on harnesses the gateway supports (Claude Code, Codex, Gemini CLI, Qwen Code). Copilot, Cursor and Kiro show the option disabled with the reason. See [LLM Gateway](#llm-gateway). |
| **Use detected endpoint** | `shell` | The harness's endpoint variable is already set in your shell. See [Detected endpoint](#detected-endpoint). |
| **Connect directly to the provider** | `direct` | Always. The harness uses its own login or provider API key. |
| **Connect to a compatible endpoint** | `endpoint` | Copilot, Qwen Code, Codex, Claude Code and Gemini CLI. Shown disabled for Cursor and Kiro, which have no way to point at a custom endpoint. |

Without `--routing`, headless launches behave as before: `--llm-gateway` or the saved gateway preference where the harness supports it, otherwise direct.

### Compatible endpoint

Connects the harness to any server that speaks the API it needs: a hosted API, or a self-hosted proxy or inference server such as LiteLLM or vLLM.

| Harness | Endpoint must be | How vibeflow-cli points it at the endpoint |
|---|---|---|
| GitHub Copilot CLI | OpenAI-compatible (chat completions) | Copilot's BYOK variables: `COPILOT_PROVIDER_BASE_URL`, `COPILOT_PROVIDER_TYPE=openai`, `COPILOT_PROVIDER_API_KEY`, `COPILOT_MODEL`. No GitHub login is needed. |
| Qwen Code | OpenAI-compatible (chat completions) | `OPENAI_BASE_URL`, `OPENAI_MODEL`, `OPENAI_API_KEY`, plus `--auth-type openai --openai-base-url … --model …` |
| Codex CLI | OpenAI-compatible (Responses API) | A temporary model provider via `-c model_provider=…` flags; the key is read from `OPENAI_API_KEY` (`env_key`) |
| Claude Code | Anthropic-compatible (Messages API) | `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, and every model tier (`ANTHROPIC_MODEL`, `ANTHROPIC_DEFAULT_*_MODEL`) set to your model |
| Gemini CLI | Gemini-compatible | `GOOGLE_GEMINI_BASE_URL`, `GEMINI_API_KEY` |

The wizard asks for:

| Input | Required | Notes |
|---|---|---|
| Base URL | yes | Absolute `http://` or `https://` URL of the API, usually ending in `/v1`. Claude Code and Gemini CLI add their own versioned path, so vibeflow-cli gives them the URL without a trailing `/v1`; enter the same URL for every harness. |
| Model | yes | Model id exactly as the endpoint expects it. |
| Vendor | no | Display label (e.g. `my-proxy`). When set, it also names the slot the API key is stored in. |
| API key | no | Leave blank for endpoints without authentication. |

Headless:

```bash
vibeflow launch --provider copilot --routing endpoint \
  --base-url http://<host>:4000/v1 --model <model-name> [--vendor <label>]
```

`--routing endpoint` requires `--base-url` and `--model` (all missing flags are listed at once). `--base-url` and `--vendor` are rejected with any other routing, and `--routing endpoint` is rejected for Cursor and Kiro.

**API key storage.** With a vendor, the key is stored in `saved_env_vars` under `OPENAI_COMPAT_API_KEY_<VENDOR>`: the vendor uppercased, with each run of other characters turned into `_` (`my-proxy.local` → `OPENAI_COMPAT_API_KEY_MY_PROXY_LOCAL`). Without a vendor, it uses `OPENAI_COMPAT_API_KEY`. An exported shell variable of that name takes precedence over the saved value. The harness's own key variables are always set explicitly for the session, so a key exported in your shell (such as `OPENAI_API_KEY` or `ANTHROPIC_API_KEY`) is never sent to the endpoint. For Claude Code the configured token is always sent, never your Claude subscription login. The key is passed through the environment only, never on the command line, and is redacted in logs.

Keys entered in the wizard are saved in plaintext in `~/.vibeflow-cli/config.yaml` (file mode `0600`). To keep a key off disk, leave the wizard field blank and export the variable from your shell or secrets manager instead; exported keys are never written to the config.

**Base URL rules.** The base URL must not contain credentials (`user:password@host`), a query string or a fragment. It appears on some agents' command lines, which other local users can read, and in logs and saved metadata. Put credentials in the API key field or the key variable.

**Endpoints without a key.** With no key set, Qwen Code, Codex, Claude Code and Gemini CLI get the placeholder `no-key`, which servers without authentication ignore. Qwen Code refuses to start without a key, and for Claude Code the placeholder also keeps your subscription login from being sent. Copilot is started without a key.

**Models and tools.** Coding agents rely on tool calling, so the model behind the endpoint must support it (Copilot also needs streaming). Small local models often don't; pick one your server documents as tool-capable.

**Restart.** Routing, vendor, base URL and model are stored in the session metadata (`routing`, `vendor`, `base_url` and `model` in `sessions.json`; never the key). Kill/restart reconnects to the same endpoint and re-reads the key.

#### Use with LiteLLM / vLLM

A LiteLLM proxy serves the OpenAI chat, OpenAI Responses and Anthropic Messages routes, so one proxy works for Copilot, Qwen Code, Codex and Claude Code:

```bash
export OPENAI_COMPAT_API_KEY_LITELLM=<proxy-key>   # if the proxy has a master key
vibeflow launch --provider copilot --routing endpoint \
  --base-url http://<host>:4000/v1 --vendor litellm --model <model-name> --skip-permissions
```

Swap `--provider` for `qwen`, `codex` or `claude` to use another harness against the same proxy. vLLM serves the OpenAI chat route. Use its server URL (commonly `http://<host>:8000/v1`) and the served model name with Copilot or Qwen Code. In the TUI wizard, pick the harness, choose **Connect to a compatible endpoint** on the Routing step, and enter the same values.

### Detected endpoint

If the harness's endpoint variable is already set in the environment you launch from, the Routing step offers **Use detected endpoint**, pre-selected, and shows the URL (without credentials, never the key):

| Harness | Variable | Also passed through when set |
|---|---|---|
| Claude Code | `ANTHROPIC_BASE_URL` | `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`, `ANTHROPIC_CUSTOM_HEADERS`, model variables |
| GitHub Copilot CLI | `COPILOT_PROVIDER_BASE_URL` | the other `COPILOT_PROVIDER_*` variables, `COPILOT_MODEL` |
| Codex CLI | `OPENAI_BASE_URL` | `OPENAI_API_KEY` (Codex ignores `OPENAI_BASE_URL` itself, so the URL is applied with the same model provider flags as a compatible endpoint) |
| Qwen Code | `OPENAI_BASE_URL` | `OPENAI_API_KEY`, `OPENAI_MODEL` (the Qwen launch config step is skipped) |
| Gemini CLI | `GOOGLE_GEMINI_BASE_URL` | `GEMINI_API_KEY` |

These values are passed to the session explicitly, so it uses what your shell has now. **Connect directly to the provider** ignores the detected endpoint: Claude Code, Codex and Gemini CLI get it cleared as before, and Copilot gets `COPILOT_PROVIDER_BASE_URL` cleared so it uses your GitHub subscription. A detected URL with credentials, a query string or a non-http scheme is shown but can't be selected.

**Claude Code login.** With only `ANTHROPIC_BASE_URL` set (no `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY`), Claude Code signs in with your Claude subscription and sends that login to the detected URL. In that case the option is not pre-selected, and both it and the confirm screen say so. Headless `--routing shell` prints a warning. Unlike a compatible endpoint, shell routing does not replace your credentials. Gemini CLI is not affected: every launch path requires `GEMINI_API_KEY`, so its Google login is not used.

Headless, `--routing shell` does the same and fails if the variable isn't set. Restarting a shell-routed session also fails, before launching, if the variable is gone. Without `--routing`, nothing changes.

### VibeFlow sessions

Whatever the routing, the agent reaches VibeFlow through the VibeFlow MCP server in its own config. Run `vibeflow bootstrap --all --api-key <key>` once per machine, or target the harness with `--agents`. Without it, a VibeFlow-mode session starts but the agent has no VibeFlow tools. Vanilla sessions don't need it.

## Custom providers

You can add entries under `providers:` in `config.yaml` with:

- `name`, `binary`
- `launch_template` (Go text template with fields such as `Binary`, `SkipPermissions`, `Model`; use `{{ shellQuote .Model }}` when rendering shell arguments)
- Optional `env`, `session_file`, `default`

Defaults from the built-in set are merged with your file; see the source `DefaultConfig()` in `internal/vibeflowcli/config.go` for the canonical templates.

The built-in model catalog is advisory. Use `vibeflow models` or `vibeflow models <provider>` to list known ids, but `--model` / `--models` accept explicit strings so new provider models work before the catalog is updated.

## MCP tool name

The VibeFlow init prompt sent to agents references the MCP server by tool name (default: `vibeflow`). If you run a renamed or forked MCP server, override the tool name so the init prompt generates correct `mcp__<name>__*` tool calls:

- **CLI flag**: `--mcp <name>` (e.g. `vibeflow --mcp my-vibeflow launch`)
- **Config file**: `mcp_tool_name: my-vibeflow` in `~/.vibeflow-cli/config.yaml`

The chosen value persists on the session record so `vibeflow restart` re-uses the original launch's MCP name. If neither is set, the default (`vibeflow`) is used.

## OpenShell sandboxes

vibeflow-cli can wrap any provider command in NVIDIA OpenShell so the agent runs inside a policy-enforced sandbox. Enable it per launch:

```bash
vibeflow launch --provider codex --skip-permissions \
  --openshell \
  --openshell-sandbox vf-main \
  --openshell-from ghcr.io/nvidia/openshell-community/sandboxes/base \
  --openshell-policy ./policy.yaml
```

The generated command shape is:

```bash
openshell sandbox create --name <sandbox> --keep [options] -- sh -lc '<agent command>'
```

Config-file equivalent:

```yaml
openshell:
  enabled: true
  binary: openshell
  mode: create
  sandbox: vf-main
  from: ghcr.io/nvidia/openshell-community/sandboxes/base
  policy: ./policy.yaml
  providers:
    - openai
    - github
  no_auto_providers: false
  keep: true
  args: []
```

Supported modes:

- `create` (default) uses `openshell sandbox create` and passes the final provider command after `--`.
- `use` is an advanced escape hatch for OpenShell installs that provide `openshell sandbox use <name> -- ...`; set `sandbox` and any extra `args` in config.

When enabled, restart metadata stores the OpenShell settings so `vibeflow restart` uses the same sandbox wrapper.

## Next steps

- [Session wizard](session-wizard.md)
- [Configuration](configuration.md)
