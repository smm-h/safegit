#!/usr/bin/env bash
# Verify safegit correctly commits parent repo files when a submodule exists,
# without corrupting the submodule entry.
#
# Setup: parent repo with a submodule. Then commit a new file in the parent.
# Expected: new file appears in HEAD, submodule entry SHA is unchanged.
# Actual: (bug would show submodule entry missing or SHA changed)

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

# Record the submodule entry before safegit touches anything
SUB_ENTRY_BEFORE=$(git ls-tree HEAD mysub)

"$SAFEGIT" init -q

# Create and commit a new file in the parent via safegit
echo "parent data" > newfile.txt
"$SAFEGIT" commit -m "add newfile" -- newfile.txt 2>err.txt && RC=0 || RC=$?

if [[ $RC -ne 0 ]]; then
    echo "FAIL: safegit commit exited with $RC"
    cat err.txt
    exit 1
fi

# Check 1: new file is in HEAD
if ! git show HEAD -- newfile.txt >/dev/null 2>&1; then
    echo "FAIL: newfile.txt not found in HEAD"
    exit 1
fi

# Check 2: submodule entry is unchanged
SUB_ENTRY_AFTER=$(git ls-tree HEAD mysub)
if [[ "$SUB_ENTRY_BEFORE" != "$SUB_ENTRY_AFTER" ]]; then
    echo "FAIL: submodule entry changed"
    echo "  before: $SUB_ENTRY_BEFORE"
    echo "  after:  $SUB_ENTRY_AFTER"
    exit 1
fi

echo "PASS: parent file committed without corrupting submodule entry"
exit 0
