# PR Review Experience Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Integrate the two preserved CLI change sets and deliver multi-repository runners, clickable review details, and durable GitHub acknowledgement and progress.

**Architecture:** A TUI owns several existing repository-scoped runner processes and a shared execution-capacity group.
AxiomCloud remains authoritative for claims, authorization, budgets, and durable GitHub publication.
Extend leased attempt renewal and existing safe summaries for progress rather than adding another scheduler or detail API.

**Tech Stack:** Go 1.25 CLI, Go 1.26.5 backend, Bubble Tea v2, Cobra, existing Unix process guards and file locks, existing AxiomCloud Go handlers/database adapters, PostgreSQL and SQLite migrations, GitHub REST API, and existing Go/PTY test facilities.

**Spec:** [Approved design](../specs/2026-09-22-pr-review-experience-design.md).

## Global Constraints

The approved default is two simultaneous reviews per TUI-managed runner group, configurable upward, with no fixed repository-count limit.
Preserve one pending attempt per repository binding; additional PRs for the same binding remain queued.
A runner obtains capacity before claiming new work, never after obtaining a server lease.
Do not reassign a slot until its previous provider process group has stopped, including when recovering capacity after a supervisor crash.
Closing the TUI stops only processes it owns and leaves pre-existing foreground or background runners untouched.
Personal configuration, agent instruction files, credentials, session caches, logs, runner state, and recovery artifacts must not enter commits.
Do not recursively scan the filesystem or clone repositories automatically.
Keep existing review-trigger syntax and repository policy semantics.
Do not expose secrets or raw model reasoning in GitHub progress comments.
Use existing test facilities and dependencies rather than adding a new test framework.
This specification authorizes no deployment or live operational mutation.

Use `apply_patch` for edits, preserve unrelated changes, do not edit generated files or changelogs, and do not add agent co-authors.
Use plain dashes in new prose and put each Markdown sentence on its own source line.
Do not start a VibeFlow autonomous session: none is active for this task.
Do not enable paid installed-provider acceptance tests or post live GitHub commands as part of automated verification.

## Review Focus

1. Supervisor death while provider descendants remain alive must not free capacity early: Task 3's cross-process guard test.
2. A project disappears or returns forbidden while another project's page is loading: Task 9's independently fenced history test.
3. GitHub accepted a reaction/comment but the response was lost: Task 6's reconciliation-before-create test.
4. A stale worker reports progress for an old PR revision after a new round starts: Task 7's lease/revision and monotonicity tests.
5. An older backend or runner lacks progress fields: Tasks 7 and 8's empty-renewal and capability-absent compatibility tests.

## Baselines, ownership, and execution order

CLI workspace: `/Users/vishnu/Projects/axiom/vibeflow-cli`, branch `feat/pr-review-experience`, design commit `e991896`, product-code baseline `f0dfa7f6ad03f2c3e239613066fc62244caf1143`.
The seven unstaged cleanup files belong to the user and are deliberately included in Task 1.
The preserved CRA source is `.claude/worktrees/cra-review-runner`, at `079cbfd5eb8f96ea8049b758a9a0c916de837481`, with seven unstaged files containing 862 insertions and 106 deletions.
Do not delete that worktree as part of this plan.

Backend source baseline: `9845769937bc66a685d709daa7bf4e7d582de6a0`, freshly fetched `origin/main` on 2026-09-22.
The existing backend checkout `/Users/vishnu/Projects/axiom/axiomcloud` is on `fix/vault-backed-ai` and must not be switched or overwritten.
At execution time use the git-worktrees skill to create an isolated sibling backend worktree on a new feature branch from this baseline, after checking the destination and branch are unused.
Use `/Users/vishnu/Projects/axiom/axiomcloud-review-experience` as the proposed destination; if occupied, choose a new unused path and record it before proceeding.
Backend commands below run from its `axiomcloud/` module, not the monorepo root.

This is one coordinated user workflow with two implementation tracks, not a collection of unrelated features.
The CLI track is Tasks 1-3 and 5; the server track is Tasks 4, 6, and 7; the integration track is Tasks 8-10.
The server track can proceed alongside Tasks 1-3 after workspace setup because it owns a different repository.
Task 5 consumes Task 4's additive discovery contract, while retaining a visible legacy-server fallback.
Within the CLI, serialize edits to `review_watch.go`, `root.go`, and `tui.go`; parallel workers must not edit these shared files simultaneously.
Each worker receives an exact task and file ownership, is told others are working in the codebase, and must preserve their changes.
New symbols and paths below are proposed additions, not claims that those APIs already exist.

Graph evidence is incomplete: CLI indexing fails before extraction; backend generation `2026-09-06T18:27:53Z` lacks these review files.
Use graph-first discovery and coverage checks, followed by exact-source reads where coverage is missing, throughout execution.

## File responsibility map

| Area | Files and responsibility |
| --- | --- |
| Preserved work | Existing CLI cleanup and CRA startup files listed in Tasks 1-2. |
| Runner admission | New `internal/vibeflowcli/review_capacity.go` and its tests; existing process guards retain slot ownership through shutdown. |
| Multi-runner discovery | New `internal/vibeflowcli/review_supervisor.go` and tests; existing startup/root code owns consent and lifecycle. |
| GitHub command feedback | Backend event database, command handler, publication worker/provider, and GitHub adapter; Task 6 lists exact paths. |
| Progress | Backend attempt lifecycle and safe projections, existing CLI renewal/child protocol; Tasks 7-8 define the wire contract. |
| Review navigation | New `internal/vibeflowcli/tui_review_detail.go`; existing `review_sessions.go` and `tui.go` integrate safe rows and navigation. |

## Task 1: Verify and commit the offered worktree-cleanup changes

**Ownership:** CLI cleanup only.
**Files:** `README.md`, `docs/VibeFlow-CLI/docs/cli-reference.md`, `internal/vibeflowcli/commands.go`, `tui.go`, `tui_worktrees.go`, `worktree.go`, and `worktree_test.go`.
**Consumes:** Existing `cleanOrphanWorktrees(wm *WorktreeManager, store *Store, out io.Writer) error`, `RemoveIfClean`, `initTestRepo(t)`, and `withTempRoot(t)` from the offered change set.
**Produces:** The same cleanup behavior, verified and committed without personal artifacts.

- [ ] **Step 1: Record the exact baseline and run the offered regression before changing it.**

```sh
git status --short
git diff --stat
git diff --check
go test ./internal/vibeflowcli -run '^TestCleanOrphanWorktrees$' -count=1 -v
```

Expected: the isolated test preserves dirty, in-use, and outside worktrees and the removed worktree's branch.
Read all seven diffs; do not run `worktrees --clean` against the user's actual repositories.

- [ ] **Step 2: Add the smallest missing safety assertion only if the offered test omits it.**

For example, ensure the shared removal helper itself rejects untracked work rather than relying only on caller filtering:

```go
func TestRemoveIfCleanPreservesUntrackedFile(t *testing.T) {
    withTempRoot(t)
    repo := initTestRepo(t)
    wm, err := NewWorktreeManager(repo, ".claude/worktrees")
    if err != nil { t.Fatal(err) }
    path, err := wm.CreateBranch("dirty", "keep-dirty", true, "")
    if err != nil { t.Fatal(err) }
    file := filepath.Join(path, "draft.txt")
    if err := os.WriteFile(file, []byte("keep"), 0600); err != nil { t.Fatal(err) }
    if err := wm.RemoveIfClean(path); err == nil { t.Fatal("dirty removal accepted") }
    if data, err := os.ReadFile(file); err != nil || string(data) != "keep" {
        t.Fatalf("lost untracked work: %q %v", data, err)
    }
}
```

Run this test before any corrective edit; a passing offered implementation needs no manufactured failing test.

- [ ] **Step 3: Correct only demonstrated cleanup defects, then run the affected suite.**

```sh
go test ./internal/vibeflowcli -run 'Test(CleanOrphanWorktrees|RemoveIfClean|WorktreeManager)' -count=1
git diff --check
```

Keep non-force Git removal, branch retention, symlink-aware scope checks, and refusal on status errors.

