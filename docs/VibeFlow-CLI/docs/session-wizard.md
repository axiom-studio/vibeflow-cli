# Session wizard

Press **`n`** in the TUI (or use headless `vibeflow launch` with flags) to create a session. The wizard guides you through the main decisions in order.

## Steps (typical flow)

1. **Working directory** — Pick from history or enter a new path. The history list is filtered: paths that no longer exist, or that are no longer inside a git work tree, are removed automatically so stale entries don't surface as selectable options.
2. **Session type** — **Vanilla** (standalone agent) or **VibeFlow** (server-connected).
3. **Project** — Choose a VibeFlow project (VibeFlow mode; requires API reachability).
4. **Persona** — Single or **multi-select** team personas (VibeFlow mode). Code agents (`developer`, `principal_engineer`, `architect`) are radio-button mutually exclusive; review/support personas are free checkboxes. See [VibeFlow server & personas](vibeflow-server.md).
5. **Provider** — Claude, Codex, Gemini, Cursor, Qwen, Kiro, Copilot, or other configured providers; unavailable binaries are marked. **Team mode** (multiple personas) opens a per-persona × provider matrix instead of a single list — see below.
6. **API / tokens** — Prompt for missing credentials the harness always needs (e.g. the Codex MCP token). A missing provider API key (`GEMINI_API_KEY`, `OPENAI_API_KEY`) is asked only after you choose direct routing, or a detected endpoint that relies on it.
7. **Routing** — "Configure routing for your coding agent": **Axiom Studio AI Gateway** (VibeFlow sessions with an API token; shown disabled with the reason on harnesses that cannot use it), **Use detected endpoint** (when the harness's endpoint variable is set in your shell; pre-selected), **Connect directly to the provider** (subscription, OAuth or an API key from your shell), or **Connect to a compatible endpoint** (says what the endpoint must be compatible with; disabled for Cursor and Kiro). See [Providers — Routing](providers.md#routing).
8. **Qwen launch config** — _(Qwen, direct or gateway routing)_ Captures OpenAI-compatible environment for the tmux process: `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL`. Includes vendor presets (OpenAI, DashScope, z.ai). With the gateway only the model is used. See [Providers — Qwen launch config](providers.md#qwen-launch-config-api-key-mode) for details.
   **Compatible endpoint** — _(Connect to a compatible endpoint only)_ Four inputs: **Base URL**, **Vendor** (optional label), **Model** and an optional **API key**. `↑`/`↓`/`tab` move between rows (letters are always typed, never used for navigation); `enter` moves to the next row and, on the API key row, validates and continues. Validation errors appear inline: the base URL must be an `http(s)` URL with a host and no credentials, query string or fragment, and the model must not be empty. The key is masked as you type and saved in the vendor's slot (or the default slot without a vendor); a blank key keeps any key already saved there. The inputs start empty; your last endpoint is offered below them (named with the harness it was used with) and `ctrl+r` fills it in. `esc` returns to Routing. See [Providers — Compatible endpoint](providers.md#compatible-endpoint).
9. **Branch** — Select an existing branch or create a new one. The current `HEAD` branch is auto-detected and pre-selected, with a `← current` annotation. Creating a new branch prompts for a **base branch** (defaults to `main`) so you don't accidentally fork from the wrong branch. If you type a name that matches a remote branch, the CLI **tracks** the remote instead of creating a divergent local branch.
10. **Worktree** — Stay in repo root, create a new worktree, or pick a custom path (see [Worktrees & session files](worktrees-session-files.md)).
11. **Permissions** — Whether to enable **autonomous** / skip-permissions style flags for the provider.
12. **Confirm** — Review and launch. The summary shows the routing. For a compatible endpoint it also shows vendor, base URL, model and whether a key will be sent; for a detected endpoint it shows the URL and its variable. It never shows the key.

Exact labels and ordering match your installed version; the list above reflects the intended product flow.

## Vera PR review runner

With `vibeflow --cra`, the persona picker also offers **Vera · Code Reviewer**.
Selecting only Vera uses the same wizard with fewer steps: **Directory > Type > Project > Team > Provider > Confirm**.
Env, Routing, Qwen, Endpoint, Branch, Worktree and Permissions are skipped because Vera always runs in its own disposable worktree with the harness's full-permission mode.
The **Provider** step is the ordinary provider list; harnesses Vera cannot run are shown dimmed and cannot be selected, and uninstalled ones show **(not installed)**.
Vera can run Claude, Codex, Copilot, Cursor, Gemini, Kiro or Qwen, in normal mode like the other personas: your own login, environment, MCP servers and credentials, with full permissions.
Codex works with a ChatGPT subscription login as well as an API key.
There is no model question: Vera uses the harness default model.
The **Confirm** step shows the project, checkout, harness and "harness default" model, and states that Vera listens for `@vibeflow review` on the linked repository until this CLI closes and runs with your login and full permissions in a disposable worktree.
Pressing Enter starts Vera directly after checking the checkout against the project's linked repositories.
Only when that check needs input, because several repository links match or the checkout does not match any link, a small Vera popup asks for that one choice.
With the LLM gateway enabled, that popup also asks for the review model, because gateway reviews require an explicit model.
Vera listens for repository review requests while idle and starts a fresh review for each assigned PR.
Each review runs in its own disposable git worktree of the PR head, never in your checkout, and the worktree and all review files are deleted when the review ends.
The `R` review-runner view shows listening, running, or failed status, and closing the TUI stops only its owned runners.
Selecting Vera with coding personas keeps the full wizard for the coding personas.
On the team **Provider** step, Vera's row uses its own override or the team default, and cycles only through harnesses Vera can run; the step does not continue while Vera's row shows a harness it cannot run.
On confirm, Vera starts with that harness and then the coding agents launch with their own settings and overrides.
If Vera cannot start, the error is shown and the coding agents still launch.
The selection grants consent only for this invocation and repository, without changing saved coding-agent configuration.
Headless `launch --persona code_reviewer` is intentionally rejected with an explicit `review-watch` command instead of starting a coding-agent loop.

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
