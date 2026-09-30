# CLI reference

The binary name is **`vibeflow`**. Root command with no subcommand runs the **TUI**.

## Global flags

| Flag | Description |
|------|-------------|
| `--cra` | Enable the PR review preview (Vera); disabled by default until production backend rollout. |
| `--config` | Path to config file (default `<root>/config.yaml`) |
| `--root` | Root directory for config, sessions, and logs (default `~/.vibeflow-cli`). Also settable via `VIBEFLOW_ROOT` env var. Enables isolated parallel instances. |
| `--mcp` | MCP server tool name used in the agent init prompt (default: `vibeflow`). Override if you run a renamed or forked MCP server. |

The root TUI additionally accepts `--server-url` to override its backend URL and `--project` to set its default project.
Subcommands read `server_url` from configuration; set it during setup with bootstrap's `--base-url`, or override it with `VIBEFLOW_URL` once a configuration file exists.
`launch`, `review-watch`, and `list` each define their own `--project` flag.

## Commands

### `vibeflow` (interactive TUI)

Server and API-key setup runs only for a new, uninitialized root and remains saved in `config.yaml`.
`vibeflow --cra` starts straight into the normal session list; there is no startup review prompt and no review runner starts implicitly.
Without `--cra`, no review UI, API requests, rows, or shortcuts are enabled.

The only way to run Vera in the TUI is a Vera session: press `n`, choose the repository's checkout and VibeFlow project, then select **Vera · Code Reviewer** in the agent picker.
The wizard continues with its ordinary **Provider** step and then **Confirm**; the coding-agent Env, Routing, Branch, Worktree and Permissions steps are skipped.
Choose any configured coding harness whose binary is installed (Claude, Codex, Gemini, Qwen, Copilot, Cursor or Kiro); other providers cannot be selected for Vera.
Vera uses the harness default model, so there is no model question.
Vera runs that harness like your other personas: your normal login, environment, MCP servers and credentials, with full permissions.
Codex works with a ChatGPT subscription login as well as an API key.
The chosen checkout must match a repository linked to that project; only when links are ambiguous or the checkout does not match does a small popup ask for that choice.
With the LLM gateway enabled, that popup also asks for a model, because gateway reviews require one.
Confirm creates an ordinary tmux session for Vera, listed with the other sessions as **Vera · Code Reviewer** with its harness, project and state (`listening`, `reviewing PR #N` or `stopped`).
Each Vera session serves one project, repository link and checkout; start one per repository.
The session runs `vibeflow --cra --root <root> --config <config> review-watch --project <id> --repo <checkout> --repository-link <id> --git-provider <provider> --provider <harness> --name <runner name>` in the foreground, adding `--model` only when one was chosen.
Passing `--cra` there is the explicit consent for repository requests on that one repository, registered only when the server advertises `repository_review_v1`.
Anyone who comments `@vibeflow review` on that repository can then request a review using your harness credentials until you delete the session.
Because Vera runs with full permissions, PR content and comments from those commenters reach an unrestricted agent on your machine; only its working directory is disposable.
Attach to the session to watch it: it prints a listening line, then for each review the claimed PR number and head commit, the worktree it prepared, and the harness's own interactive UI in the pane, followed by the result, the worktree removal and the listening line again.
Vera closes the harness once it has written a complete result.
The Vera session behaves like other persona sessions: it survives the TUI exiting, `d` deletes it, and restart re-runs the same command.
Deleting it stops the listener gracefully; an in-flight review is reported as failed and its worktree removed, and the runner deregisters.
Choosing Vera again for a repository with a live Vera session attaches that session, or reports its harness when you chose a different one.
Vera can be selected beside coding agents: its row on the team **Provider** step picks its harness, restricted to the ones Vera can run, and the coding agents keep their own settings and overrides.
Vera never enters the coding-agent task loop, and `launch --persona code_reviewer` directs you to `review-watch` instead.