- [ ] **Step 4: Commit exactly the seven offered tracked paths plus the scoped test edits.**

```sh
git add -- README.md docs/VibeFlow-CLI/docs/cli-reference.md internal/vibeflowcli/commands.go internal/vibeflowcli/tui.go internal/vibeflowcli/tui_worktrees.go internal/vibeflowcli/worktree.go internal/vibeflowcli/worktree_test.go
git diff --cached --check
git diff --cached --name-only
git commit -m "feat: preserve work while cleaning orphan worktrees"
```

## Task 2: Integrate the preserved checkout-discovery and startup UI patch

**Ownership:** CLI CRA integration only.
**Files:** `docs/VibeFlow-CLI/docs/cli-reference.md`, `configuration.md`; `internal/vibeflowcli/review_startup.go`, `review_startup_test.go`, `tui_review_startup.go`, `tui_review_startup_test.go`, `tui_review_e2e_test.go`.
**Consumes:** The seven tracked diffs from `.claude/worktrees/cra-review-runner`.
**Produces:** Existing proposed-in-WIP `findReviewStartupCheckout(ctx context.Context, path string, repositories []reviewStartupRepository) *reviewStartupCheckout`, project-specific missing-link errors, and the tested startup/owl UI.

- [ ] **Step 1: Run the CRA tests in their source worktree and check patch applicability.**

```sh
git -C .claude/worktrees/cra-review-runner diff --check
git -C .claude/worktrees/cra-review-runner diff --binary | git apply --check
go -C .claude/worktrees/cra-review-runner test ./internal/vibeflowcli -run 'TestReviewStartup|TestReviewTUIBinaryConsent' -count=1
```

Inspect any failure before copying changes; test availability is not proof that they pass.

- [ ] **Step 2: Transfer only the audited tracked hunks with `apply_patch`.**

Keep later-main fixes and the Task 1 documentation additions.
Do not copy complete old files over current source or copy the source worktree's runtime files.
The checkout helper must still use the actual Git origin plus Git common-directory identity, not session project labels.

- [ ] **Step 3: Run the transferred tests on the integration branch.**

```sh
go test ./internal/vibeflowcli -run 'TestReviewStartup|TestReviewTUIBinaryConsent|TestReviewRunnerStatus' -count=1 -v
git diff --check
```

Require existing tests for stale remembered links, symlink/worktree deduplication, distinct clones, narrow terminals, consent, and owner death to pass.

- [ ] **Step 4: Commit only those seven paths.**

```sh
git add -- docs/VibeFlow-CLI/docs/cli-reference.md docs/VibeFlow-CLI/docs/configuration.md internal/vibeflowcli/review_startup.go internal/vibeflowcli/review_startup_test.go internal/vibeflowcli/tui_review_startup.go internal/vibeflowcli/tui_review_startup_test.go internal/vibeflowcli/tui_review_e2e_test.go
git diff --cached --check
git commit -m "feat: discover linked review checkouts at startup"
```

## Task 3: Add shared execution capacity and preserve recoverable attempts

**Ownership:** CLI runner admission, configuration, and process cleanup.
**Create:** `internal/vibeflowcli/review_capacity.go`, `review_capacity_test.go`.
**Modify:** `config.go`, `config_test.go`, `review_owned.go`, `review_watch.go`, `review_provider.go`, `review_process_unix.go`, and the matching unsupported-platform stubs if their signatures change.
**Extend tests:** `review_owned_test.go`, `review_recovery_test.go`, and existing process-group tests.
**Docs:** `docs/VibeFlow-CLI/docs/configuration.md`.

**Interfaces, proposed:**

```go
// Config field; DefaultConfig initializes it before YAML decoding.
ReviewConcurrency int `yaml:"review_concurrency"`

type reviewCapacity struct {
    Directory string `json:"directory"`
    Limit int `json:"limit"`
}
func newReviewCapacity(root string, limit int) (*reviewCapacity, error)
func (c *reviewCapacity) tryAcquire() (*os.File, error)
func (c *reviewCapacity) Close() error
func (w *reviewWatch) acquireCapacity() (bool, error)
func startReviewOwnedWithCapacity(ctx context.Context, cfg *Config,
    configPath string, options reviewWatchOptions,
    capacity *reviewCapacity) (*reviewOwnedRunner, error)
```

Keep `startReviewOwned` as a wrapper passing nil capacity so standalone behavior remains unchanged.
Add `Capacity *reviewCapacity` to `reviewOwnedSpec`, `capacity *reviewCapacity` and `slot *os.File` to `reviewWatch`, and `CapacityFD int` to `reviewChildSpec`.
Only `0` and `3` are valid capacity descriptor values.
`tryAcquire` is nonblocking: `(nil, nil)` means saturation, while actual filesystem/locking errors remain errors.

- [ ] **Step 1: Add failing admission tests and configuration cases.**

```go
func TestReviewCapacityLimits(t *testing.T) {
    c, err := newReviewCapacity(t.TempDir(), 2)
    if err != nil { t.Fatal(err) }
    a, err := c.tryAcquire()
    if err != nil || a == nil { t.Fatal("first slot", err) }
    defer a.Close()
    b, err := c.tryAcquire()
    if err != nil || b == nil { t.Fatal("second slot", err) }
    defer b.Close()
    if extra, err := c.tryAcquire(); err != nil || extra != nil {
        if extra != nil { extra.Close() }
        t.Fatal("capacity exceeded", err)
    }
    b.Close()
    replacement, err := c.tryAcquire()
    if err != nil || replacement == nil { t.Fatal("slot not reusable", err) }
    replacement.Close()
    a.Close()
    if err := c.Close(); err != nil { t.Fatal(err) }
}
```

Add table cases for omitted YAML giving `2`, positive values `1` and `4`, and rejected `0`, `-1`, `null`, and noninteger values.
Check the YAML node for an explicitly present null before config migration can overwrite the invalid input.
Run `go test ./internal/vibeflowcli -run 'TestReviewCapacity|TestReviewConcurrency' -count=1`; expect undefined new symbols initially.

- [ ] **Step 2: Implement file-slot admission using existing nonblocking flock behavior.**

Create a private `os.MkdirTemp` group under the CLI root, with private slot files and no new dependency or coordinator service.
Create slot files lazily as they are considered, not by allocating all configured slots up front; cleanup visits only that group's existing validated files.
Distinguish `EWOULDBLOCK`/`EAGAIN` from other errors instead of turning every lock error into saturation.
Use this flow inside `poll` only after eligible work is found:

```go
acquired, err := w.acquireCapacity()
if err != nil { return err }
if !acquired { return nil }
// Existing receipt creation and claim follow this gate.
// Do not advance the work cursor when admission was denied.
```

An empty or rejected claim releases its slot after clearing its own receipt.
An active or submission-pending receipt retains its slot through result handoff.
Heartbeat and work discovery remain independent of waiting for capacity.
Recovery-bearing bindings get admission before fresh claimers during initial reconciliation.

- [ ] **Step 3: Make slot lifetime include provider-group cleanup.**

```go
if w.slot != nil {
    spec.CapacityFD = 3
    guard.ExtraFiles = []*os.File{w.slot}
}
```

The guard retains the inherited open-file description until its provider group has stopped, sets close-on-exec on that descriptor before launching the model, and never forwards it to model `ExtraFiles`.
Extend the existing process cleanup to confirm group termination, not only leader exit.
Never unlock explicitly through one duplicate descriptor while another process still relies on that lock; release by closing owned descriptors.

For abnormal guard death, write an attempt-scoped `provider-cleanup-pending.json` before launch, containing only the capacity-group/slot association and existing attempt identity, in the existing private attempt directory.
Only the live guard records cleanup completion after verified group shutdown.
An unresolved marker quarantines its binding and capacity slot; do not blindly kill a numeric PGID loaded from disk or clear uncertainty on a timeout.
On a new TUI launch, count unresolved markers from prior groups in that CLI root against available capacity until cleanup is confirmed, while remaining slots continue serving healthy bindings.
Reuse receipt identity to deduplicate this cleanup debt, and refuse capacity-directory removal while any held lock or unresolved marker remains.
Report `Provider cleanup unverified` with the private diagnostic location; automatic recovery is allowed when a surviving guard confirms cleanup, not when a lock merely becomes free.

