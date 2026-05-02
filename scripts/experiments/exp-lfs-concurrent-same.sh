#!/usr/bin/env bash
# Two agents modify the same LFS file concurrently (different content each
# time). Verifies both commits land and the final blob is a valid LFS
# pointer (not corrupted by concurrent clean-filter execution).
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

# Create initial large.bin and commit it
dd if=/dev/urandom of=large.bin bs=1024 count=100 2>/dev/null
"$SAFEGIT" commit -m "initial large.bin" -- large.bin

"$SAFEGIT" config commit.casMaxAttempts 50

# Two agents each write different content and commit concurrently
(echo "version-a-$(head -c 1024 /dev/urandom | base64)" > large.bin && \
    "$SAFEGIT" commit -m "version a" -- large.bin) 2>err-a.txt &
PID_A=$!

(echo "version-b-$(head -c 1024 /dev/urandom | base64)" > large.bin && \
    "$SAFEGIT" commit -m "version b" -- large.bin) 2>err-b.txt &
PID_B=$!

wait $PID_A && RC_A=0 || RC_A=$?
wait $PID_B && RC_B=0 || RC_B=$?

FAIL=0

# Check for errors from either agent
if [[ $RC_A -ne 0 ]]; then
    echo "FAIL: agent A exited with $RC_A"
    cat err-a.txt
    FAIL=1
fi
if [[ $RC_B -ne 0 ]]; then
    echo "FAIL: agent B exited with $RC_B"
    cat err-b.txt
    FAIL=1
fi

# Check stderr for unexpected errors (warnings are OK)
if [[ -s err-a.txt ]]; then
    echo "NOTE: agent A stderr:"
    cat err-a.txt
fi
if [[ -s err-b.txt ]]; then
    echo "NOTE: agent B stderr:"
    cat err-b.txt
fi

# Check 1: 4 commits total (init-lfs + initial-large.bin + version-a + version-b)
COUNT=$(git rev-list --count HEAD)
if [[ "$COUNT" -ne 4 ]]; then
    echo "FAIL: expected 4 commits, got $COUNT"
    git log --oneline
    FAIL=1
fi

# Check 2: HEAD commit's large.bin is an LFS pointer (not corrupted)
BLOB_CONTENT=$(git show HEAD:large.bin)
if [[ "$BLOB_CONTENT" != version\ https://git-lfs.github.com/spec/v1* ]]; then
    echo "FAIL: HEAD:large.bin is not an LFS pointer"
    echo "  got: $(echo "$BLOB_CONTENT" | head -1)"
    FAIL=1
fi

# Check 3: git lfs ls-files still lists large.bin
if ! git lfs ls-files | grep -q "large.bin"; then
    echo "FAIL: git lfs ls-files does not list large.bin"
    git lfs ls-files
    FAIL=1
fi

if [[ $FAIL -ne 0 ]]; then
    echo "Final log:"
    git log --oneline
    echo "Final tree:"
    git ls-tree HEAD
    exit 1
fi

echo "PASS: concurrent same-file LFS commits both landed with valid pointer"
exit 0
