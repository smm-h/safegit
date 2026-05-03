#!/usr/bin/env bash
# Bug #9: Zero-length lock file blocks all commits.
#
# If safegit crashes between O_CREAT|O_EXCL and WriteString, a zero-length
# lock file is left behind. parsePID fails (no pid= line), IsStale returns
# an error, and the lock is never reclaimed. Commits hang until timeout.
#
# Expected: safegit detects the corrupt lock and reclaims it
# Actual: safegit waits for the full lock timeout then fails

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

# Set a short lock timeout so the test doesn't take forever
"$SAFEGIT" config lock.acquireTimeoutSeconds 3

# Create a zero-length lock file (simulates crash mid-create)
LOCK_DIR=".git/safegit/locks/refs/heads"
mkdir -p "$LOCK_DIR"
touch "$LOCK_DIR/main.lock"

# Try to commit -- should recover quickly, not hang for 3 seconds
echo "data" > file.txt
START=$(date +%s)
"$SAFEGIT" commit -m "after corrupt lock" -- file.txt 2>err.txt && RC=0 || RC=$?
END=$(date +%s)
ELAPSED=$((END - START))

if [[ $RC -eq 0 && $ELAPSED -lt 2 ]]; then
    echo "PASS: commit recovered from zero-length lock quickly (${ELAPSED}s)"
    exit 0
elif [[ $RC -eq 0 ]]; then
    echo "FAIL: commit succeeded but took ${ELAPSED}s (hung on corrupt lock)"
    exit 1
else
    echo "FAIL: commit failed (exit $RC) due to zero-length lock"
    cat err.txt
    exit 1
fi
