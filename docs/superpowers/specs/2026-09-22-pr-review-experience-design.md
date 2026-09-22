# Multi-repository CLI reviews and GitHub progress

Date: 2026-09-22
Status: Written specification approved by the user on 2026-09-22.
Repositories: `vibeflow-cli` and `axiomcloud`.

## Intent and approved outcome

A developer should be able to leave one VibeFlow TUI open and have eligible PR reviews start automatically for multiple linked repositories and projects with known local checkouts.
The selected default project must not restrict that runner coverage.
The developer should be able to open a review from the TUI and see its progress, while GitHub shows receipt of the request, an updating checklist, and the result or an actionable failure.
The target is the acknowledgement and progress experience described by the user for Claude Code reviews, using VibeFlow branding and truthful execution state.

The approved default is two simultaneous reviews per TUI-managed runner group, configurable upward, with no fixed repository-count limit.
This is a capacity default, not permission to bypass repository policies, project access, review budgets, or runner scope checks.
The work also includes safely integrating both identified uncommitted change sets after verification.

## Current evidence and integration boundary

The inspected CLI baseline is `f0dfa7f6ad03f2c3e239613066fc62244caf1143`.
The main checkout has seven modified tracked files containing safe orphan-worktree cleanup, TUI notices, tests, and documentation, totalling 291 insertions and 23 deletions at the audit.
The retained `.claude/worktrees/cra-review-runner` checkout is based on `079cbfd5eb8f96ea8049b758a9a0c916de837481` and has seven modified tracked files containing automatic checkout discovery, startup UI including the owl, tests, and documentation, totalling 862 insertions and 106 deletions.
The CRA patch passes `git apply --check` against the current main changes, but that only proves textual applicability.
Neither set has been runtime-validated together.

The integration must preserve the offered functionality, adapt the startup UI to multiple bindings, and retain the original worktree until the integrated result is verified and committed.
Personal configuration, agent instruction files, credentials, session caches, logs, runner state, and recovery artifacts must not enter commits.
Unrelated retained branches and worktrees are outside the implementation scope.

Existing CLI components already provide repository-scoped runners, durable attempt state, checkout validation, per-binding locks, and TUI-owned process shutdown.
Existing AxiomCloud components already provide review commands, authorization, durable review events, review jobs, sticky PR comments, and check publication.
Extend those mechanisms instead of introducing a second review scheduler or publication system.

Source entry points are `internal/vibeflowcli/review_startup.go`, `tui_review_startup.go`, `review_owned.go`, `review_background.go`, `review_watch.go`, and `review_sessions.go` in the CLI.
Backend entry points are `axiomcloud/handlers/vibeflow_pr_review_commands.go`, `vibeflow_pr_review_events.go`, `vibeflow_pr_review_publications.go`, `vibeflow_pr_review_publication_provider.go`, and `vibeflow_pr_review_publication_render.go` in the AxiomCloud repository.
These are implementation entry points, not a requirement to change every file.
CLI graph indexing failed, and the backend graph predates the review feature, so the baseline analysis used direct source inspection.
Refresh exact source revisions before implementation and use source fallback wherever graph coverage remains missing or stale.

## Architecture and ownership

Keep each runner bound to a server, project, Git provider, and linked repository.
The TUI manages a collection of these runners instead of owning exactly one.
The server remains authoritative for authorization, repository policy, review budgets, queue state, and atomic claims.
No runner may accept an unrelated repository merely because another checkout is present on the machine.

Reuse the existing stable runner identity, lock, state directory, and parent-lifetime mechanisms.
Finding an already-running binding means reporting it as externally owned, not inventing a different name to evade its lock.
Different CLI roots or machines still rely on server-side atomic claims to prevent duplicate execution of the same attempt.
Closing the TUI stops only processes it owns and leaves pre-existing foreground or background runners untouched.
One binding failing to start must not stop healthy bindings or ordinary agent sessions.

### Discovery and startup

Retain a single session-level consent choice to run reviews while the TUI is open.
After opt-in, enumerate the authenticated user's accessible projects and their linked repositories, with pagination and isolated per-project errors.
Use known local paths from the current directory, configured checkout, directory history, and saved sessions/worktrees as checkout candidates.
Do not recursively scan the filesystem or clone repositories automatically.
Validate Git remotes against server-provided repository identity; session names and project labels are hints rather than authority.

Deduplicate linked worktrees, symlink aliases, and multiple clones into one selected checkout per repository binding.
Reuse an explicitly chosen or remembered valid checkout; resolve genuinely ambiguous candidates through a non-blocking choice.
Keep `default_project` as the initial display preference rather than a discovery filter.
Reconcile discovery on startup, explicit refresh, and changes to known local session/checkouts, at no more than one periodic discovery pass per minute.
Continue existing queue polling for established runners independently of discovery.

