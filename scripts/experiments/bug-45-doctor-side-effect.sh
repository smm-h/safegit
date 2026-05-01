#!/usr/bin/env bash
# Bug #45: doctor deletes orphan tmp dirs as a side effect.
#
# safegit doctor is a diagnostic command but it calls GarbageCollect
# which removes orphan tmp directories. A health check shouldn't mutate state.
#
# Expected: doctor reports orphan dirs but doesn't delete them
# Actual: doctor deletes them (same as gc)

set -euo pipefail

DIR=$(mktemp -d)
trap "rm -rf $DIR" EXIT
cd "$DIR"

git init --initial-branch=main -q
git config user.email "test@test.com"
git config user.name "Test"
echo "seed" > seed.txt
git add seed.txt && git commit -q -m "initial"
safegit init -q

# Create a fake orphan tmp dir with a dead PID
ORPHAN_DIR=".git/safegit/tmp/999999999-deadbeef"
mkdir -p "$ORPHAN_DIR"
echo "fake-index" > "$ORPHAN_DIR/index"

# Run doctor
safegit doctor >/dev/null 2>&1

# Check if the orphan dir still exists
if [[ -d "$ORPHAN_DIR" ]]; then
    echo "PASS: doctor preserved orphan dir (diagnostic only)"
    exit 0
else
    echo "FAIL: doctor deleted orphan dir (should only report, not clean)"
    exit 1
fi