Extend `TestReviewOwnedParentDeathStopsActiveDescendants` with inherited-slot assertions and a guard-SIGKILL case.
The assertion at the live-child checkpoint is:

```go
if slot, err := capacity.tryAcquire(); err != nil || slot != nil {
    if slot != nil { slot.Close() }
    t.Fatalf("admitted work before provider cleanup: %v", err)
}
```

In that existing test, `capacity` is the new group passed to `startReviewOwnedWithCapacity`; retain the existing real descendant fixture and readiness synchronization.
Also restart the capacity group with an unresolved cleanup marker and assert that the quarantined slot is not silently restored.

- [ ] **Step 4: Pin the submission and timeout regressions before correcting them.**

Extend `TestReviewSavedResultSurvivesLostResponseWithoutRelaunch` with result-submission `401` and `403` responses followed by restored authorization.
Assert durable `Pending`, unchanged result bytes, no completed marker, and no new provider launch before the same result is acknowledged.
Classify submission failures separately from existing permanent claim `404`/`409` behavior.
Add a blocked `/result` integration case reaching the actual 30-second local submission deadline; verify the parent runner remains alive and retries the saved receipt.
A short HTTP client timeout on `/work` is not a reproduction of that local-context bug.
The loop decision must preserve the distinction:

```go
if ctx.Err() != nil { return ctx.Err() }
if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errReviewConnection) {
    continue
}
```

Apply this after normal poll pacing, not in a tight retry loop.
Release admission only after acknowledged receipt finalization or verified cancellation cleanup.

- [ ] **Step 5: Run runner tests and commit this independently testable layer.**

```sh
go test ./internal/vibeflowcli -run 'TestReview(Capacity|Concurrency|Owned|SavedResult|Submission)' -count=1 -timeout=180s
go test -race ./internal/vibeflowcli -run 'TestReview(Capacity|Owned|SavedResult)' -count=1 -timeout=180s
git diff --check
```

Stage only Task 3 paths after reviewing the diff; commit as `feat: bound owned review execution across repositories`.

## Task 4: Remove the hidden project-discovery ceiling compatibly

**Ownership:** Backend project-list pagination only.
**Modify:** `axiomcloud/handlers/vibeflow_projects.go`, `axiomcloud/database/vibeflow_projects.go`, and their existing test files.
**Consumes:** Existing organization, privacy, archive, and rollout-controlled project ACL predicates.
**Produces:** Opt-in `GET /rest/v1/vibeflow/projects?paginated=true&limit=100&after_id=<last-id>` returning `{projects:[],next_after_id:<decimal-string>}`.
Requests without `paginated=true` retain the existing bare-array response and ordering.

The current database query has `LIMIT 200`; do not assume its bare array is complete.
Repository-link enumeration has no such pagination or hidden cap and continues using its current complete array.

**Proposed database contract:**

```go
type VibeflowProjectPage struct {
    Projects []VibeflowProject `json:"projects"`
    NextAfterID string `json:"next_after_id"`
}
func (db *DB) ListVibeflowProjectsPage(ctx context.Context, orgID string,
    userID int64, includeArchived bool, afterID int64, limit int) (VibeflowProjectPage, error)
```

- [ ] **Step 1: Add failing HTTP and database paging regressions.**

Use the existing project fixture to create 205 visible projects plus foreign-organization, private-other-owner, archived, rejected, and ACL-denied projects.
Page with limits `1` and `100`; assert every visible ID appears once and excluded IDs never appear.
Assert a legacy request is still a bare array.
Exercise the same cases with project ACL rollout enabled and disabled.
The page-walk assertion is:

```go
seen := map[int64]bool{}
var after int64
for {
    page, err := db.ListVibeflowProjectsPage(t.Context(), orgID, userID, false, after, 100)
    if err != nil { t.Fatal(err) }
    for _, p := range page.Projects {
        if seen[p.ID] { t.Fatalf("duplicate project %d", p.ID) }
        seen[p.ID] = true
    }
    if page.NextAfterID == "" { break }
    next, err := strconv.ParseInt(page.NextAfterID, 10, 64)
    if err != nil || next <= 0 || (after != 0 && next >= after) {
        t.Fatalf("non-progressing cursor %q", page.NextAfterID)
    }
    after = next
}
if len(seen) != 205 { t.Fatalf("truncated projects: %d", len(seen)) }
```

Here `db`, `orgID`, and `userID` are the existing fixture's database and authenticated scope, after seeding the 205 projects.

- [ ] **Step 2: Implement stable ID-descending keyset pagination.**

Bind `p.id < afterID` only after the first page, order by `p.id DESC`, and fetch `limit+1` to determine whether another page exists.
Reuse the existing access predicate and row decoding without broad project-query refactoring.
Require positive numeric cursors and limits between 1 and 100; use parameter binding for SQLite and PostgreSQL.
Check `rows.Err()` and propagate context cancellation.
Set `Cache-Control: no-store` on the new response.

- [ ] **Step 3: Run both authorization and pagination tests, then commit.**

```sh
go test ./database ./handlers -run 'Test.*Project.*(Page|Paginat|Access|ACL|Private)' -count=1
git diff --check
```

Name the new tests to match this command and verify the output includes them.
Stage only Task 4 paths; commit as `feat: paginate accessible projects without breaking existing clients`.

## Task 5: Discover and supervise multiple repository-bound runners

**Ownership:** CLI discovery, startup, runner status, and owned shutdown.
**Create:** `internal/vibeflowcli/review_supervisor.go`, `review_supervisor_test.go`.
**Modify:** `review_startup.go`, `review_startup_test.go`, `tui_review_startup.go`, `tui_review_startup_test.go`, `root.go`, `tui.go`, `tui_review_e2e_test.go`.
**Docs:** `README.md` and the existing CLI reference/configuration pages.
**Consumes:** Task 2 checkout matching, Task 3 capacity, and existing `reviewBackgroundID(serverURL, options)` identity and owned-runner shutdown.

**Interfaces, proposed:**

```go
type reviewBinding struct {
    Options reviewWatchOptions
    ProjectName string
    Repository reviewStartupRepository
    Checkouts []reviewStartupCheckout
    Problem string
}
type reviewDiscovery struct {
    Projects []Project
    Bindings []reviewBinding
    Problems map[int64]string
}
func knownReviewCheckoutPaths(cfg *Config, cwd string, sessions []SessionMeta) []string
func discoverReviewBindings(ctx context.Context, cfg *Config, paths []string,
    preferred map[string]string) (reviewDiscovery, error)

type reviewRunnerStatus struct {
    BindingID string
    Binding reviewBinding
    State string // starting, online, external, needs_checkout, failed
    Message string
}
func newReviewSupervisor(ctx context.Context, cfg *Config, configPath string) (*reviewSupervisor, error)
func (s *reviewSupervisor) Reconcile(discovery reviewDiscovery) []reviewRunnerStatus
func (s *reviewSupervisor) Close() error
```

`reviewSupervisor` privately holds its cancellation context, Task 3 capacity, a mutex, and `map[string]*reviewOwnedRunner` keyed by existing stable binding ID.
Serialize reconciliation under that mutex; execute it outside Bubble Tea's update goroutine and return immutable status snapshots in messages.
Cancellation precedes waiting for the mutex during `Close`, so an in-progress child startup can terminate.

- [ ] **Step 1: Reproduce single-project coverage with a real-checkout HTTP fixture.**

