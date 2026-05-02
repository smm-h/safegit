#!/usr/bin/env bash
# Two concurrent safegit commits to different parent files while a submodule
# exists. Verifies CAS retry works and the submodule entry is not corrupted.
#
# Setup: parent repo with a submodule. Two agents each create a file and
# commit concurrently.
# Expected: both files and the submodule entry appear in the final tree.
# Actual: (bug would lose one commit, or corrupt the submodule entry)

set -euo pipefail

SAFEGIT="$(cd "$(dirname "$0")/../.." && pwd)/safegit"
DIR=$(mktemp -d)
trap "rm -rf $DIR" EXIT
cd "$DIR"

# Create the repo that will become the submodule
mkdir sub-origin
cd sub-origin
git init --initial-branch=main -q
git config user.email "test@test.com"
git config user.name "Test"
echo "sub content" > sub-file.txt
git add sub-file.txt && git commit -q -m "sub initial"
cd "$DIR"

# Create the parent repo and add the submodule
mkdir parent
cd parent
git init --initial-branch=main -q
git config user.email "test@test.com"
git config user.name "Test"
echo "seed" > seed.txt
git add seed.txt && git commit -q -m "initial"

# Allow local file:// transport for submodule clone
git config protocol.file.allow always
git submodule add -q "$DIR/sub-origin" mysub
git commit -q -m "add submodule"

# Record the submodule entry before concurrent commits
SUB_ENTRY_BEFORE=$(git ls-tree HEAD mysub)

"$SAFEGIT" config commit.casMaxAttempts 50

# Create the files that will be committed concurrently
echo "content-a" > file-a.txt
echo "content-b" > file-b.txt

# Launch two concurrent safegit commits
"$SAFEGIT" commit -m "add a" -- file-a.txt 2>err-a.txt &
PID_A=$!
"$SAFEGIT" commit -m "add b" -- file-b.txt 2>err-b.txt &
PID_B=$!

# Wait for both
wait $PID_A && RC_A=0 || RC_A=$?
wait $PID_B && RC_B=0 || RC_B=$?

if [[ $RC_A -ne 0 ]]; then
    echo "FAIL: agent A exited with $RC_A"
    cat err-a.txt
    exit 1
fi
if [[ $RC_B -ne 0 ]]; then
    echo "FAIL: agent B exited with $RC_B"
    cat err-b.txt
    exit 1
fi

# Check 1: 3 commits total (initial + add-submodule + 2 concurrent = 4 actually)
# Correction: initial + add-submodule = 2 base commits, then +2 = 4 total
COUNT=$(git rev-list --count HEAD)
if [[ "$COUNT" -ne 4 ]]; then
    echo "FAIL: expected 4 commits, got $COUNT"
    git log --oneline
    exit 1
fi

# Check 2: both files exist in the final tree
TREE=$(git ls-tree --name-only HEAD)
FAIL=0
if ! echo "$TREE" | grep -q "^file-a.txt$"; then
    echo "FAIL: file-a.txt missing from final tree"
    FAIL=1
fi
if ! echo "$TREE" | grep -q "^file-b.txt$"; then
    echo "FAIL: file-b.txt missing from final tree"
    FAIL=1
fi

# Check 3: submodule entry is unchanged
SUB_ENTRY_AFTER=$(git ls-tree HEAD mysub)
if [[ "$SUB_ENTRY_BEFORE" != "$SUB_ENTRY_AFTER" ]]; then
    echo "FAIL: submodule entry changed"
    echo "  before: $SUB_ENTRY_BEFORE"
    echo "  after:  $SUB_ENTRY_AFTER"
    FAIL=1
fi

if [[ $FAIL -ne 0 ]]; then
    echo "Final tree:"
    git ls-tree HEAD
    exit 1
fi

echo "PASS: two concurrent parent commits succeeded with submodule intact"
exit 0
