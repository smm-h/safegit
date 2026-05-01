#!/usr/bin/env bash
# Bug #4: Amend doesn't check wip-locks.
#
# When a file is wip-locked, safegit commit refuses to touch it (exit 6).
# But safegit amend skips the check entirely and succeeds.
#
# Expected: amend refuses wip-locked files (same as commit)
# Actual: amend succeeds, defeating wip protection

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

# Make a commit with a file
echo "v1" > guarded.txt
safegit commit -q -m "add guarded" -- guarded.txt

# Wip the file (locks it)
echo "wip-content" > guarded.txt
safegit wip guarded.txt

# Verify commit is blocked
echo "v2" > guarded.txt
safegit commit -m "should fail" -- guarded.txt 2>/dev/null && COMMIT_RC=0 || COMMIT_RC=$?
if [[ $COMMIT_RC -ne 6 ]]; then
    echo "SETUP FAILED: commit should have been blocked with exit code 6, got $COMMIT_RC"
    exit 2
fi

# Now try amend with the same wip-locked file
echo "v3" > guarded.txt
safegit amend -- guarded.txt 2>amend_err.txt && AMEND_RC=0 || AMEND_RC=$?

if [[ $AMEND_RC -ne 0 ]]; then
    echo "PASS: amend correctly refused wip-locked file (exit $AMEND_RC)"
    exit 0
else
    echo "FAIL: amend succeeded on wip-locked file (should have been blocked)"
    exit 1
fi