```go
func TestReviewDiscoveryIgnoresDefaultProject(t *testing.T) {
    repo, _ := reviewTestRepo(t)
    reviewTestGit(t, repo, "remote", "set-url", "origin", "https://github.com/acme/repo.git")
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/rest/v1/vibeflow/projects":
            io.WriteString(w, `[{"id":66,"name":"A"},{"id":67,"name":"B"}]`)
        case "/rest/v1/vibeflow/projects/66/pr-review-repositories", "/rest/v1/vibeflow/projects/67/pr-review-repositories":
            io.WriteString(w, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"}]}`)
        default:
            t.Errorf("unexpected request %s", r.URL.Path)
            http.NotFound(w, r)
        }
    }))
    defer server.Close()
    cfg := DefaultConfig()
    cfg.ServerURL, cfg.APIToken, cfg.DefaultProject = server.URL, "fixture", "66"
    got, err := discoverReviewBindings(context.Background(), cfg, []string{repo}, nil)
    if err != nil || len(got.Bindings) != 2 { t.Fatalf("coverage: %+v %v", got, err) }
    if got.Bindings[0].Options.ProjectID == got.Bindings[1].Options.ProjectID {
        t.Fatal("project identity collapsed")
    }
}
```

Run `go test ./internal/vibeflowcli -run TestReviewDiscovery -count=1`; expect failure before implementation.
Extend this fixture to three different repository remotes across two projects for the spec's end-user acceptance case.

- [ ] **Step 2: Implement discovery as reads plus remote validation.**

Read `NewStore().readFile()` once, not mutation-capable `Store.List()`.
Collect configured directory, CWD, history, and saved working/worktree paths; canonicalize and validate them using Task 2's helper.
Read the Task 4 opt-in project page contract until its cursor is empty; reject repeated cursors and duplicate project IDs.
For an older backend returning a bare array, use that response without retrying the same unsupported page and show a coverage warning if it contains 200 projects.
Do not claim complete discovery on that legacy capped response.
Repository enumeration uses its complete current array, with no invented cursor.
Validate each linked repository record before matching it.
For each project/repository binding, prefer a valid remembered path, deduplicate common Git directories, and retain genuinely separate clone choices as one unresolved binding.
Isolate per-project failures and exclude `default_project` from filtering logic.
Persist reusable checkout choices by stable binding identity without storing credentials or session consent; read the old single-choice preference format as a migration source.

- [ ] **Step 3: Replace single-runner startup ownership with reconciliation.**

```go
bindingID := reviewBackgroundID(cfg.ServerURL, binding.Options)
if existing := owned[bindingID]; existing != nil {
    continue
}
runner, err := startReviewOwnedWithCapacity(ctx, cfg, configPath, binding.Options, capacity)
if err == nil { owned[bindingID] = runner }
```

This is the core of `Reconcile`; `cfg`, `ctx`, `configPath`, `owned`, and `capacity` are its supervisor fields, and `binding` is the current discovery entry.
Classify genuine binding-lock contention as externally owned, never rename a runner to bypass it.
Retry failed starts only on a subsequent reconciliation or explicit retry, not on every render.
Keep healthy runners on transient discovery errors; stop accepting work for definitively removed/revoked scope.
Close all owned liveness pipes before waiting concurrently for their exits, then clean up verified-idle capacity files.

- [ ] **Step 4: Keep startup non-blocking and add runner-status actions.**

After the one session-level consent, start all resolved bindings and enter the main TUI even if some checkouts are missing.
Preserve the startup owl and accessibility behavior from Task 2.
Add a runner-status view reachable by `R`, with per-binding state and an Enter action to choose or enter a checkout for unresolved bindings.
Use existing picker/path-input components; revalidate an explicit path without substituting another path if it is wrong.
Trigger reconciliation on startup, explicit `r` refresh, known-checkout changes, and a one-minute discovery tick.
Queue polling retains its existing faster interval.
Keep provider/model selection session-wide; when the configured provider needs a model or is unavailable, show one actionable setup choice without prompting once per repository.

- [ ] **Step 5: Verify consent, partial failures, duplicate ownership, and shutdown.**

Extend the real `TestReviewTUIBinaryConsent` fixture rather than introducing a second terminal harness.
Assert zero registration after decline, three registrations after consent, continued main-screen access with a fourth missing checkout, and no extra registration after repeated refresh.
Use a pre-existing runner and verify it remains alive after TUI exit.
Use the task's discovery fixture for one-project `403`, stale preferences, numeric project names, malformed repository records, symlink aliases, and independent clones.
Run `go test ./internal/vibeflowcli -run 'TestReview(Discovery|Supervisor|Startup|TUIBinary)' -count=1 -timeout=180s`.
Review exact changed paths, run `git diff --check`, and commit as `feat: supervise review runners across linked projects`.

## Task 6: Persist and publish GitHub command acknowledgement

**Ownership:** Backend command feedback only.
**Create:** `axiomcloud/database/vibeflow_pr_review_command_feedback.go`, its test file, `axiomcloud/handlers/vibeflow_pr_review_command_feedback.go`, and its test file.
**Migrations, new:** `axiomcloud/migrations/postgres/20261001010000_add_pr_review_command_feedback.up.sql`, its `.down.sql` pair, and the same pair under `migrations/sqlite/`.
**Modify:** Existing `database/vibeflow_pr_review_events.go`, `handlers/vibeflow_pr_review_events.go`, `handlers/vibeflow_pr_review_publications.go`, `handlers/vibeflow_pr_review_publication_provider.go`, `github/pr_review_publication.go`, and corresponding tests, all under `axiomcloud/`.
**Consumes:** `EnqueuePRReviewEvent`, `ProcessPRReviewEvent`, and the existing publication worker, publisher validation, comment wrappers, lease fencing, and retry classification.

**Proposed internal interfaces:**

```go
func (db *DB) ClaimPRReviewCommandFeedback(ctx context.Context) (*PRReviewCommandFeedback, error)
func (db *DB) AckPRReviewCommandFeedback(ctx context.Context,
    p *PRReviewCommandFeedback, channel, remoteID string) error
func (db *DB) FailPRReviewCommandFeedback(ctx context.Context,
    p *PRReviewCommandFeedback, reason string, blocked bool, retryAt ...int64) error
func (h *VibeflowHandler) ProcessPRReviewCommandFeedback(ctx context.Context,
    p *database.PRReviewCommandFeedback) error
func prReviewCommandFeedbackText(reasonCode, acceptedURL string) string
```

`PRReviewCommandFeedback` is the database record defined by the schema below, with credential/fencing fields excluded from JSON.
The new GitHub adapter method follows existing `Client` method conventions:

```go
type PRReviewReaction struct { ID int64 `json:"id"`; Content string `json:"content"` }
func (c *Client) CreatePRReviewCommentReaction(ctx context.Context,
    installationID int64, owner, repo string, commentID int64,
    content string) (*PRReviewReaction, error)
```

- [ ] **Step 1: Reproduce silent command receipt through signed ingress.**

```go
func TestPRReviewCommandFeedbackDeduplicatesIngress(t *testing.T) {
    f := newPRReviewAPITest(t)
    registerPRReviewEventIngress(f)
    body := `{"action":"created","installation":{"id":999},"repository":{"id":11,"full_name":"acme/repo"},"issue":{"number":7,"pull_request":{"url":"https://api.github.com/repos/acme/repo/pulls/7"}},"comment":{"id":51,"body":"@vibeflow review","user":{"id":123,"login":"creator","type":"User"}}}`
    for _, delivery := range []string{"one", "one", "two"} {
        rr := postPRReviewGitHubEvent(t, f, "/webhook", "issue_comment", delivery, body, "event-secret")
        prReviewAPIStatus(t, rr, http.StatusAccepted)
    }
    first, err := f.db.ClaimPRReviewCommandFeedback(t.Context())
    if err != nil || first == nil { t.Fatalf("missing acknowledgement: %v", err) }
    second, err := f.db.ClaimPRReviewCommandFeedback(t.Context())
    if err != nil || second != nil { t.Fatalf("duplicate feedback: %+v %v", second, err) }
}
```

Extend the current command parser/ingress fixtures for bad signature, edited comments, bot authors, extra prose, and one repository linked to two projects.
The database row count and remote side-effect count remain one per command identity in that organization, not one per event/project.

- [ ] **Step 2: Add the durable receipt and connect it to existing intake.**

```sql
CREATE TABLE vibeflow_pr_review_command_feedback (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL,
  provider TEXT NOT NULL,
  provider_host TEXT NOT NULL,
  repository_id TEXT NOT NULL,
  pr_number BIGINT NOT NULL,
  comment_id TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  generation BIGINT NOT NULL DEFAULT 1,
  outcome TEXT NOT NULL DEFAULT 'received',
  reason_code TEXT NOT NULL DEFAULT '',
  job_id TEXT,
  state TEXT NOT NULL DEFAULT 'pending',
  attempts INTEGER NOT NULL DEFAULT 0,
  claim_token TEXT NOT NULL DEFAULT '',
  claim_generation BIGINT NOT NULL DEFAULT 0,
  lease_expires_at BIGINT NOT NULL DEFAULT 0,
  next_attempt_at BIGINT NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  publisher_id TEXT NOT NULL DEFAULT '',
  reaction_id TEXT NOT NULL DEFAULT '',
  reply_comment_id TEXT NOT NULL DEFAULT '',
  reply_generation BIGINT NOT NULL DEFAULT 0,
  restore_needed INTEGER NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  UNIQUE (organization_id, provider, provider_host, repository_id, comment_id)
);
CREATE INDEX idx_pr_review_command_feedback_due
  ON vibeflow_pr_review_command_feedback(state, next_attempt_at, lease_expires_at);
