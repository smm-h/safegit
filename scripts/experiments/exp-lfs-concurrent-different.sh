#!/usr/bin/env bash
# Two agents commit different LFS files concurrently. Verifies both land
# as proper LFS pointers in the final tree.
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

"$SAFEGIT" init --force -q
"$SAFEGIT" config commit.casMaxAttempts 50

# Create two different binary files
dd if=/dev/urandom of=file-a.bin bs=1024 count=100 2>/dev/null
dd if=/dev/urandom of=file-b.bin bs=1024 count=100 2>/dev/null

# Launch concurrently
"$SAFEGIT" commit -m "add a" -- file-a.bin 2>err-a.txt &
PID_A=$!
"$SAFEGIT" commit -m "add b" -- file-b.bin 2>err-b.txt &
PID_B=$!

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

FAIL=0

# Check 1: 3 commits total (init + a + b)
COUNT=$(git rev-list --count HEAD)
if [[ "$COUNT" -ne 3 ]]; then
    echo "FAIL: expected 3 commits, got $COUNT"
    git log --oneline
    FAIL=1
fi

# Check 2: both files in final tree
if ! git ls-tree --name-only HEAD | grep -q "^file-a.bin$"; then
    echo "FAIL: file-a.bin missing from final tree"
    FAIL=1
fi
if ! git ls-tree --name-only HEAD | grep -q "^file-b.bin$"; then
    echo "FAIL: file-b.bin missing from final tree"
    FAIL=1
fi

# Check 3: both are LFS pointers
BLOB_A=$(git show HEAD:file-a.bin)
if [[ "$BLOB_A" != version\ https://git-lfs.github.com/spec/v1* ]]; then
    echo "FAIL: file-a.bin is not an LFS pointer"
    echo "  got: $(echo "$BLOB_A" | head -1)"
    FAIL=1
fi

# file-b.bin might be on HEAD or HEAD~1 depending on commit order;
# check the final tree (HEAD) which should have both after CAS merges
BLOB_B=$(git show HEAD:file-b.bin)
if [[ "$BLOB_B" != version\ https://git-lfs.github.com/spec/v1* ]]; then
    echo "FAIL: file-b.bin is not an LFS pointer"
    echo "  got: $(echo "$BLOB_B" | head -1)"
    FAIL=1
fi

# Check 4: git lfs ls-files lists both
LFS_LIST=$(git lfs ls-files)
if ! echo "$LFS_LIST" | grep -q "file-a.bin"; then
    echo "FAIL: git lfs ls-files does not list file-a.bin"
    echo "$LFS_LIST"
    FAIL=1
fi
if ! echo "$LFS_LIST" | grep -q "file-b.bin"; then
    echo "FAIL: git lfs ls-files does not list file-b.bin"
    echo "$LFS_LIST"
    FAIL=1
fi

if [[ $FAIL -ne 0 ]]; then
    echo "Final tree:"
    git ls-tree HEAD
    exit 1
fi

echo "PASS: two concurrent LFS commits both landed with valid pointers"
exit 0
