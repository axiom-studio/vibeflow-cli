#!/bin/sh
# Builds the seeded-defect fixture repository at $1: a base commit from
# testdata/base and a head commit that overlays testdata/head. Fixed
# identities and dates make both SHAs the same on every machine.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
dest=${1:?usage: build-fixture.sh <new directory>}
if [ -e "$dest" ]; then
	echo "build-fixture.sh: $dest already exists" >&2
	exit 1
fi
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME="Fixture Author" GIT_AUTHOR_EMAIL=fixture@example.invalid
export GIT_COMMITTER_NAME="Fixture Author" GIT_COMMITTER_EMAIL=fixture@example.invalid
g() { git -c maintenance.auto=false -c init.defaultBranch=main -c commit.gpgsign=false -C "$dest" "$@"; }
mkdir -p "$dest"
g init -q
cp -R "$here/testdata/base/." "$dest/"
g add -A
GIT_AUTHOR_DATE=2026-01-01T00:00:00Z GIT_COMMITTER_DATE=2026-01-01T00:00:00Z g commit -q -m "Linked repositories, plans, status check names and review findings"
cp -R "$here/testdata/head/." "$dest/"
g add -A
GIT_AUTHOR_DATE=2026-01-02T00:00:00Z GIT_COMMITTER_DATE=2026-01-02T00:00:00Z g commit -q -m "Webhook secrets, Starter plan gate, legacy check names, retention purge and diff anchors"
g remote add origin https://github.com/acme/review-fixture.git