```

Use this schema for both engines, adapting existing migration style without altering historical migrations.
The down migration drops only this new index/table and must be tested only against an isolated database.
Before adding files, verify the proposed version is unused and still sorts after existing migrations.

Insert/reuse feedback transactionally inside recognized GitHub command event enqueue, before webhook success.
Keep per-project scheduling events separate; use organization, provider, host, repository ID, and comment ID to find all events contributing to one feedback outcome.
Never create an unauthorized job merely to satisfy the existing job-publication foreign key.
Keep the row independent of event-retention deletion while delivery is outstanding.
Different authenticated organizations retain isolated receipt ownership; never borrow another organization's publisher or private job link.

Update the receipt in the same transaction that finalizes a command event.
An accepted authorized job supplies its sticky comment link when available; unresolved events defer a rejection; only an entirely blocked command gets the fixed rejection response.
Reuse publication generations, claim tokens, retry timing, and stale-write restoration semantics for both channels, named `reaction` and `reply`.

- [ ] **Step 3: Add reaction delivery and marker-bound reply recovery to the existing worker.**

Use POST `/repos/{owner}/{repo}/issues/comments/{comment_id}/reactions` with `{"content":"eyes"}` through `prReviewPublicationRequest`.
Both `200` and `201` are success; retrying the same publisher/comment/content returns the existing reaction, so do not add a preflight reaction-list call.
Pin and validate the publisher before writes, including retries after a credential change.
See the [official GitHub reaction contract](https://docs.github.com/en/rest/reactions/reactions#create-reaction-for-an-issue-comment) for the required Issues write permission.

Alternate bounded feedback and job-publication work in `RunPRReviewPublications` so unavailable reactions cannot starve final results.
Use one deterministic feedback marker per receipt, separate from the job marker, and reconcile existing publisher-owned comments before retrying a possibly successful create.
If bounded history cannot establish whether a prior create succeeded, retain a retry/attention state instead of blindly creating a duplicate.
An accepted reply links to the job's existing sticky status comment; a blocked reply does not disclose project names or account bindings.
While the sticky comment is not yet available, publish accepted status with the existing validated AxiomCloud destination and update the same reply when its GitHub link arrives.
Waiting for that link is not a failed delivery attempt and must not exhaust the feedback retry budget.
Use these fixed reason categories: `authorization`, `policy_disabled`, `budget_required`, `request_changed`, and `unavailable`.
Unknown reasons produce the generic unavailable message, never raw errors.

```go
func TestPRReviewCommandFeedbackDoesNotRenderRawErrors(t *testing.T) {
    text := prReviewCommandFeedbackText("provider-secret-canary", "")
    if strings.Contains(text, "canary") || strings.Contains(text, "account_id") {
        t.Fatalf("unsafe feedback: %s", text)
    }
    text = prReviewCommandFeedbackText("authorization", "")
    if !strings.Contains(text, "connected GitHub identity") {
        t.Fatalf("missing corrective action: %s", text)
    }
}
```

- [ ] **Step 4: Verify lost responses, authorization rejection, and retry isolation.**

Extend `newPRReviewPublicationHTTP` with a reaction endpoint storing its first reaction before dropping the response, then returning the same ID with `200` on retry.
Use its existing `loseComment`, `beforeComment`, and `afterComment` controls for reply recovery and stale-write tests.
Test absent identity, read-only repository permission, revoked project access, disabled policy, and exhausted budget using existing command-authority fixtures.
Require unchanged review budgets, no unauthorized attempts, one reaction, one reply, preserved job publication, and no provider-error canary.
Test `429`, `5xx`, missing Issues write permission, publisher changes, worker restarts, and out-of-order acknowledgements.

- [ ] **Step 5: Run focused backend tests and commit.**

```sh
go test ./database ./handlers ./github -run 'TestPRReview.*(Command|Feedback|Publication)' -count=1
go test -race ./database ./handlers ./github -run 'TestPRReview.*(Command|Feedback|Publication)' -count=1
git diff --check
```

Stage only Task 6 paths and new migrations; commit as `feat: acknowledge review commands and explain blocked requests`.

## Task 7: Persist truthful attempt progress and render one live checklist

**Ownership:** Backend renewal, progress projection, and sticky renderer.
**Migrations, new:** `axiomcloud/migrations/{postgres,sqlite}/20261001020000_add_pr_review_attempt_progress.{up,down}.sql`.
**Modify:** `axiomcloud/database/vibeflow_pr_review_execution.go`, `vibeflow_pr_review_sessions.go`, `vibeflow_pr_review_summaries.go`, `vibeflow_pr_review_publications.go`; `axiomcloud/handlers/vibeflow_pr_review_execution.go`, `vibeflow_pr_review_publication_render.go`, and corresponding tests.
**Consumes:** Existing leased attempt renewal and `withPRReviewExecution` transaction/authority checks.

**Additive wire contract:**

```go
type PRReviewProgressInput struct {
    HeadSHA string `json:"head_sha"`
    BaseSHA string `json:"base_sha"`
    CheckoutPrepared bool `json:"checkout_prepared"`
    ReviewCompleted bool `json:"review_completed"`
}
type PRReviewProgress struct {
    ReportingVersion int `json:"reporting_version"`
    RoundID string `json:"round_id"`
    RoundNumber int `json:"round_number"`
    AttemptNumber int `json:"attempt_number"`
    HeadSHA string `json:"head_sha"`
    BaseSHA string `json:"base_sha"`
    RequestAccepted bool `json:"request_accepted"`
    RunnerAssigned bool `json:"runner_assigned"`
    CheckoutPreparedAt int64 `json:"checkout_prepared_at"`
    ReviewCompletedAt int64 `json:"review_completed_at"`
    ResultRecorded bool `json:"result_recorded"`
    State string `json:"state"`
}
```

Add `Progress *PRReviewProgress` to summary, session, and publication-snapshot DTOs.
Do not expose `PRReviewAttempt.ID`, which is a fencing credential rather than a public attempt identifier.
Keep execution envelope `version: 2`; add `progress_reporting_version: 1` on claim/get/renew.
Preserve existing database renewal callers with an optional final argument:

```go
func (db *DB) RenewPRReviewExecution(ctx context.Context, e PRReviewExecutor,
    id, token string, progress ...*PRReviewProgressInput) (*PRReviewAttempt, error)
