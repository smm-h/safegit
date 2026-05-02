#!/usr/bin/env bash
# Verify safegit can commit an LFS-tracked file and the commit contains
# an LFS pointer (not raw content).
set -euo pipefail

SAFEGIT=/home/m/Projects/safegit/safegit
DIR=$(mktemp -d)
trap "rm -rf $DIR" EXIT
cd "$DIR"

# Set up a fresh repo with LFS
git init --initial-branch=main -q
git config user.email "test@test.com"
git config user.name "Test"
git lfs install --local

# Track *.bin via LFS
echo '*.bin filter=lfs diff=lfs merge=lfs -text' > .gitattributes
git add .gitattributes && git commit -q -m "init lfs"

"$SAFEGIT" init -q

# Create a 100KB binary file
dd if=/dev/urandom of=large.bin bs=1024 count=100 2>/dev/null

# Commit via safegit
"$SAFEGIT" commit -m "add large.bin" -- large.bin

FAIL=0

# Check 1: commit succeeded (we'd have exited already if not, due to set -e,
# but verify we have 2 commits)
COUNT=$(git rev-list --count HEAD)
if [[ "$COUNT" -ne 2 ]]; then
    echo "FAIL: expected 2 commits, got $COUNT"
    FAIL=1
fi

# Check 2: the blob in git is an LFS pointer, not raw data
BLOB_CONTENT=$(git show HEAD:large.bin)
if [[ "$BLOB_CONTENT" != version\ https://git-lfs.github.com/spec/v1* ]]; then
    echo "FAIL: blob is not an LFS pointer"
    echo "  got: $(echo "$BLOB_CONTENT" | head -1)"
    FAIL=1
fi

# Check 3: git lfs ls-files lists large.bin
if ! git lfs ls-files | grep -q "large.bin"; then
    echo "FAIL: git lfs ls-files does not list large.bin"
    git lfs ls-files
    FAIL=1
fi

if [[ $FAIL -ne 0 ]]; then
    exit 1
fi

echo "PASS: safegit committed an LFS-tracked file with a valid pointer"
exit 0
