#!/usr/bin/env bash
# One agent bumps a submodule pointer while another commits a parent file,
# concurrently. Verifies the final tree has both the updated pointer and
# the new file.
#
# Setup: parent repo with a submodule. Make a new commit inside the submodule.
# Then concurrently: agent 1 commits the pointer bump, agent 2 commits a
# new parent file.
# Expected: final tree has updated submodule SHA AND the new file.
# Actual: (bug would lose one of the two changes)

set -euo pipefail

SAFEGIT=/home/m/Projects/safegit/safegit
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

OLD_SUB_SHA=$(git ls-tree HEAD mysub | awk '{print $3}')

"$SAFEGIT" init --force -q
"$SAFEGIT" config commit.casMaxAttempts 50

# Make a new commit inside the submodule
cd mysub
git config user.email "test@test.com"
git config user.name "Test"
echo "updated sub content" > updated.txt
git add updated.txt && git commit -q -m "sub update"
NEW_SUB_SHA=$(git rev-parse HEAD)
cd "$DIR/parent"

# Sanity check
if [[ "$OLD_SUB_SHA" == "$NEW_SUB_SHA" ]]; then
    echo "SETUP FAILED: submodule SHA did not change"
    exit 2
fi

# Create the parent file that agent 2 will commit
echo "new parent data" > newfile.txt

# Launch both agents concurrently
"$SAFEGIT" commit -m "bump sub" -- mysub 2>err-sub.txt &
PID_SUB=$!
"$SAFEGIT" commit -m "add file" -- newfile.txt 2>err-file.txt &
PID_FILE=$!

# Wait for both
wait $PID_SUB && RC_SUB=0 || RC_SUB=$?
wait $PID_FILE && RC_FILE=0 || RC_FILE=$?

if [[ $RC_SUB -ne 0 ]]; then
    echo "FAIL: submodule bump exited with $RC_SUB"
    cat err-sub.txt
    exit 1
fi
if [[ $RC_FILE -ne 0 ]]; then
    echo "FAIL: file commit exited with $RC_FILE"
    cat err-file.txt
    exit 1
fi

# Check 1: final tree has updated submodule SHA
FINAL_SUB_SHA=$(git ls-tree HEAD mysub | awk '{print $3}')
FAIL=0
if [[ "$FINAL_SUB_SHA" != "$NEW_SUB_SHA" ]]; then
    echo "FAIL: submodule pointer not updated"
    echo "  expected: $NEW_SUB_SHA"
    echo "  got:      $FINAL_SUB_SHA"
    FAIL=1
fi

# Check 2: newfile.txt exists in the final tree
if ! git ls-tree --name-only HEAD | grep -q "^newfile.txt$"; then
    echo "FAIL: newfile.txt missing from final tree"
    FAIL=1
fi

# Check 3: 4 commits total (initial + add-submodule + bump + file)
COUNT=$(git rev-list --count HEAD)
if [[ "$COUNT" -ne 4 ]]; then
    echo "FAIL: expected 4 commits, got $COUNT"
    git log --oneline
    FAIL=1
fi

if [[ $FAIL -ne 0 ]]; then
    echo "Final tree:"
    git ls-tree HEAD
    exit 1
fi

echo "PASS: concurrent submodule bump and parent file commit both landed"
exit 0