```

- [ ] **Step 1: Add failing HTTP fencing and legacy-client tests.**

```go
func TestPRReviewExecutionHTTPProgress(t *testing.T) {
    f, job := newPRReviewExecutionAPITest(t, "manual", 3)
    runner := registerPRReviewAPIRunner(t, f, f.creator, "local")
    a := prReviewAPIAttempt(t, f.do(t, f.creator, http.MethodPost,
        runner+"/jobs/"+job.ID+"/claim", map[string]any{"request_id":"progress"}))
    renew := runner+"/jobs/"+job.ID+"/attempts/"+a.ID+"/renew"
    body := map[string]any{"progress": map[string]any{
        "head_sha":a.Round.HeadSHA, "base_sha":a.Round.BaseSHA,
        "checkout_prepared":true, "review_completed":false,
    }}
    prReviewAPIAttempt(t, f.do(t, f.creator, http.MethodPost, renew, body))
    prReviewAPIAttempt(t, f.do(t, f.creator, http.MethodPost, renew, map[string]any{}))
    path := fmt.Sprintf("/projects/%d/pr-review-summaries/%s", f.project.ID, job.ID)
    rr := f.do(t, f.creator, http.MethodGet, path, nil)
    prReviewAPIStatus(t, rr, http.StatusOK)
    var summary database.PRReviewSummary
    if err := json.Unmarshal(rr.Body.Bytes(), &summary); err != nil { t.Fatal(err) }
    if summary.Progress == nil || summary.Progress.CheckoutPreparedAt == 0 || summary.Progress.ResultRecorded {
        t.Fatalf("incorrect progress: %+v", summary.Progress)
    }
    if strings.Contains(rr.Body.String(), a.ID) { t.Fatal("fencing credential leaked") }
}
```

Add wrong head/base, another runner/user, expired lease, completed attempt, and superseding-revision cases to this fixture before implementing persistence.

- [ ] **Step 2: Add cumulative milestone storage in the existing renewal transaction.**

```sql
ALTER TABLE vibeflow_pr_review_attempts ADD COLUMN progress_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE vibeflow_pr_review_attempts ADD COLUMN checkout_prepared_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE vibeflow_pr_review_attempts ADD COLUMN review_completed_at BIGINT NOT NULL DEFAULT 0;
```

Use paired down migrations following the repository's supported SQLite/PostgreSQL column-removal conventions.
Before renewal succeeds, validate progress against the same active executor, runner, attempt, lease, round, head/base, and round-deadline checks already applied by `managedPRReviewAttempt`/`activePRReviewAttempt`.
Reject more than one variadic progress argument and reject malformed or mismatched revisions.
Set timestamps from server time only when the corresponding incoming flag is true and the stored timestamp is zero.
Ignore false flags for already-observed milestones; reject review completion without stored or same-report checkout evidence.
The server's accepted-result transaction alone controls `ResultRecorded`.
Accept an empty `{}` renewal without detailed progress from older clients.

- [ ] **Step 3: Share one safe projection and checklist renderer.**

Populate the current active round/attempt first; when none is active, use only the latest relevant terminal attempt at the current revisions.
A fresh queued round, including a same-SHA rerun, starts with a fresh checklist rather than inheriting earlier completed milestones.
Exclude heartbeat timestamps and lease extensions from the publication semantic digest so idle renewals do not generate comment writes.
Represent reporting version zero as unreported detail, not an inferred successful checkout.

Render the existing sticky comment with these five literal rows:

```text
- [x] Request accepted
- [x] Runner assigned
- [ ] Checkout prepared - in progress
- [ ] Code review completed
- [ ] Result recorded
```

Derive each box from `PRReviewProgress`, never elapsed time.
Add waiting, failure, cancellation, and unavailable annotations without checking unfinished boxes.
Preserve existing findings, sanitization, content bounds, revision links, and check-success rules.
Use validated repository/revision/path components for source links, not model-supplied arbitrary URLs.

- [ ] **Step 4: Verify publication transitions and stale-state rejection.**

Drive the existing `newPRReviewPublicationHTTP` fixture through claim, checkout, completed review, accepted result, and a new revision.
Assert one sticky comment and increasing meaningful updates, unchanged generation for empty heartbeat, no stale green check, and reset state for same-SHA new rounds.
Use existing lost-response and stale-write restoration controls and assert repeated/decreasing progress reports cannot regress timestamps.
Test public DTO serialization for the absence of attempt tokens, prompts, model output, and environment canaries.

- [ ] **Step 5: Verify both database engines and commit.**

```sh
go test ./database ./handlers -run 'TestPRReview.*(Progress|Execution|Summary|Session|Publication)' -count=1
go test -race ./database ./handlers -run 'TestPRReview.*(Progress|Execution|Publication)' -count=1
git diff --check
```

Run existing isolated PostgreSQL migration/lease tests when their test service is available; do not substitute the user's live database.
If unavailable, record that gap for Task 10 rather than claiming PostgreSQL verification.
Stage only Task 7 paths and new migrations; commit as `feat: publish revision-bound review progress`.

## Task 8: Report CLI milestones through the existing lease renewal

**Ownership:** CLI progress transport only; wait for Task 3 edits to `review_watch.go` to finish.
**Modify:** `internal/vibeflowcli/review_client.go`, `review_watch.go`, `review_recovery_test.go`, and `review_execution_diagnostics_test.go`.
**Create:** `internal/vibeflowcli/review_progress_test.go`.
**Consumes:** Task 7's additive capability and renewal wire contract.
**Produces:** Authenticated cumulative checkout/completion reports without changing execution isolation or ordinary session behavior.

**Proposed signatures:**

```go
// Add to reviewExecution:
ProgressReportingVersion int `json:"progress_reporting_version,omitempty"`

