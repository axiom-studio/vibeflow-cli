# Session wizard

Press **`n`** in the TUI (or use headless `vibeflow launch` with flags) to create a session. The wizard guides you through the main decisions in order.

## Steps (typical flow)

1. **Working directory** — Pick from history or enter a new path. The history list is filtered: paths that no longer exist, or that are no longer inside a git work tree, are removed automatically so stale entries don't surface as selectable options.
2. **Session type** — **Vanilla** (standalone agent) or **VibeFlow** (server-connected).
3. **Project** — Choose a VibeFlow project (VibeFlow mode; requires API reachability).
4. **Persona** — Single or **multi-select** team personas (VibeFlow mode). Code agents (`developer`, `principal_engineer`, `architect`) are radio-button mutually exclusive; review/support personas are free checkboxes. See [VibeFlow server & personas](vibeflow-server.md).
5. **Provider** — Claude, Codex, Gemini, Cursor, Qwen, OpenAI Compatible, or other configured providers; unavailable binaries are marked. **Team mode** (multiple personas) opens a per-persona × provider matrix instead of a single list — see below.
6. **API / tokens** — Prompt for missing credentials (e.g. Codex/Gemini keys) when needed.
7. **LLM Gateway** — Optional: route via server gateway when your org uses it.
8. **Qwen launch config** — _(Qwen provider, non-gateway only)_ Captures OpenAI-compatible environment for the tmux process: `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL`. Includes vendor presets (OpenAI, DashScope, z.ai). Skipped for other providers and for qwen when the LLM Gateway is enabled. See [Providers — Qwen launch config](providers.md#qwen-launch-config-api-key-mode) for details.
   **Endpoint** — _(OpenAI Compatible provider only)_ Four inputs: **Base URL**, **Vendor**, **Model** and an optional **API key**. `↑`/`↓`/`tab` move between rows (letters are always typed, never used for navigation); `enter` moves to the next row and, on the API key row, validates and continues. Validation errors appear inline: the base URL must be an `http(s)` URL with a host and no credentials, query string or fragment, the vendor needs at least one letter or digit, and the model must not be empty. The key is masked as you type, saved for that vendor only, and a blank key keeps any key already saved for the vendor. Base URL, vendor and model are prefilled from your last use. The LLM Gateway step is never shown for this provider. See [Providers — OpenAI Compatible](providers.md#openai-compatible).
9. **Branch** — Select an existing branch or create a new one. The current `HEAD` branch is auto-detected and pre-selected, with a `← current` annotation. Creating a new branch prompts for a **base branch** (defaults to `main`) so you don't accidentally fork from the wrong branch. If you type a name that matches a remote branch, the CLI **tracks** the remote instead of creating a divergent local branch.
10. **Worktree** — Stay in repo root, create a new worktree, or pick a custom path (see [Worktrees & session files](worktrees-session-files.md)).
11. **Permissions** — Whether to enable **autonomous** / skip-permissions style flags for the provider.
12. **Confirm** — Review and launch. For OpenAI Compatible sessions the summary shows vendor, base URL, model and whether a key will be sent — never the key itself.

Exact labels and ordering match your installed version; the list above reflects the intended product flow.

## Multi-persona launch

When multiple personas are selected, the CLI spawns **one session per persona** so parallel agents share the same repository context with **isolated session files** (`.vibeflow-session-<persona>`).

### Per-persona provider selection (team mode)

In team mode the **Provider** step renders a per-persona matrix instead of a single list:

- A **Team default** row at the top sets the fallback for every persona.
- One row per selected persona below; each row resolves to either an explicit override or **`(team default)`** (rendered dim).
- Keys: `j` / `k` move between rows, `←` / `→` (or `h` / `l`) cycle the focused row's provider while skipping uninstalled binaries, `r` resets the focused row to inherit from the team default, `enter` advances, `esc` goes back.

The Confirm screen replaces the single `Provider:` line with a `Providers:` block listing the resolved provider per persona. At launch, an override naming a non-configured or uninstalled provider surfaces an actionable error before any tmux session is created.

## Next steps

- [Interactive TUI](tui.md)
- [Providers](providers.md)
