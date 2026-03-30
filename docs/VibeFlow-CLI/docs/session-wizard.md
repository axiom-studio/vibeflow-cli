# Session wizard

Press **`n`** in the TUI (or use headless `vibeflow launch` with flags) to create a session. The wizard guides you through the main decisions in order.

## Steps (typical flow)

1. **Working directory** — Pick from history or enter a new path.
2. **Session type** — **Vanilla** (standalone agent) or **VibeFlow** (server-connected).
3. **Project** — Choose a VibeFlow project (VibeFlow mode; requires API reachability).
4. **Persona** — Single or **multi-select** team personas (VibeFlow mode). See [VibeFlow server & personas](vibeflow-server.md).
5. **Provider** — Claude, Codex, Gemini, Cursor, or other configured providers; unavailable binaries are marked.
6. **API / tokens** — Prompt for missing credentials (e.g. Codex/Gemini keys) when needed.
7. **LLM Gateway** — Optional: route via server gateway when your org uses it.
8. **Branch** — Select existing branch or create a new one.
9. **Worktree** — Stay in repo root, create a new worktree, or pick a custom path (see [Worktrees & session files](worktrees-session-files.md)).
10. **Permissions** — Whether to enable **autonomous** / skip-permissions style flags for the provider.
11. **Confirm** — Review and launch.

Exact labels and ordering match your installed version; the list above reflects the intended product flow.

## Multi-persona launch

When multiple personas are selected, the CLI can spawn **multiple sessions** (one per persona) so parallel agents share the same repository context with **isolated session files** (`.vibeflow-session-<persona>`).

## Next steps

- [Interactive TUI](tui.md)
- [Providers](providers.md)