Missing or ambiguous checkouts appear with an add/select-path action in runner status instead of trapping the TUI in a mandatory path dialog.
Projects with no linked repositories appear with a clear explanation and do not prevent other projects from running.
Newly discovered valid bindings can start under the session's existing review consent.
Revoked access or disabled runners stop accepting new work and show the reason without leaking project details to unauthorized callers.

### Execution capacity and lifecycle

All discovered, enabled bindings can register and heartbeat while waiting for eligible work.
A shared capacity limit applies to review execution across all runners owned by that TUI, including their child processes.
The default is two active review attempts, and configuration accepts positive integers without a fixed repository-count ceiling.
An omitted setting uses the default; an explicitly invalid value produces an actionable configuration error rather than silently disabling protection.
Standalone review commands and externally owned runners are not silently enrolled in this TUI's capacity group.

A runner obtains capacity before claiming new work, never after obtaining a server lease.
Idle bindings release unused capacity promptly so repositories with work can proceed.
Preserve one pending attempt per repository binding; additional PRs for the same binding remain queued.
The approved first version supports simultaneous reviews across bindings, not multiple executors for one binding.
The limit covers the review attempt through execution and result handoff, so failed submissions cannot accumulate unbounded pending work.
Crashes, cancellation, unsuccessful claims, and shutdown must release capacity without admitting duplicate pending attempts.
Do not reassign a slot until its previous provider process group has stopped, including when recovering capacity after a supervisor crash.

Existing pending attempts and durable result receipts take precedence over claiming fresh work after a restart.
An interrupted attempt must be failed or have its saved result replayed under the existing lease rules, never resume the interrupted model conversation.
Transient request timeouts must not be mistaken for cancellation of the whole runner.
Authentication or publication failures must remain visible and retain recoverable results instead of appearing completed.
This work must verify those paths where they intersect runner management; unrelated CLI recovery and wizard changes remain out of scope.

## TUI review experience

Aggregate review rows across the same accessible projects used for runner discovery, including waiting reviews whose local checkout is missing.
Preserve project and repository identity in row keys, pagination, grouping, and detail requests so equal numeric IDs cannot collide across projects.
Keep ordinary agent sessions and managed review sessions distinguishable.

A first click selects a review and previews its state.
Enter or a second click opens a read-only review detail view with project, repository, PR, revision, runner, attempt, progress, and available result/error information.
The detail view exposes explicit actions to open the PR on GitHub and its existing AxiomCloud destination.
Validate external links as HTTP(S), reject credential-bearing or malformed URLs, and invoke the platform opener without shell interpolation.
Keyboard navigation, back/escape, scrolling, and narrow-terminal rendering must work without requiring mouse support.

Refresh visible progress while the review runs and label unavailable or stale information explicitly.
Show available local execution output read-only for locally owned attempts; remote reviews show server progress rather than pretending local logs exist.
Do not expose secrets or raw model reasoning in GitHub progress comments.
Review rows must not enter ordinary-session tmux attachment, deletion, branch switching, recovery, or persona controls.

## GitHub acknowledgement and progress

Keep existing review-trigger syntax and repository policy semantics.
Recognized `@vibeflow review` commands must receive an eyes reaction as acknowledgement of receipt, not a claim that authorization or execution succeeded.
Only process verified webhook deliveries for linked repositories and eligible human-created comments; ignore bots, unrelated text, and duplicate deliveries under existing intake rules.
PR-created and update hooks use the existing repository policy to decide whether to schedule work and do not require a synthetic command comment.

Persist acknowledgement intent with the existing durable event processing before returning success to the webhook sender.
Deliver the reaction asynchronously with retry and idempotency, keeping GitHub API latency out of webhook receipt.
If the command is blocked, publish one deduplicated, non-sensitive reply associated with that command, explaining the supported corrective action.
Receipt acknowledgement must never grant permission to run a review.
Authorization failures may identify the need for a connected GitHub identity with project and repository access, but must not disclose account bindings or private project membership.

For accepted review jobs, reuse the existing marker-bound sticky status comment and check publication.
Update one comment throughout the job rather than appending a comment on every tick.
Link a command's response to that job comment when an existing job already covers the request.
Keep explicit budget-exhausted, paused, disabled, no-runner, and failed states visible; do not silently reset budgets or resume blocked jobs.

### Truthful progress contract

The checklist describes observable lifecycle milestones, not simulated activity or model chain-of-thought.
Use server-known events and authenticated, revision-bound runner stage updates where existing state is insufficient.
Runner stage reports must be tied to the current claimed attempt and lease, rejecting stale attempts and unrelated callers.
Use the smallest extension of the current attempt/status transport that carries those stages; no token-streaming or general telemetry subsystem is required.

