#!/usr/bin/env bash
# Bug #2: Reword has no CAS retry loop.
#
# When the branch tip moves between reword's RevParse and UpdateRef,
# reword fails with a hard error instead of retrying like commit/amend do.
#
# This test races a reword against a concurrent commit. The reword should
# either succeed (by retrying) or fail with a CAS-specific error.
#
# Expected: reword retries and succeeds (like commit does)
# Actual: reword fails with "branch tip moved" hard error

set -euo pipefail

SAFEGIT="$(cd "$(dirname "$0")/../.." && pwd)/safegit"
DIR=$(mktemp -d)
trap "rm -rf $DIR" EXIT
cd "$DIR"

git init --initial-branch=main -q
git config user.email "test@test.com"
git config user.name "Test"

echo "seed" > seed.txt
git add seed.txt && git commit -q -m "initial"

# Make a "$SAFEGIT" commit so we have something to reword
echo "file" > file.txt
"$SAFEGIT" commit -q -m "original message" -- file.txt

# Now race: start a concurrent commit in the background, then immediately reword
echo "racer" > racer.txt
"$SAFEGIT" commit -q -m "racing commit" -- racer.txt &
RACER_PID=$!

# Give the racer a tiny head start
sleep 0.05

# Try to reword -- if the racer lands first, this should retry but currently doesn't
"$SAFEGIT" reword -m "new message" 2>reword_err.txt && REWORD_RC=0 || REWORD_RC=$?
wait $RACER_PID 2>/dev/null || true

if [[ $REWORD_RC -eq 0 ]]; then
    echo "PASS: reword succeeded (either won the race or retried)"
    exit 0
else
    ERR=$(cat reword_err.txt)
    if [[ "$ERR" == *"branch tip moved"* ]]; then
        echo "FAIL: reword failed with 'branch tip moved' instead of retrying"
        echo "  error: $ERR"
        exit 1
    else
        echo "FAIL: reword failed with unexpected error"
        echo "  error: $ERR"
        exit 1
    fi
fi
