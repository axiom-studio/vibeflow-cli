# Vera seeded-defect evaluation

A release gate for changes to Vera, the PR reviewer that `vibeflow review-watch` runs.
It measures whether Vera catches real defects: the seven found on PR #197 after Vera had called it clean, rebuilt in miniature in a small fixture project, plus correct decoy changes that measure false positives.

## Shipping bar

A Vera change (prompt, runner policy, specialist passes, harness settings) ships only when a live run on this fixture:

1. catches at least 6 of the 7 seeded defects (`-min-caught 6`),
2. always catches `a-list-leaks-webhook-secret`, the leak in unchanged sibling code (marked `"required": true`), and
3. raises no blocker on a decoy (`-max-decoy-blockers 0`).

The scorer enforces all three by default and exits non-zero otherwise.
Advisories on decoys and unmatched findings lower precision but do not fail the bar; pass `-min-precision 0.7` to gate on it too.
Model output varies, so run it at least twice and report every run, not the best one.

## Run it live

A live run needs a person with a logged-in harness (`claude`, `codex`, ...) on the PATH; no VibeFlow account or network access to VibeFlow is needed.

```sh
cd vibeflow-cli
eval/vera/run.sh                              # Claude Code, your own setup
VERA_EVAL_PROVIDER=codex eval/vera/run.sh     # another harness
eval/vera/run.sh --isolated                   # Claude Code without your hooks, MCP servers and plugins
VERA_EVAL_SCORE="-min-precision 0.7" eval/vera/run.sh
```

`run.sh` builds the fixture repository, the CLI, the fake server and the scorer into a temporary work directory (`VERA_EVAL_WORK` to choose one), runs `vibeflow review-watch --once` against the fake server, and scores the result it submits.
Extra arguments go to `review-watch` (`--model`, `--timeout`, `--isolated`).
The runner uses its own `VIBEFLOW_ROOT` under the work directory and unsets `VIBEFLOW_URL` and `VIBEFLOW_TOKEN`, so your VibeFlow config and credentials are not used; the harness runs with your HOME and login.
A review takes as long as a real one (minutes); the result is kept at `<work>/result.json` and can be re-scored:

```sh
go run ./eval/vera/score -answers eval/vera/answers.json -result <work>/result.json
```

Exit status: 0 meets the bar, 1 below it, 2 no result (the review failed; `<work>/result.json` or `<work>/fakeserver.log` says why).

## Offline checks

`go test ./eval/...` runs without a model or network:

- `score` unit tests: matching by path and line range with a tolerance (default 3 lines), duplicates, decoys, thresholds, result envelopes.
- `TestFixtureMatchesAnswers`: the fixture builds reproducibly, compiles and vets, every answer's line range still contains its anchor text, and only the required defect lives in unchanged code.
- `TestRunScoresTheSubmittedResult`: `run.sh` end to end with a fake `claude` that reports every defect, under a temporary HOME.

## Layout

| Path | What it is |
|------|------------|
| `testdata/base/` | The fixture's base commit (a small Go module) |
| `testdata/head/` | Files the head commit adds or replaces |
| `build-fixture.sh` | Builds the fixture git repository from both trees, with fixed SHAs |
| `answers.json` | Seeded defects and decoys: id, path, line range, anchor, description |
| `prompt.txt` | The review prompt the server sends, copied from axiomcloud |
| `fakeserver/` | Local VibeFlow review API serving one job for the fixture |
| `score/` | The scorer |
| `run.sh` | Builds, reviews and scores |

## Seeded defects

| ID | Location | Defect from PR #197 |
|----|----------|---------------------|
| `a-list-leaks-webhook-secret` | `repos/list.go` (unchanged) | List endpoint still encodes the webhook secret; the two siblings were fixed |
| `b-untrusted-description-in-privileged-prompt` | `prompt/answer.go` | Untrusted PR description enters the project-context prompt; comments are filtered |
| `c-starter-gate-refuses-same-status` | `plan/gate.go`, `plan/status.go` | Starter gate refuses implementing to implementing |
| `d-signing-secret-check-then-act-race` | `webhooks/secret.go` | Read, send to provider, write whole record: overlapping calls diverge |
| `e-legacy-flag-before-base-branch-rule` | `gates/name.go` | Repository-wide legacy flag wins over the base branch's required check |
| `f-retention-purge-deletes-dismissals` | `findings/store.go`, `retention/purge.go` | Purge cascades to dismissals other reviews read |
| `g-diff-parser-trusts-header-lines-in-hunks` | `diffparse/parse.go` | An added `++ ` line is taken as a `+++` file header |

Decoys: `redacted` in `repos/handlers.go`, `Verify` in `webhooks/verify.go`, and `web/SecretField.tsx`.

## Maintaining it

- Keep `prompt.txt` in step with production: copy `prReviewExecutionPrompt` from `axiomcloud/handlers/vibeflow_pr_review_execution.go` on axiomcloud `main` (copied from `59c0a0304`).
- To add a defect, edit `testdata/head/`, add its entry to `answers.json` with an anchor, and run `go test ./eval/...`; the anchors catch line drift.
- Never describe a defect in fixture comments or commit messages; the fixture must read like an ordinary change.
- The fake server sends an empty brief (no project context, no CI results) and no prior findings, so this measures a first review only.
