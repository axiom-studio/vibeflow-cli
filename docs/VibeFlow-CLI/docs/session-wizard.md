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

## Vera PR review session

With `vibeflow --cra`, the persona picker also offers **Vera · Code Reviewer**.
This is the only way to run Vera in the TUI; `--cra` starts straight into the session list with no startup prompt.
Selecting only Vera uses the same wizard with fewer steps: **Directory > Type > Project > Team > Provider > Confirm**.
Env, Routing, Qwen, Endpoint, Branch, Worktree and Permissions are skipped because each review runs in its own disposable worktree with the harness's full-permission mode.
The **Provider** step is the ordinary provider list; harnesses Vera cannot run are shown dimmed and cannot be selected, and uninstalled ones show **(not installed)**.
Vera can run Claude, Codex, Copilot, Cursor, Gemini, Kiro or Qwen, in normal mode like the other personas: your own login, environment, MCP servers and credentials, with full permissions.
Codex works with a ChatGPT subscription login as well as an API key.
There is no model question: Vera uses the harness default model.
The **Confirm** step shows the project, checkout, harness and "harness default" model, and states that Vera runs in its own tmux session, listening for `@vibeflow review` on the linked repository until you delete the session.
Pressing Enter checks the checkout against the project's linked repositories and then creates the session.
Only when that check needs input, because several repository links match or the checkout does not match any link, a small Vera popup asks for that one choice.
With the LLM gateway enabled, that popup also asks for the review model, because gateway reviews require an explicit model.

Each Vera session serves exactly one project, repository link and checkout; start one Vera session per repository.
It is an ordinary tmux session in the session list, like every other persona: attach with Enter, detach, delete with `d`, and it keeps running after the TUI exits.
Its row shows **Vera · Code Reviewer · <repository>**, the project and the listener's state: `listening`, `reviewing PR #N` or `stopped`; the detail panel keeps the session name.
The session window is split in two.
The left pane, about 70% wide and focused on attach, runs the foreground listener, `vibeflow --cra --root <root> --config <config> review-watch --project <id> --repo <checkout> --repository-link <id> --git-provider <provider> --provider <harness> --name <runner name>`, with `--model` only when a model was chosen.
The right pane, about 30% wide, runs the same command with `--history`: a read-only list of the PRs Vera reviewed on this repository, newest first, with outcome, round, findings, short head commit and time.
The PR under review now is marked with `▶` and the selected PR with `>`; the list refreshes every 10 seconds.
While Vera listens, its left pane does not echo keys: ↑/↓, j/k, PgUp/PgDn, Home/End, Enter and Esc typed there move and open the history list, and Ctrl-C still stops Vera.
The listener prints that hint, `↑/↓ browse reviews · Enter opens a review · Ctrl-C stops Vera`, each time it starts listening.
The history pane takes the same keys and the mouse directly; the Vera session turns tmux mouse support on for itself only, so a click selects a row and a click on the selected row opens it.
Enter opens the selected review in a tmux popup (90% wide, 85% high; tmux 3.2 or later, with an attached client) showing the PR title and link, outcome, rounds used of the limit, last reviewed head, summary and every finding with its state, severity, location, title, trigger, impact, evidence and verification.
The popup scrolls with the same keys and closes with q or Esc.
Without popups, the same view replaces the list inside the history pane until Esc.
The view states that the live harness transcript of past reviews is not stored: only the recorded result and findings are.
A review that is running now is visible in the left pane itself.
Past PR reviews are no longer rows in the session list; without a running Vera session for a repository, use AxiomCloud for its history.
No model runs while it listens.
When a review is claimed, the pane shows the PR number and head commit, prepares a fresh worktree of the PR head, and then runs the harness's own interactive UI in the pane, launched like a persona (its launch template with full permissions) with the review task as the initial prompt.
You can watch or type into the harness while it reviews.
Once the harness writes a complete `result.json`, Vera closes it (SIGINT, then SIGTERM, then SIGKILL), restores the terminal, prints the result (clean, changes requested with the number of new findings, or failed with its reason), removes the worktree, and returns to listening.
Before claiming any review, Vera runs the harness's login status command where one exists (Claude, Codex, Cursor and Kiro) and stops with the login command if the harness is not logged in.
If the harness exits within 15 seconds without a result, Vera fails that attempt once and stops with its exit code and a possible login or trust fix; the review stays queued for when Vera starts again.
If no result exists after 60 seconds, the pane says once that Vera is still waiting, in case the harness shows a login or trust screen to answer there.
Ctrl-C during a review goes to the harness; once it exits, Vera reports the attempt as cancelled and waits 5 seconds, so pressing Ctrl-C again stops Vera, and otherwise it resumes listening.
Deleting the session stops the listener gracefully: an in-flight review is reported as cancelled, its worktree is removed, and the runner deregisters.
Restarting the session re-runs the same listener command.
Choosing Vera again for a repository that already has a live Vera session attaches that session; choosing a different harness for it reports the running one instead, and a stopped session is replaced.
All Vera sessions in one root share `review_concurrency`, so at most that many harness reviews run at once; a session at the limit keeps listening without claiming work.
First-run prompts behave as they do for a persona in a new worktree: Claude's and Codex's folder-trust questions are pre-answered for the worktree in their own config and forgotten at cleanup, Gemini trusts the worktree through `GEMINI_CLI_TRUST_WORKSPACE`, Copilot's first-run dialogs are pre-seeded, Kiro uses `--trust-all-tools`, and Claude and Codex use their permission-bypass flags.
Cursor's `--trust` works only in headless mode, so interactive Cursor can still ask to trust the worktree.

Selecting Vera with coding personas keeps the full wizard for the coding personas.
On the team **Provider** step, Vera's row uses its own override or the team default, and cycles only through harnesses Vera can run; the step does not continue while Vera's row shows a harness it cannot run.
On confirm, Vera's session starts with that harness and then the coding agents launch with their own settings and overrides.
If Vera cannot start, the error is shown and the coding agents still launch.
The selection grants consent only for that session's repository, without changing saved coding-agent configuration.

With `--cra`, the **Edit Group** wizard (`e`) lists Vera too, with only its **Team > Provider > Confirm** steps.
A group includes the Vera session of its checkout whatever branch that session recorded, so Vera starts preselected when the checkout has one.
Ticking Vera starts it for the group's checkout and project through the same launch path as New Agent, including attaching a live Vera session or reporting its different harness.
Unticking Vera stops and removes its session like any removed persona.
Without `--cra`, Edit Group hides Vera and never stops a running Vera session.
Vera's Provider row in the edit is restricted to the harnesses Vera can run.
Pressing `e` on Vera's row edits the coding group of its checkout, inheriting settings from a coding session when there is one.
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