| Milestone | Completion evidence |
| --- | --- |
| Request accepted | Authorization and repository policy allow the queued job. |
| Runner assigned | The server has accepted the current attempt claim. |
| Checkout prepared | The runner validated the intended repository and prepared the claimed revision. |
| Code review completed | The current attempt produced a complete review result. |
| Result recorded | The server durably accepted that result for the current revision. |

The currently active milestone is labelled in progress, completed milestones have checked boxes, and unstarted milestones remain unchecked.
Waiting for capacity or an eligible runner is explicit, not displayed as code review in progress.
Failure or cancellation leaves unfinished milestones unchecked and shows the available corrective action.
The result-recorded milestone does not assert that every external publication has succeeded; GitHub delivery failures remain retryable publication state.
New rounds or superseding revisions reset the displayed checklist to the active round, retaining links to prior results rather than showing old completion as current success.

Publish an initial acknowledgement/status promptly through the existing background delivery path, and update on material transitions rather than heartbeat ticks.
Coalesce updates and retry rate limits or transient failures without losing newer state or creating duplicate reactions/comments.
The terminal comment includes the review outcome, confirmed findings with useful file/revision references, and links to the PR and AxiomCloud.
Keep existing sanitization, notification suppression, body limits, and check-success criteria.

## UAT configuration and PR #25

The user's local target is `https://cloud-uat.axiomstudio.ai`, with the existing `VIBEFLOW_UAT_TOKEN` supplied to the CLI's supported `VIBEFLOW_TOKEN` environment variable.
Do not hardcode this host or credential alias into production defaults, log credentials, or copy tokens into repository configuration.
The selected initial project is `axiomcloud`; it is not the scope limit for runner discovery.

The observed PR #25 command reached UAT and was blocked with the connected-account, repository-write-access, and project-access requirement.
Its existing review job also required explicit Continue with a review budget.
Adding reactions and progress must expose these conditions, not bypass them or claim a missing deployment caused that request to fail.
Changing an account binding, granting a review budget, posting a live test command, merging PRs, tagging releases, or deploying UAT requires the relevant separate authorization.
Local automated tests use isolated repositories and controlled GitHub/API doubles before any approved live smoke test.

## Verification and acceptance

Start bug-path verification with end-user-equivalent TUI and webhook flows before changing the affected behavior.
Record a baseline for both uncommitted sets, integrate them without personal artifacts, and retain regressions as runnable tests.
Use existing test facilities and dependencies rather than adding a new test framework.

### CLI acceptance

1. One TUI discovers at least three linked local repositories across at least two projects; the default project does not hide the other projects.
2. Two reviews execute concurrently by default, a third waits before claiming, and changing the configured capacity changes that behavior.
3. Missing paths, ambiguous clones, duplicate worktrees, externally owned runners, and one failed binding do not block unrelated sessions or healthy runners.
4. Enter and mouse activation open review details; external links open correctly; refresh, pagination, escape, and narrow terminals remain usable.
5. Shutdown and restart release owned resources, preserve other runners and recoverable receipts, and do not duplicate active attempts.

### GitHub and backend acceptance

1. A recognized command receives one eyes reaction and an accepted job receives one updating checklist comment under duplicate webhook deliveries and retries.
2. Authorization, policy, budget, and missing-runner failures are visible without starting unauthorized work or leaking sensitive details.
3. Real claim, checkout, execution, and result events advance the correct checklist; stale lease/revision reports cannot advance it.
4. Rate limits, network failures, process restarts, and out-of-order progress do not regress newer state or lose final publication.
5. A new PR under an enabled automatic policy starts the same review flow, while manual or disabled policies retain their intended restrictions.

Run the relevant CLI and backend tests, CLI race tests, vet/build checks, and the repository's applicable existing quality checks.
Exercise worktree cleanup against isolated dirty, in-use, out-of-scope, and clean orphan checkouts, verifying that branches and protected files survive.
Document any unavailable live validation as a limitation rather than calling the workflow deployed or end-to-end verified.
Commit only verified, scoped changes, separating preserved existing work from the new runner and GitHub behavior where practical.

## Delivery boundary

This specification authorizes no deployment or live operational mutation.
The next gate is user review of this written specification, followed by a written implementation plan and execution-method selection.
The implementation plan must choose the concrete shared-capacity mechanism and any minimal durable progress/acknowledgement schema changes against freshly verified source.
Changes must remain compatible with existing single-runner commands and older clients that do not report detailed stages; missing stage evidence is shown as unavailable rather than fabricated.
Do not add a general fleet manager, automatic cloning, a second review engine, automatic fixes beyond existing policies, or unrelated CLI refactors.
