# CLI reference

The binary name is **`vibeflow`**. Root command with no subcommand runs the **TUI**.

## Global flags

| Flag | Description |
|------|-------------|
| `--config` | Path to config file (default `<root>/config.yaml`) |
| `--root` | Root directory for config, sessions, and logs (default `~/.vibeflow-cli`). Also settable via `VIBEFLOW_ROOT` env var. Enables isolated parallel instances. |
| `--mcp` | MCP server tool name used in the agent init prompt (default: `vibeflow`). Override if you run a renamed or forked MCP server. |

The root TUI additionally accepts `--server-url` to override its backend URL and `--project` to set its default project.
Subcommands read `server_url` from configuration; set it during setup with bootstrap's `--base-url`, or override it with `VIBEFLOW_URL` once a configuration file exists.
`launch` and `review-watch` each define their own `--project` flag.

## Commands

### `vibeflow version`

Prints build version, commit, and build date.

### `vibeflow launch`

Create and launch a session without the full wizard. Key flags:

| Flag | Description |
|------|-------------|
| `--provider` | Provider key: `claude`, `codex`, `cursor`, `gemini`, `qwen`, or a custom key from `config.yaml` |
| `--branch` | Git branch (default `main`) |
| `--worktree` | Create a new git worktree for the session |
| `--new-branch` | Create a new git branch (used with `--worktree`) |
| `--worktree-name` | Custom worktree directory name (default: auto-generated) |
| `--skip-permissions` | Skip permission prompts (autonomous mode) |
| `--model` | Model id to pass to each launched provider session |
| `--models` | Comma-separated `persona=model` overrides for team launches |
| `--reuse` | Relaunch matching project/work-directory personas with their existing durable session IDs; removes older duplicates |
| `--replace` | Stop matching persona sessions and launch fresh sessions with new IDs |
| `--llm-gateway` | Route LLM requests through the VibeFlow server's LLM Gateway |
| `--openshell` | Run the agent command inside an NVIDIA OpenShell sandbox |
| `--openshell-sandbox` | OpenShell sandbox name |
| `--openshell-from` | OpenShell sandbox image/base |
| `--openshell-policy` | OpenShell policy YAML path |
| `--openshell-provider` | Comma-separated OpenShell provider names to attach |
| `--openshell-no-auto-providers` | Disable OpenShell credential auto-provider discovery |

Examples:

```bash
vibeflow launch --provider claude --branch main
vibeflow launch --provider cursor --worktree --new-branch
vibeflow launch --provider codex --skip-permissions --llm-gateway
vibeflow launch --provider claude --personas developer,architect --model sonnet --models developer=gpt-5.1-codex,architect=opus
vibeflow launch --provider codex --project nimbus --personas developer,architect --reuse
vibeflow launch --provider qwen --skip-permissions
vibeflow launch --provider codex --openshell --openshell-sandbox vf-main
```

Model flags apply when the provider process starts and are stored in session metadata so `vibeflow restart` reuses the same model. They do not rewrite a model inside an already-running provider process. The model catalog is advisory: use `vibeflow models` to discover known ids, but launch accepts explicit model strings so new provider models work before the catalog is updated.

### `vibeflow review-watch`

Keep one local or shared PR review runner online without using a model while idle.
Each claimed attempt starts a fresh Principal Engineer process with the server's finite review prompt, exact base/head snapshots, project brief, and prior finding IDs.
The server controls automatic and comment-triggered reviews, local priority, shared grants, cycle limits, repair tickets, and PR publication.

```bash
vibeflow review-watch --project my-project --repo /path/to/repo --repository-link 123 --provider claude
vibeflow review-watch --project 42 --repo /srv/repo --repository-link 123 --provider codex --runner-kind shared --name team-runner
vibeflow review-watch --project 42 --repository-link 123 --provider claude --model anthropic/claude-sonnet --once
```

| Flag | Description |
|------|-------------|
| `--project` | Project name or numeric ID; defaults to the configured project |
| `--repo` | Existing checkout of the linked base repository; defaults to the current directory |
| `--repository-link` | Required VibeFlow repository link ID |
| `--git-provider` | `github` (including Enterprise) or `bitbucket` |
| `--provider` | `claude` or `codex`; defaults to the configured provider |
| `--model` | Provider model; required when `llm_gateway_enabled` is configured |
| `--runner-kind` | `local` or `shared`; shared execution requires an existing server grant |
| `--name` | Stable runner name; defaults to hostname |
| `--interval` | Idle polling interval, at least `1s`; default `5s` |
| `--timeout` | Per-attempt maximum, `1m` to `1h`; default `15m`, also bounded by the server deadline |
| `--once` | Check one work page, process at most one review, then exit |

