# VibeFlow CLI documentation

Welcome to the documentation for **VibeFlow CLI** (`vibeflow`), a terminal-based session manager for AI coding agents. Use these guides to install the tool, connect it to [VibeFlow](https://axiomstudio.ai), and run reliable multi-agent workflows with git worktrees and persona isolation.

## Quick links

| Topic | Description |
|--------|-------------|
| [Overview](overview.md) | What VibeFlow CLI does and how it fits your workflow |
| [Installation](installation.md) | Prerequisites, install from source or `go install` |
| [Configuration](configuration.md) | Config file, environment variables, providers |
| [Interactive TUI](tui.md) | Main terminal UI, keybindings, grouped view |
| [CLI reference](cli-reference.md) | Headless commands (`launch`, `list`, `kill`, …) |
| [Providers](providers.md) | Claude Code, Codex, Gemini, Cursor Agent |
| [Session wizard](session-wizard.md) | Step-by-step session creation |
| [Worktrees & session files](worktrees-session-files.md) | Isolation, conflicts, `.vibeflow-session-*` |
| [VibeFlow server & personas](vibeflow-server.md) | Autonomous mode, API token, team personas |
| [Advanced topics](advanced-topics.md) | Error recovery, LLM gateway, session cache, plugin |
| [Troubleshooting](troubleshooting.md) | Common problems and fixes |

## Publishing this site

From `docs/VibeFlow-CLI/`:

```bash
pip install mkdocs-material
mkdocs serve   # local preview
mkdocs build   # static site in site/
```

You can point any static host or CI at `mkdocs build` output, or import the `docs/` Markdown tree into another doc platform (Docusaurus, VitePress, etc.).

---

*VibeFlow CLI is developed by [Axiom Studio](https://axiomstudio.ai).*
