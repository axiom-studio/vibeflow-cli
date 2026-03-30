# Installation

## Prerequisites

- **Go 1.25+** (to build or `go install`)
- **tmux 3.2+** (environment passthrough features used by the CLI)
- At least one **agent binary** on your `PATH`, for example:
  - `claude` (Claude Code)
  - `codex` (OpenAI Codex CLI)
  - `gemini` (Google Gemini CLI)
  - `agent` ([Cursor CLI](https://cursor.com/docs/cli/overview))

## Install with Go

From the module root (matches [README.md](https://github.com/axiom-studio/vibeflow-cli) in the published repo):

```bash
go install vibeflow-cli/cmd/vibeflow@latest
```

If your environment requires the full module path (for example when installing directly from GitHub without a replace directive), use the path that matches your `go.mod` module line.

Ensure your `GOBIN` (or `GOPATH/bin`) is on `PATH` so the `vibeflow` command is found.

## Build from source

```bash
git clone https://github.com/axiom-studio/vibeflow-cli.git
cd vibeflow-cli
go build -o vibeflow ./cmd/vibeflow
```

Run `./vibeflow` or move the binary to a directory on `PATH`.

## First run

The first time you start the TUI without an existing config, an **interactive setup wizard** collects server URL, API token (for VibeFlow), and defaults. You can rerun configuration anytime with `vibeflow config`.

## Verify

```bash
vibeflow version
```

## Next steps

- [Configuration](configuration.md)
- [Session wizard](session-wizard.md)
