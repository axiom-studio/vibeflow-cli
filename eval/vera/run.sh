#!/bin/sh
# Runs Vera on the seeded-defect fixture and scores the review.
#
#   eval/vera/run.sh [review-watch flags...]
#
# Builds the fixture repository, the vibeflow CLI, the fake VibeFlow server
# and the scorer; runs `vibeflow review-watch --once` against the fake server
# with the harness named by VERA_EVAL_PROVIDER (default claude); then scores
# the submitted result against answers.json. Exit status: 0 at or above the
# bar, 1 below it, 2 when the review did not produce a result.
#
# VERA_EVAL_WORK      work directory (default: a new temporary directory)
# VERA_EVAL_PROVIDER  review-watch --provider (default: claude)
# VERA_EVAL_SCORE     extra scorer flags, e.g. "-min-caught 7"
set -eu
here=$(cd "$(dirname "$0")" && pwd)
module=$(cd "$here/../.." && pwd)
work=${VERA_EVAL_WORK:-$(mktemp -d "${TMPDIR:-/tmp}/vera-eval.XXXXXX")}
provider=${VERA_EVAL_PROVIDER:-claude}
mkdir -p "$work/bin"
echo "Work directory: $work"

"$here/build-fixture.sh" "$work/fixture"
(cd "$module" && go build -o "$work/bin/vibeflow" ./cmd/vibeflow && go build -o "$work/bin/fakeserver" ./eval/vera/fakeserver && go build -o "$work/bin/score" ./eval/vera/score)

rm -f "$work/addr" "$work/result.json"
"$work/bin/fakeserver" -repo "$work/fixture" -prompt "$here/prompt.txt" -result "$work/result.json" -addr-file "$work/addr" 2>"$work/fakeserver.log" &
server=$!
trap 'kill "$server" 2>/dev/null || true' EXIT INT TERM
tries=0
until [ -s "$work/addr" ]; do
	tries=$((tries + 1))
	if [ "$tries" -gt 100 ] || ! kill -0 "$server" 2>/dev/null; then
		cat "$work/fakeserver.log" >&2
		echo "run.sh: the fake server did not start" >&2
		exit 2
	fi
	sleep 0.1
done

# A separate CLI root keeps the runner's state and config out of
# ~/.vibeflow-cli, and no real VibeFlow credential reaches the fake server;
# the harness itself runs with the caller's HOME and login.
unset VIBEFLOW_URL VIBEFLOW_TOKEN
mkdir -p "$work/root"
printf 'server_url: http://%s\napi_token: vera-eval\n' "$(cat "$work/addr")" >"$work/root/config.yaml"
status=0
VIBEFLOW_ROOT="$work/root" "$work/bin/vibeflow" review-watch --once --project 1 --repository-link 7 --repo "$work/fixture" --provider "$provider" --name vera-eval "$@" || status=$?
if [ ! -s "$work/result.json" ]; then
	echo "run.sh: review-watch exited $status without submitting a result; see $work/fakeserver.log" >&2
	exit 2
fi
# shellcheck disable=SC2086 # VERA_EVAL_SCORE is a list of flags.
"$work/bin/score" -answers "$here/answers.json" -result "$work/result.json" ${VERA_EVAL_SCORE:-}