Use a current Claude Code or Codex CLI on macOS or Linux.
Startup checks required isolation flags and Codex's native sandbox before claiming work.
Claude uses its existing local login or configured model API key/OAuth token with restricted read tools and empty MCP configuration.
Codex copies only model authentication into a private temporary home and uses a native read-only filesystem profile with tool networking disabled.
Claude Code `2.1.268` passed native review acceptance on macOS.
The installed Codex `0.154.0` on this macOS host allows shared `/tmp` reads and is now rejected by the capability probe before new review discovery or claim.
Use Claude or a Codex runtime that passes the native capability probe on its host; Linux also requires a working native sandbox.
Custom launch templates, ambient MCP servers, repository agent rules, and ordinary session restart caches are excluded from review execution.
Custom authentication helpers or non-file Codex logins need a configured model-only API key; the runner does not copy general user configuration to make them work.

Gateway mode uses an attempt-local model relay, pins `--model`, and exposes only the selected provider's inference endpoints to the child.
The VibeFlow API token stays in the supervisor.
Provider-hosted tools, remote MCP, and saved provider conversations are rejected by the relay.
Native model authentication and the relay transport are separate from the child's source-access boundary.
Earlier native subscription inference succeeded with both providers, before the Codex shared `/tmp` read allowance was identified.
Relay restrictions have real HTTP coverage, but an actual model call through the configured VibeFlow gateway remains unverified because the available credential returned HTTP 403 from its model catalog.

Reviews inspect source and the complete diff; they do not execute project tests or code.
The developer checkout stays untouched.
The PR diff uses the unique merge base, while the exact target tip remains separate integration context.
`revisions.json` identifies both baselines; missing or ambiguous history fails visibly instead of producing a misleading diff.
Exact missing commits are fetched using the host's existing Git credentials; a missing credential fails the attempt visibly.
Symlinks are exported as literal target text, submodules are recorded without downloading, and oversized input fails explicitly.
Each snapshot is limited to 20,000 files and 512 MiB, with a 16 MiB per-file limit.
Up to three snapshots use at most 1.5 GiB of exported source, plus private Git objects and review inputs.
Prior finding evidence is bounded to 20 additional pages and 2 MiB; up to 100 relevant findings can be reconciled per result, with omitted states retained by the server.

Ctrl-C or SIGTERM stops the child process group and reports cancellation.
A parent crash, expired lease, server cancellation, or deadline also stops the child.
Owned input and temporary model credentials are removed before result publication.
Private receipts under `<root>/review-runners/` preserve exact submissions across lost HTTP responses; restart with the same root, server, project, repository, kind, and name to recover.
An interrupted conversation is never resumed.
Saved result/failure submissions can recover even after the provider CLI is removed or logged out.
Active reviews refresh both runner presence and the attempt lease.
A provider's bounded failure explanation is saved in the runner's private `last-provider-diagnostic.json` when available.

### `vibeflow models [provider]`

List curated model ids for the built-in providers. Pass a provider key to show one provider:

```bash
vibeflow models
vibeflow models codex
```

### `vibeflow list` (alias: `ls`)

List active sessions.

### `vibeflow switch <session-name>`

Attach to a tmux session by name.

### `vibeflow kill <session-name>`

Terminate a session.

| Flag | Description |
|------|-------------|
| `--cleanup-worktree` | Also remove the git worktree associated with the session |

### `vibeflow delete <session-name>` (alias: `rm`)

Remove session metadata and session file; may interact with worktree cleanup per config.

| Flag | Description |
|------|-------------|
| `--cleanup-worktree` | Also remove the git worktree associated with the session |

### `vibeflow restart <session-name>`

Kill the existing tmux session and re-launch the agent with the same provider, branch, worktree, working directory, environment, and **stored `SkipPermissions` value** — so an autonomous session stays autonomous after restart. Looks the session up in the active store first, then falls back to the session cache for dead sessions.

| Flag | Description |
|------|-------------|
| `--skip-permissions` | Explicitly override the stored autonomous setting. Pass `--skip-permissions=true` to force autonomous mode or `--skip-permissions=false` to force interactive mode; omit the flag to preserve whatever the session was launched with. |

See [Advanced topics](advanced-topics.md) for the session cache behavior that enables restart after tmux exits.

### `vibeflow worktrees` (alias: `wt`)

List or manage git worktrees related to the tool.

### `vibeflow check [directory]`

Check for **session conflicts** (`.vibeflow-session*` files vs active tmux).

### `vibeflow config`

Re-run interactive configuration.

### `vibeflow agent-doc <provider>`

Print the embedded agent documentation template for the given provider to stdout. Provider keys: `claude` → `CLAUDE.md`, `codex` → `AGENTS.md`, `cursor` → `AGENTS.md`, `gemini` → `GEMINI.md`, `qwen` → `QWEN.md`. Useful for inspecting or piping the embedded template outside of the normal launch flow (launch automatically writes these files via `EnsureAllAgentDocs`, deduplicating `AGENTS.md` when both Codex and Cursor are configured).

Use `vibeflow --help` and `vibeflow <command> --help` for the exact flag set in your installed version.

## Next steps

- [Providers](providers.md)
- [Worktrees & session files](worktrees-session-files.md)