type reviewProgressInput struct {
    HeadSHA string `json:"head_sha"`
    BaseSHA string `json:"base_sha"`
    CheckoutPrepared bool `json:"checkout_prepared"`
    ReviewCompleted bool `json:"review_completed"`
}
func reviewRenewBody(execution reviewExecution, checkoutPrepared, reviewCompleted bool) any
```

- [ ] **Step 1: Write the compatibility test before touching renewal.**

```go
func TestReviewRenewBodyCapability(t *testing.T) {
    _, e := reviewTestRepo(t)
    data, err := json.Marshal(reviewRenewBody(*e, true, false))
    if err != nil || string(data) != "{}" { t.Fatalf("legacy body: %s %v", data, err) }
    e.ProgressReportingVersion = 1
    data, err = json.Marshal(reviewRenewBody(*e, true, false))
    if err != nil { t.Fatal(err) }
    var body struct { Progress reviewProgressInput `json:"progress"` }
    if err := json.Unmarshal(data, &body); err != nil { t.Fatal(err) }
    if body.Progress.HeadSHA != e.Attempt.Round.HeadSHA ||
        body.Progress.BaseSHA != e.Attempt.Round.BaseSHA ||
        !body.Progress.CheckoutPrepared || body.Progress.ReviewCompleted {
        t.Fatalf("incorrect progress payload: %+v", body.Progress)
    }
}
```

Run `go test ./internal/vibeflowcli -run TestReviewRenewBodyCapability -count=1`; expect missing symbols initially.

- [ ] **Step 2: Implement capability-gated bodies and cumulative stage notification.**

```go
func reviewRenewBody(e reviewExecution, prepared, completed bool) any {
    if e.ProgressReportingVersion != 1 { return struct{}{} }
    return struct { Progress reviewProgressInput `json:"progress"` }{
        Progress: reviewProgressInput{
            HeadSHA: e.Attempt.Round.HeadSHA, BaseSHA: e.Attempt.Round.BaseSHA,
            CheckoutPrepared: prepared, ReviewCompleted: completed,
        },
    }
}
```

In `execute`, retain cumulative milestone bits in `atomic.Uint32` and signal a buffered size-one channel only when a new milestone becomes true.
The existing renewal goroutine selects that channel as well as its heartbeat timer, so stage changes request prompt renewal without spawning a second renewal worker.
Keep the timer bounded by the last acknowledged lease expiry, including while publication endpoints are unavailable.
Mark checkout prepared only after exact-object checkout and repository validation succeed.
Mark code review completed only after the provider exits successfully and the result passes existing parsing/validation.
Never mark success on process start, on an arbitrary output line, or on an error result.
Send `{}` if the capability is absent or unsupported; do not retry a rejected progress body as legacy `{}` to mask an authorization/validation error.

- [ ] **Step 3: Exercise real stage timing and lease cancellation in existing runner fixtures.**

Use the existing fake provider with a readiness barrier: hold it after checkout, observe prepared progress at the HTTP renewal endpoint, then release it and observe accepted completion.
Add cases for checkout failure, malformed provider result, lost renewal response, old backend without capability, and lease rejection after a stage signal.
Assert only one renewal loop exists, the capability never enters model environment/prompt, and no stale attempt continues after its lease expires.
If an interim stage cannot be delivered before termination, preserve that uncertainty; accepted results may establish completion, but missing checkout evidence must not be fabricated.
Keep result submission on the durable receipt path regardless of best-effort interim visibility.

- [ ] **Step 4: Run the focused and race suites and commit.**

```sh
go test ./internal/vibeflowcli -run 'TestReview(Renew|Progress|Lease|Execution|SavedResult)' -count=1 -timeout=180s
go test -race ./internal/vibeflowcli -run 'TestReview(Renew|Progress|Lease|Execution)' -count=1 -timeout=180s
git diff --check
```

Stage only Task 8 files; commit as `feat: report review milestones through leased renewals`.

## Task 9: Make cross-project reviews navigable and safe

**Ownership:** CLI review reads, grouping, detail view, links, and diagnostics.
**Create:** `internal/vibeflowcli/tui_review_detail.go`, `tui_review_detail_test.go`.
**Modify:** `internal/vibeflowcli/review_sessions.go`, `review_visibility_test.go`, `tui.go`, `root.go`, and `docs/VibeFlow-CLI/docs/cli-reference.md`.
**Consumes:** Task 4 project enumeration, Task 5 discovery/status snapshots, Task 7 safe progress, and existing summary/history/findings APIs.

**Existing read routes to reuse:**

```text
GET /projects/{id}/pr-review-summaries?after_id=<cursor>&limit=25
GET /projects/{id}/pr-review-summaries/{job_id}
GET /projects/{id}/pr-review-summaries/{job_id}/findings?after_id=<cursor>&limit=25
GET /projects/{id}/pr-review-sessions?job_id=<job_id>&after_id=<cursor>&limit=25
```

The summaries list contains queued jobs even when no attempt/session exists.
Keep headless `list --project ... --reviews-after ...` behavior unchanged; the new multi-project browsing is TUI behavior.

**Proposed client contracts:**

```go
type reviewSummaryJob struct {
    reviewJob
    ProjectID int64 `json:"project_id"`
    Number int64 `json:"number"`
    Details struct {
        URL string `json:"url"`
        BaseRepositoryName string `json:"base_repository_name"`
    } `json:"details"`
}
type reviewProgress struct {
    ReportingVersion int `json:"reporting_version"`
    RoundID string `json:"round_id"`
    RoundNumber int `json:"round_number"`
    AttemptNumber int `json:"attempt_number"`
    HeadSHA string `json:"head_sha"`
    BaseSHA string `json:"base_sha"`
    RequestAccepted bool `json:"request_accepted"`
    RunnerAssigned bool `json:"runner_assigned"`
    CheckoutPreparedAt int64 `json:"checkout_prepared_at"`
    ReviewCompletedAt int64 `json:"review_completed_at"`
    ResultRecorded bool `json:"result_recorded"`
    State string `json:"state"`
}
type reviewSummary struct {
    Review reviewSummaryJob `json:"review"`
    Summary string `json:"summary"`
    FindingCount int `json:"finding_count"`
    UnresolvedBlockers int `json:"unresolved_blockers"`
    Progress *reviewProgress `json:"progress"`
    ReviewSessions []reviewSession `json:"review_sessions"`
    ReviewSessionsNext string `json:"review_sessions_next_after_id"`
    Runner struct { State, Name, Reason string } `json:"runner"`
    Publication *struct {
        State string `json:"state"`
        LastError string `json:"last_error"`
    } `json:"publication"`
}
type reviewSummariesPage struct {
    Summaries []reviewSummary `json:"summaries"`
    NextAfterID string `json:"next_after_id"`
}
func (c *Client) listReviewSummaries(ctx context.Context, projectID int64, after string) (reviewSummariesPage, error)
func (c *Client) getReviewSummary(ctx context.Context, projectID int64, jobID string) (reviewSummary, error)
func (s reviewSummary) row() SessionRow
func reviewExternalCommand(goos, rawURL string) (*exec.Cmd, error)
func (m Model) activateSession(name string) (tea.Model, tea.Cmd)
```

Add this read-only finding projection in `review_sessions.go`; current execution code reads findings as raw JSON and provides no reusable typed public finding DTO.

```go
type reviewFinding struct {
    ID string `json:"id"`
    JobID string `json:"job_id"`
    Title string `json:"title"`
    Severity string `json:"severity"`
    Path string `json:"path"`
    Line int `json:"line"`
    State string `json:"state"`
    HeadSHA string `json:"head_sha"`
    BaseSHA string `json:"base_sha"`
    Trigger string `json:"trigger"`
    Impact string `json:"impact"`
    Evidence string `json:"evidence"`
    Verification string `json:"verification"`
}
type reviewFindingsPage struct {
    Findings []reviewFinding `json:"findings"`
    NextAfterID string `json:"next_after_id"`
}
func (c *Client) listReviewSummaryFindings(ctx context.Context,
    projectID int64, jobID, after string) (reviewFindingsPage, error)
```

Validate every finding's job identity and page bounds before rendering it through terminal sanitization.
No detail reads may call authenticated execution/brief endpoints or put attempt fencing credentials in the UI model.

- [ ] **Step 1: Replace the deliberate Enter no-op regression with safe navigation tests.**

```go
func TestReviewEnterOpensReadOnlyDetail(t *testing.T) {
    row := (reviewSession{ProjectID:13, JobID:"job", SessionID:"session"}).row()
    m := Model{config:DefaultConfig(), sessions:[]SessionRow{row},
        repoRootCache:map[string]string{}, collapsedGroups:map[string]bool{}}
    next, _ := m.Update(tea.KeyPressMsg{Code:tea.KeyEnter})
    got := next.(Model)
    if got.activeView != ViewReviewDetail { t.Fatal("review detail did not open") }
    if got.attachSessionCmd(row.Name) != nil { t.Fatal("review became attachable") }
}