With `--cra`, the TUI also displays review jobs across accessible projects, including queued jobs without an attempt.
These read-only rows are the server's PR review history, grouped per repository under **PR reviews**, while the Vera session row is the local listener; the two are different things, so each appears once.
Select a review to preview it, then press Enter or click it again to open its read-only detail.
Details show revision, runner, progress checklist, findings, publication status, and retained attempt history.
Use `o` for the PR, `c` for AxiomCloud, `r` to refresh, arrows or PageUp/PageDown to scroll, and Esc to return.
Use `]` and `[` for older and latest jobs in the selected project, or attempt history inside detail; `n` and `p` page findings inside detail.
Local diagnostics appear only for a matching attempt of one of this root's Vera sessions; provider transcripts are unavailable.
Separate `--root` instances remain independent, and `--root` is not the repository checkout.

### `vibeflow version`

Prints build version, commit, and build date.

### `vibeflow launch`

Create and launch a session without the full wizard. Key flags:

| Flag | Description |
|------|-------------|
| `--provider` | Provider key: `claude`, `codex`, `copilot`, `cursor`, `gemini`, `kiro`, `qwen`, or a custom key from `config.yaml` |
| `--branch` | Git branch (default `main`) |
| `--worktree` | Create a new git worktree for the session |
| `--new-branch` | Create a new git branch (used with `--worktree`) |
| `--worktree-name` | Custom worktree directory name (default: auto-generated) |
| `--skip-permissions` | Skip permission prompts (autonomous mode) |
| `--model` | Model id to pass to each launched provider session |
| `--models` | Comma-separated `persona=model` overrides for team launches |
| `--reuse` | Relaunch matching project/work-directory personas with their existing durable session IDs; removes older duplicates |
| `--replace` | Stop matching persona sessions and launch fresh sessions with new IDs |
| `--routing` | How the agent reaches its model: `gateway`, `direct`, `endpoint` (a compatible endpoint) or `shell` (the endpoint already set in the environment). See [Providers — Routing](providers.md#routing) |
| `--base-url` | Compatible endpoint URL (with `--routing endpoint`) |
| `--vendor` | Optional endpoint label; names the key slot `OPENAI_COMPAT_API_KEY_<VENDOR>` (with `--routing endpoint`) |
| `--llm-gateway` | Route LLM requests through the VibeFlow server's LLM Gateway (same as `--routing gateway`) |
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
vibeflow launch --provider copilot --routing endpoint --base-url http://localhost:4000/v1 --model <model-name>
vibeflow launch --provider claude --routing shell
vibeflow launch --provider codex --openshell --openshell-sandbox vf-main
```

Model flags apply when the provider process starts and are stored in session metadata so `vibeflow restart` reuses the same model. They do not rewrite a model inside an already-running provider process. The model catalog is advisory: use `vibeflow models` to discover known ids, but launch accepts explicit model strings so new provider models work before the catalog is updated.

### `vibeflow review-watch`

Passing `--cra` explicitly opts this runner into requests from commenters on its linked repository when supported by the server.
Without the server capability, it uses legacy routing and reports that a server upgrade is needed.
A Vera session shows that notice in its pane, and detached runners show it in `review-watch --status` and when started.
Runner selection is configured in the project's pull request settings, using Automatic or a preferred eligible runner.

All public forms require `--cra`, including `--background`, `--status`, and `--stop`.

Keep one local or shared PR review runner online without using a model while idle.
Each claimed attempt starts a fresh Vera process with the server's finite review prompt, a disposable git worktree of the exact head commit, the exact base snapshot, project brief, and prior finding IDs.
The server controls automatic and comment-triggered reviews, local priority, shared grants, cycle limits, repair tickets, and PR publication.

```bash
vibeflow --cra review-watch --project my-project --repo /path/to/repo --repository-link 123 --provider claude
vibeflow --cra review-watch --project 42 --repo /srv/repo --repository-link 123 --provider codex --runner-kind shared --name team-runner
vibeflow --cra review-watch --project 42 --repository-link 123 --provider claude --model anthropic/claude-sonnet --once
```

| Flag | Description |
|------|-------------|
| `--project` | Project name or numeric ID; defaults to the configured project |
| `--repo` | Existing checkout of the linked base repository; defaults to the current directory |
| `--repository-link` | Required VibeFlow repository link ID |
| `--git-provider` | `github` (including Enterprise) or `bitbucket` |
| `--provider` | Coding harness: `claude`, `codex`, `copilot`, `cursor`, `gemini`, `kiro` or `qwen`; defaults to the configured provider |
| `--model` | Provider model; required when `llm_gateway_enabled` is configured |
| `--runner-kind` | `local` or `shared`; shared execution requires an existing server grant |
| `--name` | Stable runner name, 1 to 100 bytes; defaults to hostname |
| `--interval` | Idle polling interval, `1s` to `60s`; default `5s`. Each poll also sends the runner heartbeat, and the server marks a runner offline after 2 minutes without one |
| `--timeout` | Per-attempt maximum, `1m` to `1h`; default `15m`, also bounded by the server deadline |
| `--once` | Check one work page, process at most one review, then exit |
| `--server-url` | Explicit VibeFlow server URL; overrides configuration and `VIBEFLOW_URL`, and is pinned for a background runner |
| `--background` | Save an explicit repository binding and start a detached runner; requires explicit `--project`, `--repo`, and `--repository-link`; incompatible with `--once` |
| `--status` | Show this root's managed runner IDs, pinned scope, lifecycle state, and safe last-failure metadata |
| `--stop <ID>` | Request graceful shutdown of one explicitly detached runner; retains pending receipts and review history |

#### Background runner management

Ordinary persona sessions do not start a review runner implicitly.
For ordinary interactive use, start a Vera session from the TUI (see above), which already survives the TUI exiting.
Use explicit detached mode on headless machines without tmux, using the same root and config that contain your VibeFlow credentials.
Stop an existing foreground watcher with Ctrl-C before enabling its background replacement.

```bash
vibeflow --cra --root /path/to/cli-root review-watch --background \
  --server-url https://cloud-uat.axiomstudio.ai \
  --project 12 --repository-link 7 --git-provider github \
  --provider claude --repo /path/to/axiomcloud
vibeflow --cra --root /path/to/cli-root review-watch --status
vibeflow --cra --root /path/to/cli-root review-watch --stop <runner-ID-from-status>
```

The start command waits up to 20 seconds for provider preflight and a successful server heartbeat before reporting that the runner is running.
It stays alive after the terminal or TUI exits; idle polling makes no model calls.
Each claimed review runs the harness headless, not in a tmux pane.
Use the preview TUI's retained review sessions or `vibeflow --cra list --project 12` to see attempts, and `vibeflow --cra review-watch --status` to see the local background supervisor.
Repeated starts of the same binding do not launch another supervisor; changing an active binding requires stopping it first.

The private binding under `<root>/review-runners/<ID>/background.json` saves absolute root/config/repository paths, the exact server URL, provider/model, local login sources, and a one-way VibeFlow credential fingerprint, not credential values.
Background credentials must already exist in the selected config; ambient-only `VIBEFLOW_TOKEN` is refused.
An explicit `VIBEFLOW_URL` at opt-in is pinned, so a later shell's production URL or token cannot redirect that runner.
Literal model credentials can come from `provider.env` or `saved_env_vars`, or from the harness's existing local login (for example a Claude login or a Codex ChatGPT login under `CODEX_HOME`); ambient-only model secrets and interpolated provider credentials are refused.
An existing absolute `SSH_AUTH_SOCK` is pinned for Git fetches and is also available to the harness, like any other persona's environment.
If the socket changes after login or reboot, explicitly enable the runner again with the new socket.
After rotating the VibeFlow token, explicitly enable the runner again to approve its new credential binding.

Launching the TUI never restarts saved detached bindings or treats them as consent.
To restart one, rerun its full `--background` command.
Rerunning it while the runner is active reports the active runner.
If that runner was started without repository-request routing, for example by an older CLI, the command fails and asks you to stop it with `--stop` and start it again.
The stop command waits up to 10 seconds, then reports if shutdown is still pending; it never signals an unverified saved PID.
`background.log` contains fixed lifecycle messages only, while `last-provider-diagnostic.json` contains sanitized failure metadata.
No service manager, login item, deployment, or machine-boot autostart is installed.

Vera runs on any supported coding harness on macOS or Linux, in normal mode like the other personas.
Startup only checks that the harness is configured and its binary is installed; it makes no model call.
In a terminal, Vera also runs the harness's own login status command before claiming any review, and refuses to start with the login command when it reports that it is not logged in: `claude auth status`, `codex login status`, `agent status` (Cursor) and `kiro-cli whoami`.
Copilot, Gemini and Qwen have no non-interactive status command, so for them a login screen shows up in the pane instead.
`codex login status` reads only the local credentials, so a login the server has revoked still shows Codex's sign-in screen in the pane.
If the harness reports that it is not logged in, was refused access, or does not trust the review worktree, that attempt fails once and Vera stops with the command to fix it, instead of spending the review's remaining attempts.
The CLI pre-accepts folder trust for each disposable review worktree for these harnesses:

| Harness | How the worktree is trusted |
|---------|-----------------------------|
| `claude` | Interactive runs set `projects["<worktree>"].hasTrustDialogAccepted` in Claude's config (`~/.claude.json`, or `$CLAUDE_CONFIG_DIR/.claude.json`); cleanup removes every entry inside the review directory, including the one Claude adds for the worktree's repository. Headless `claude -p` does not ask. |
| `codex` | Interactive runs add a `[projects."<worktree>"]` table with `trust_level = "trusted"` to Codex's `config.toml` (`$CODEX_HOME`, by default `~/.codex`); cleanup removes it. A `-c` override is not used because codex 0.159.2 then fails its interactive start with HTTP 401. |
| `copilot` | The worktree is added to `trustedFolders` in `~/.copilot/config.json`, as for a persona launch. |
| `gemini` | `GEMINI_CLI_TRUST_WORKSPACE=true`. |
| `cursor` | `--trust` in headless runs only; interactive Cursor can still ask to trust the worktree. |

Kiro and Qwen start with their permission-bypass flags and showed no folder-trust step in a 2026-09-30 check.
The harness gets your full environment, its configured `env`, saved env vars, and its own login and configuration, with your real `HOME` and `CODEX_HOME`.
Codex therefore works with a ChatGPT subscription login or an API key.
In a terminal, such as a Vera session's tmux pane, each review runs the harness's normal interactive UI in the foreground of that terminal, launched with the provider's launch template in full-permission mode and the task file as its initial prompt, the same way a persona receives its init prompt (Gemini uses `-i` so its UI stays interactive).
Keys typed into the pane reach the harness, and Vera closes it with SIGINT, then SIGTERM, then SIGKILL once a complete `result.json` exists or the deadline or lease ends, then restores the terminal.
The harness gets the pane's own terminal device (the listener's stdin, stdout and stderr), never the `/dev/tty` alias, which macOS kqueue rejects.
If an interactive harness exits within 15 seconds without a result, Vera fails that attempt once and stops, showing the exit code and that the harness may need a login or a trust answer; the review stays queued for when Vera starts again.
Its output is not captured, so the login and trust classification below applies to headless runs.
If no result exists after 60 seconds, the pane shows once that Vera is still waiting, in case the harness shows a login or trust screen to answer there.
Ctrl-C during a review goes to the harness; when it exits without a result, the attempt is reported as cancelled and Vera waits 5 seconds, so a second Ctrl-C stops Vera instead of resuming listening.
After each interactive review Vera takes the terminal back, restores its modes, discards unread input such as late replies to the harness's terminal queries, and continues on a fresh line.
Stopping the listener during a review reports the attempt as cancelled (the server has no release call) and exits without announcing listening again.
Without a terminal (detached runners, redirected output), each harness runs headless with its permission prompts disabled, and its output is never shown or relayed:

| Harness | Command |
|---------|---------|
| `claude` | `claude -p --dangerously-skip-permissions [--model M]`, task on stdin |
| `codex` | `codex exec --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check [-m M] -`, task on stdin |
| `gemini` | `gemini --yolo [-m M] -p <prompt>` |
| `qwen` | `qwen --yolo [-m M] <prompt>` |
| `copilot` | `copilot --yolo [--model M] -p <prompt>` |
| `cursor` | `agent -p --force [--model M] <prompt>` |
| `kiro` | `kiro-cli chat --no-interactive --trust-all-tools [--model M] <prompt>` |

The binary comes from the provider's `binary` in `config.yaml`.
For argument-prompt harnesses, `<prompt>` points the agent at the full task file, which avoids per-argument size limits.
The task tells the agent to write exactly one JSON object to the review's `result.json`, matching `schema.json`, then stop.
A harness that exits without writing that file fails the attempt as `invalid_result`.
Headless runs do not use launch templates; interactive runs use the provider's launch template, like persona sessions.

Gateway mode requires `--model`.
With Claude, gateway mode uses an attempt-local model relay and exposes only Claude's inference endpoints to the harness.
Other harnesses use their normal configuration and login, resolved like an ordinary session's environment.
The VibeFlow API token is never placed on the harness command line.
Provider-hosted tools, remote MCP, and saved provider conversations are rejected by the relay.
Relay restrictions have real HTTP coverage, but an actual model call through the configured VibeFlow gateway remains unverified because the available credential returned HTTP 403 from its model catalog.

Every review runs in its own disposable git worktree, never in the developer checkout.
The worktree is detached at the exact head commit and is created from a private object store under `<root>/review-runners/<ID>/work/<request>/`, so the developer repository never gains a worktree entry or branch.
Beside it are `base/`, `merge-base/` (when the target advanced), `revisions.json`, `review.diff`, `brief.json` and `prior-findings.json`.
The agent may build and run project code and tests inside the worktree; it is told not to push, not to publish PR comments, and not to touch any other checkout.
The developer checkout stays untouched.
The PR diff uses the unique merge base, while the exact target tip remains separate integration context.
`revisions.json` identifies both baselines; missing or ambiguous history fails visibly instead of producing a misleading diff.
Exact missing commits are fetched using the host's existing Git credentials; a missing credential fails the attempt visibly.
SSH checkouts retain their SSH authentication transport when fetching missing commits, including forks on the same verified provider host.
GHE.com accepts both `TENANT@TENANT.ghe.com:OWNER/REPO.git` and `ssh://TENANT@TENANT.ghe.com/OWNER/REPO.git`; the username must match the tenant, and embedded passwords remain forbidden.
The head worktree is an ordinary checkout: symlinks stay symlinks, submodules are not initialized, and Git LFS content is left as pointer files.
In the `base/` and `merge-base/` context snapshots, symlinks are exported as literal target text, submodules are recorded without downloading, and oversized input fails explicitly.
Each context snapshot is limited to 20,000 files and 512 MiB, with a 16 MiB per-file limit.
Up to two context snapshots use at most 1 GiB of exported source, plus the head worktree, private Git objects and review inputs.
Private Git acquisition has a 2 GiB aggregate budget checked before and after each fetch and every 100 ms while fetching, including historical objects absent from the reviewed snapshots.
Exceeding that budget stops the Git process group and fails the attempt before source export; ordinary receipt cleanup removes its private directory.
This sampled guard can overshoot between checks and is not a hard disk quota; shared runner deployments that require a strict ceiling must also apply a filesystem or container storage quota.
Prior finding evidence is bounded to 20 additional pages and 2 MiB; up to 100 relevant findings can be reconciled per result, with omitted states retained by the server.

Ctrl-C or SIGTERM stops the child process group and reports cancellation.
A parent crash, expired lease, server cancellation, or deadline also stops the child.
Cleanup is part of every review: on success, failure, timeout, cancellation, lease loss, and crash recovery on the next start, the whole review directory, including the worktree and its private object store, is removed before result publication.
Private receipts under `<root>/review-runners/` preserve exact submissions across lost HTTP responses; restart with the same root, server, project, repository, kind, and name to recover.
An interrupted conversation is never resumed.
Saved result/failure submissions can recover even after the provider CLI is removed or logged out.
Active reviews refresh both runner presence and the attempt lease.
Runner registration binds the selected Git provider and repository link; the CLI verifies the server echoes both before continuing.
Upgrade the server and re-register if scope confirmation fails.
Legacy unscoped registrations are disabled by the server.
A failed execution saves versioned metadata in the runner's private mode-0600 `last-provider-diagnostic.json` before temporary inputs are removed.
It records the attempt ID, stage, fixed category, actual provider exit code or signal when available, durations, captured stdout/stderr byte counts, and numeric HTTP status when available.
It never retains raw provider output, free-form failure explanations, prompts, command arguments, environment values, or credential paths.
Stages distinguish checkout, provider setup, child-guard startup, provider execution, and result validation; categories distinguish cancellation, deadline, lease expiry/rejection, process failure, and invalid results.
Relative roots such as `--root .` are anchored to their absolute location before the child guard changes directory.
Claude API failures retain a sanitized category and HTTP status even when the provider process exits unsuccessfully.
For example, `rate_limited` with status `429` identifies a quota or throttling failure without saving the provider's raw error text.
Failed executions remain failed; richer diagnostics do not retry a finished server round or reset its repair-cycle budget.

### `vibeflow models [provider]`

List curated model ids for the built-in providers. Pass a provider key to show one provider:

```bash
vibeflow models
vibeflow models codex
```

### `vibeflow list` (alias: `ls`)

List local agents; `--cra` also includes managed Principal Engineer review sessions for the selected project.
Use `--project <id-or-name>` or the configured default project.
Managed reviews are read-only and include retained completed, failed, cancelled and expired attempts.

```bash
vibeflow list --project my-project
vibeflow --cra list --project 42 --reviews-after <returned-cursor>
```

Each page contains at most 25 managed reviews, newest first.
The output includes repository/PR, head SHA, runner, state, round and attempt.
An unavailable local tmux server does not hide shared reviews.
An unreachable or refusing review API prints one `managed reviews unavailable: ...` warning on stderr after at most 3 seconds; the local listing stands and the command still exits 0.
The TUI shows `PR reviews: no project selected (use --project or set default_project)` when no project resolves instead of a stale-history warning.
The TUI shows the same reviews alongside local agents, with `]` for older reviews and `[` for the latest page.
`r` refreshes the selected review page.
Transient failed refreshes retain the previous page with a stale-data warning.
Authentication, authorization or missing-endpoint responses clear managed history and details.
Managed reviewers have no attach, resume, chat, delete, branch, group-edit or workbench controls and never enter the ordinary restart cache.
Use AxiomCloud for review controls and findings.

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

Re-launch the agent with the same provider, branch, worktree, working directory, environment, and **stored `SkipPermissions` value** so an autonomous session stays autonomous after restart.
Exited panes are reused in place; a running session is stopped and replaced.
The command looks the session up in the active store first, then falls back to the session cache.
For recovery directly inside an exited pane, press **Enter** to resume its exact conversation or open the harness's history picker.

| Flag | Description |
|------|-------------|
| `--skip-permissions` | Explicitly override the stored autonomous setting. Pass `--skip-permissions=true` to force autonomous mode or `--skip-permissions=false` to force interactive mode; omit the flag to preserve whatever the session was launched with. |

See [Advanced topics](advanced-topics.md) for the session cache behavior that enables restart after tmux exits.

### `vibeflow worktrees` (alias: `wt`)

List or manage git worktrees related to the tool.

| Flag | Effect |
|------|--------|
| `--clean` | Remove orphaned worktrees and prune stale records. |

An orphaned worktree is one under `worktree.base_dir` that no stored session references.
`--clean` keeps any worktree with uncommitted changes and prints one row per worktree: `removed`, `kept` (with the number of uncommitted changes) or `in use` (with the session).
The plain listing adds a `SESSION` column that shows the session using each worktree, or `orphaned`.
Paths are shown relative to the repository.
The branch of a removed worktree is always kept, so committed work is never lost.
The TUI help bar shows the orphan count next to `w: worktrees` when there are any.

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
