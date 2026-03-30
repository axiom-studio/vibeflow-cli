# Advanced topics

## Error recovery

The CLI watches agent output for **known failure patterns** (rate limits, HTTP errors, etc.) and can send **recovery prompts** to the agent with **exponential backoff** and configurable **max retries** and **max backoff** caps. Ordering of patterns matters: specific status codes should be registered before generic wildcards so the right recovery message wins.

## LLM Gateway

Optional integration routes provider traffic through **`{serverURL}/rest/v1/llm-gateway`** (or your deployment’s equivalent). Enable via config or wizard. Per-provider environment variables (e.g. custom headers for Claude, OpenAI-compatible base URLs for Codex/Gemini) are applied when gateway mode is on; some providers may not have gateway mapping yet.

## Session cache & restart

A separate **`session_cache.json`** stores enough metadata to **restart** a session after tmux exits: provider, branch, VibeFlow init parameters, gateway flags, etc. On CLI startup, if cached sessions are **dead** (not in tmux), you may get a **multiselect** to restart them. Entries are garbage-collected periodically and removed on explicit kill/delete.

## Agent documentation embedding

On launch, the CLI can write **agent rule files** from embedded templates (`vibecoding-agent-docs/`) so each provider picks up project rules. Behavior differs when the **VibeFlow Claude plugin** is enabled—Claude may rely on plugin skills while Codex/Gemini still receive on-disk docs.

## Logging

Logs rotate at **1 MB** under `~/.vibeflow-cli/vibeflow-cli.log` for debugging CLI behavior (not a substitute for agent logs inside tmux).

## Next steps

- [Troubleshooting](troubleshooting.md)
- [Configuration](configuration.md)
