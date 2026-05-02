#!/usr/bin/env bash
# Verify safegit can commit an updated submodule pointer.
#
# Setup: parent repo with a submodule. Make a new commit inside the submodule,
# then use safegit to commit the pointer bump in the parent.
# Expected: git ls-tree HEAD shows the new submodule SHA after the commit.
# Actual: (bug would leave the old SHA in the tree)

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

# Record the old submodule SHA
OLD_SHA=$(git ls-tree HEAD mysub | awk '{print $3}')

# Make a new commit inside the submodule
cd mysub
git config user.email "test@test.com"
git config user.name "Test"
echo "new sub content" > new-sub-file.txt
git add new-sub-file.txt && git commit -q -m "sub update"
NEW_SHA=$(git rev-parse HEAD)
cd "$DIR/parent"

# Sanity: the SHAs should differ
if [[ "$OLD_SHA" == "$NEW_SHA" ]]; then
    echo "SETUP FAILED: submodule SHA did not change"
    exit 2
fi

# Commit the submodule pointer bump via safegit
"$SAFEGIT" commit -m "bump submodule" -- mysub 2>err.txt && RC=0 || RC=$?

if [[ $RC -ne 0 ]]; then
    echo "FAIL: safegit commit exited with $RC"
    cat err.txt
    exit 1
fi

# Verify the pointer was updated
TREE_SHA=$(git ls-tree HEAD mysub | awk '{print $3}')
if [[ "$TREE_SHA" == "$NEW_SHA" ]]; then
    echo "PASS: submodule pointer bumped from ${OLD_SHA:0:8} to ${NEW_SHA:0:8}"
    exit 0
else
    echo "FAIL: submodule pointer not updated"
    echo "  expected: $NEW_SHA"
    echo "  got:      $TREE_SHA"
    exit 1
fi
