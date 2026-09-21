# Routing matrix

Which coding agent can use which routing mode, and what each one is verified against.

!!! note "Generated file"
    This page is generated from `RoutingMatrix` in `internal/vibeflowcli/routing_matrix.go`.
    Regenerate it with `go test ./internal/vibeflowcli -run TestRoutingMatrixDoc -update`.
    Editing it by hand will be overwritten, and CI fails when it drifts from the code.

## Routing modes

| Mode | What it does |
|---|---|
| `direct` | The agent talks to its own provider with your subscription, OAuth or API key. |
| `gateway` | Requests route through the Axiom Studio AI Gateway for observability, cost tracking and governance. |
| `endpoint` | The agent is pointed at a compatible endpoint you supply. |
| `shell` | The agent uses the endpoint already exported in your shell. |

## Support

| Agent | direct | gateway | endpoint | shell |
|---|---|---|---|---|
| `claude` | yes | yes | yes | yes |
| `codex` | yes | yes | yes | yes |
| `copilot` | yes | gap (#5330) | yes | yes |
| `cursor` | yes | no | no | no |
| `gemini` | yes | yes | yes | yes |
| `kiro` | yes | no | no | no |
| `qwen` | yes | yes | yes | yes |

Yes means wired and covered by tests. No means the agent cannot do it and the wizard says why. Gap means it should work and is not wired yet; the ticket is below.

## Gaps

- **`copilot` / `gateway`** (#5330) — BuildLLMGatewayEnv has no copilot case, so the option would set nothing and silently run direct. Copilot BYOK is COPILOT_PROVIDER_BASE_URL + COPILOT_PROVIDER_TYPE=openai, so pointing it at the gateway should work.

## Not supported by design

- **`cursor` / `gateway`** — Cursor Agent connects only to its own backend
- **`cursor` / `endpoint`** — Cursor Agent connects only to its own backend
- **`cursor` / `shell`** — Cursor Agent connects only to its own backend
- **`kiro` / `gateway`** — Kiro authenticates with its own KIRO_API_KEY against its own backend
- **`kiro` / `endpoint`** — Kiro authenticates with its own KIRO_API_KEY against its own backend
- **`kiro` / `shell`** — Kiro authenticates with its own KIRO_API_KEY against its own backend

These are shown in the wizard with the reason rather than hidden, so the mode is never silently missing.

## What each supported mode sets

`Blanked` variables are the leak guards: a pane inherits the tmux server's environment, so a variable exported in your shell is cleared explicitly rather than merely left unset.

| Agent | Mode | Wire format | Set | Blanked | Live-verified |
|---|---|---|---|---|---|
| `claude` | `direct` | — | — | `ANTHROPIC_CUSTOM_HEADERS`, `ANTHROPIC_BASE_URL` | not yet |
| `claude` | `gateway` | Anthropic-compatible, Messages API | `ANTHROPIC_CUSTOM_HEADERS`, `ANTHROPIC_BASE_URL` | — | not yet |
| `claude` | `endpoint` | Anthropic-compatible, Messages API | `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_MODEL`, `ANTHROPIC_DEFAULT_HAIKU_MODEL`, `ANTHROPIC_DEFAULT_SONNET_MODEL`, `ANTHROPIC_DEFAULT_OPUS_MODEL`, `ANTHROPIC_DEFAULT_FABLE_MODEL` | `ANTHROPIC_API_KEY`, `ANTHROPIC_CUSTOM_HEADERS` | not yet |
| `claude` | `shell` | Anthropic-compatible, Messages API | `ANTHROPIC_BASE_URL` | — | not yet |
| `codex` | `direct` | — | — | `OPENAI_BASE_URL` | not yet |
| `codex` | `gateway` | OpenAI-compatible | `GATEWAY_API_KEY`, `OPENAI_BASE_URL` | — | not yet |
| `codex` | `endpoint` | OpenAI-compatible, Responses API | `OPENAI_API_KEY` | `OPENAI_BASE_URL` | not yet |
| `codex` | `shell` | OpenAI-compatible, Responses API | `OPENAI_API_KEY` | `OPENAI_BASE_URL` | not yet |
| `copilot` | `direct` | — | — | — | not yet |
| `copilot` | `endpoint` | OpenAI-compatible | `COPILOT_PROVIDER_BASE_URL`, `COPILOT_PROVIDER_TYPE`, `COPILOT_PROVIDER_API_KEY`, `COPILOT_MODEL` | `COPILOT_PROVIDER_BEARER_TOKEN`, `COPILOT_PROVIDER_WIRE_API` | not yet |
| `copilot` | `shell` | OpenAI-compatible | `COPILOT_PROVIDER_BASE_URL` | — | not yet |
| `cursor` | `direct` | — | — | — | not yet |
| `gemini` | `direct` | — | — | `GOOGLE_GEMINI_BASE_URL` | not yet |
| `gemini` | `gateway` | Gemini-compatible | `GEMINI_API_KEY`, `GOOGLE_GEMINI_BASE_URL` | — | not yet |
| `gemini` | `endpoint` | Gemini-compatible | `GOOGLE_GEMINI_BASE_URL`, `GEMINI_API_KEY` | — | not yet |
| `gemini` | `shell` | Gemini-compatible | `GOOGLE_GEMINI_BASE_URL` | — | not yet |
| `kiro` | `direct` | — | — | — | not yet |
| `qwen` | `direct` | — | — | — | not yet |
| `qwen` | `gateway` | OpenAI-compatible | `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `QWEN_CUSTOM_API_KEY_*` | — | not yet |
| `qwen` | `endpoint` | OpenAI-compatible | `OPENAI_BASE_URL`, `OPENAI_MODEL`, `OPENAI_API_KEY` | — | not yet |
| `qwen` | `shell` | OpenAI-compatible | `OPENAI_BASE_URL` | — | not yet |