func TestReviewSessionNamesIncludeProject(t *testing.T) {
    a := (reviewSession{ProjectID:13, SessionID:"same"}).row()
    b := (reviewSession{ProjectID:14, SessionID:"same"}).row()
    if a.Name == b.Name { t.Fatal("cross-project row collision") }
}
```

Preserve the existing negative assertions for deletion, branch changes, persona controls, tmux capture, and ordinary metadata.
Use real `tea.KeyEnter`, not text spelling that accidentally tests a different key.

- [ ] **Step 2: Add per-project summary paging and immutable read results.**

Maintain cursor, next cursor, warning, and request generation separately for each project inside `Model`.
Increment a project's generation before dispatch and include project ID, requested cursor, and generation in its result message.
Only `Update` mutates these maps; asynchronous commands receive value snapshots and use a bounded fanout of four requests.
Accept a result only when its project, cursor, and generation still match.
On `401`/`403`/authoritative `404`, clear that project's rows and selected details; transient failures preserve them with a stale warning.
Do not let one project's failure erase another project's history.

Use project-scoped keys: `review:<project-id>:session:<session-id>` for history and `review:<project-id>:job:<job-id>` for summary rows.
Top-level rows represent jobs; attempt history belongs in the detail view, avoiding duplicate active/history rows for one job.
Group by project and repository, using repository-link/provider identity rather than display names as the internal grouping key.
Keep the default project first but never filter out the other accessible projects.
Review browsing remains read-only and available when runner consent is declined; declining execution must not hide existing reviews.

Extend `TestReviewSessionsDelayedHTTPPageCannotWin` to two projects, with one delayed old-page response and one access revocation while a valid other-project response is pending.
Add a queued summary with no sessions and verify it remains selectable and shows waiting for runner/checkout.
Reject oversized pages, repeated cursors, foreign project IDs, malformed job IDs, and duplicate rows using the existing bounded request pattern.

- [ ] **Step 3: Add the read-only detail view and share keyboard/mouse activation.**

Add `ViewReviewDetail` to `ViewState` and route both Enter and second-click activation through `activateSession`.
For an ordinary session it returns the existing attach command; for a review it opens detail state and requests the safe summary.
Keep `attachSessionCmd`'s managed-review guard unchanged.
Use these controls in detail: `o` opens the PR, `c` opens AxiomCloud, `r` refreshes, arrows/PageUp/PageDown scroll, and Esc returns without changing the selected job.
Use `]`/`[` for older/latest history of the selected project or detail's attempt history, not one global cursor shared by all projects.
Preserve global quit/cancel behavior and all ordinary-session controls outside the review view.

Show project, repository, revision, runner, attempt, checklist, summary/findings, publication failure, and stale/unavailable states through existing terminal sanitization and width limits.
Use the existing cloud destination `/ai/vibeflow?project=<id>` unless a more specific already-supported route is verified; do not invent a job URL.
For local diagnostics, correlate owned runner state and receipt with runner ID, job ID, session ID, and round ID before reading `last-provider-diagnostic.json` through `readReviewExecutionDiagnostic`.
Render only its validated category, stage, code, signal, and duration, never the receipt or attempt token.
Current code retains no provider transcript, so show `Review transcript unavailable` rather than adding raw-output persistence or attaching tmux.

- [ ] **Step 4: Validate external links and test the opener without launching it.**

```go
func TestReviewExternalCommandRejectsUnsafeLinks(t *testing.T) {
    for _, raw := range []string{"file:///tmp/a", "javascript:alert(1)",
        "https://user:secret@github.com/a/b", "https://github.com/a\n/b", "-rf"} {
        if _, err := reviewExternalCommand("darwin", raw); err == nil {
            t.Fatalf("accepted unsafe URL %q", raw)
        }
    }
    raw := "https://github.com/acme/repo/pull/7?x=$(touch%20ignored)"
    cmd, err := reviewExternalCommand("darwin", raw)
    if err != nil || len(cmd.Args) != 2 || cmd.Args[0] != "open" || cmd.Args[1] != raw {
        t.Fatalf("URL was not a literal opener argument: %+v %v", cmd, err)
    }
}
```

Parse with `net/url`, allow only absolute HTTP(S) with a nonempty host and no userinfo or control characters, then use `exec.Command("open", rawURL)` on Darwin and `exec.Command("xdg-open", rawURL)` on Linux.
Unsupported systems receive an actionable error with the safe displayed URL.
No shell, command substitution, or interpolated command string is involved.
An unavailable browser command becomes a TUI notice, not a TUI exit.

- [ ] **Step 5: Run navigation, history, and terminal regressions and commit.**

```sh
go test ./internal/vibeflowcli -run 'TestReview(Enter|Session|External|Managed|TUI|List)' -count=1 -timeout=180s
go test -race ./internal/vibeflowcli -run 'TestReview(Session|Managed|Enter|External)' -count=1
git diff --check
```

Test first-click preview, second-click open, keyboard parity, non-mouse use, 40-column layout, safe missing diagnostics, and backend responses containing terminal-control canaries.
Stage only Task 9 files; commit as `feat: open cross-project review details from the TUI`.

## Task 10: Prove the combined workflow and hand off verified commits

**Ownership:** Cross-repository acceptance and final review; no new product scope.
**Extend:** `internal/vibeflowcli/tui_review_e2e_test.go`, `axiomcloud/handlers/vibeflow_pr_review_command_feedback_test.go`, and existing backend publication/execution fixtures.
**Update:** Existing CLI reference/configuration pages with `review_concurrency`, runner-status navigation, review-detail controls, and truthful logging limitations.

- [ ] **Step 1: Exercise the complete CLI flow in isolated repositories.**

Expand the existing real-PTY fixture to three repositories/two projects and a fourth missing checkout.
Use fixture model processes with explicit start/finish barriers, not paid model calls.
Assert two active providers, zero early claim for the third, eventual execution after a slot releases, and continued UI access for the missing-checkout notice.
Exercise Enter/mouse review details, open-link command construction, current progress, quit, restart, and externally owned runner survival.
Keep same-repository PRs serial and verify an unresolved cleanup quarantine does not become new capacity on restart.

- [ ] **Step 2: Exercise the complete signed webhook/publication lifecycle.**

Use a real migrated isolated backend database and the existing HTTP GitHub protocol fixture.
Submit the signed recognized command, duplicate its delivery, process feedback, authorize and claim the job, renew with the Task 8 payload, accept the result, and process publication.
Assert one eyes reaction, one command feedback reply, one sticky progress comment, truthful milestone changes, final findings, and unchanged authorization/budget checks.
Repeat with authorization failure and an automatic PR-open policy; the first must visibly block and the second must queue without a command comment.
Feed the exact safe summary wire fixtures into the CLI client tests to catch cross-repository field-name or capability drift.
This is local protocol acceptance, not proof that the changes are deployed to UAT.

- [ ] **Step 3: Run final CLI checks with paid-provider acceptance disabled.**

```sh
env VIBEFLOW_REVIEW_PROVIDER_ACCEPTANCE= go test ./... -count=1
env VIBEFLOW_REVIEW_PROVIDER_ACCEPTANCE= go test -race ./... -count=1
go vet ./...
go build ./...
git diff --check
```

Run commands with sufficient per-test timeouts for the explicit 30-second timeout regression.
Report skipped PTY/process tests if their tools or platform are unavailable rather than claiming those cases passed.

- [ ] **Step 4: Run backend checks with its required Go 1.26.5 toolchain.**

```sh
env -u PR_REVIEW_POSTGRES_DSN go test ./database ./handlers ./github -count=1
env -u PR_REVIEW_POSTGRES_DSN go test -race ./database ./handlers ./github -run 'TestPRReview|Test.*Project.*Paginat' -count=1
go vet ./database ./handlers ./github
go build ./...
git diff --check
```

Extend `database/pr_review_migration_fixture_test.go` and `database/vibeflow_pr_reviews_postgres_test.go` to include the two new migration sets in their appropriate fixture stage.
Do not blindly append non-idempotent ALTER statements to the older test loop that deliberately applies historical CRA migrations twice.
Test fresh install, upgrade from the pinned baseline, and down/up round trips against isolated SQLite and disposable PostgreSQL databases.
`PR_REVIEW_POSTGRES_DSN` must name a disposable test database, never an existing UAT/production/user database; the PostgreSQL tests create and drop private schemas.
If no disposable service is available, report PostgreSQL validation as outstanding and do not claim the server changes are release-ready.
Fix trivial failures in touched paths; report unrelated baseline failures separately without hiding them.

- [ ] **Step 5: Review both complete diffs and hand off only the verified result.**

Run a fresh whole-branch review across both repositories, including uncommitted-work integration, process ownership, authorization, migration compatibility, and stale publication recovery.
With subagent-driven execution, retain the per-task review gates as well as this final cross-repository check.
Resolve material findings and rerun their regression tests before creating the final scoped commits.
Record commit IDs, exact tests run, skipped checks, and any required GitHub App Issues write permission in the handoff.
Do not stage personal configs or runtime artifacts, delete the preserved worktree, push/merge, change identity bindings, grant review budgets, or deploy without the relevant separate authorization.

## Plan self-review and execution gate

Spec coverage: existing work is Tasks 1-2; execution capacity/lifecycle is Task 3; complete discovery is Tasks 4-5; acknowledgement is Task 6; truthful backend/CLI progress is Tasks 7-8; cross-project navigation is Task 9; final acceptance is Task 10.
The five Review Focus conditions each have a named owning task and regression path.
New progress field names are identical between Tasks 7, 8, and 9, while private execution identifiers stay out of the safe DTOs.
No deployment, identity repair, budget grant, or raw transcript retention is implied by this plan.

Recommended execution method: subagent-driven, with sequential ownership within each repository and parallel CLI/backend tracks where interfaces are fixed.
The task boundaries involve process lifetime, durable external writes, and authorization, so independent task reviews are worth the extra contexts.
Native execution remains available: one implementer performs all tasks followed by a fresh whole-branch review.
Implementation begins only after the user reviews this plan and selects the execution method.
