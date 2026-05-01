#!/usr/bin/env bash
# Bug #1: SyncMainIndex("HEAD") after --branch commit clobbers the main index.
#
# After committing to a non-HEAD branch, safegit runs git read-tree HEAD
# which resets the main .git/index to HEAD's tree. This wipes any staged
# changes that existed in the main index.
#
# Expected: the main index is unchanged after a cross-branch commit
# Actual: the main index is reset to HEAD's tree

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

git branch feature

# Stage a change in the main index (simulating another tool or agent)
echo "staged-change" > staged.txt
git add staged.txt

# Verify it's staged
BEFORE=$(git diff --cached --name-only)
if [[ "$BEFORE" != "staged.txt" ]]; then
    echo "SETUP FAILED: staged.txt not in index"
    exit 2
fi

# Cross-branch commit
echo "feature-work" > feature.txt
safegit commit -q -m "cross-branch" --branch feature -- feature.txt

# Check if staged changes survived
AFTER=$(git diff --cached --name-only)
if [[ "$AFTER" == *"staged.txt"* ]]; then
    echo "PASS: staged changes preserved after cross-branch commit"
    exit 0
else
    echo "FAIL: staged changes lost after cross-branch commit"
    echo "  before: $BEFORE"
    echo "  after:  $AFTER"
    exit 1
fi
