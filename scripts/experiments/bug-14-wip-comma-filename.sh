#!/usr/bin/env bash
# Bug #14: Wip file list breaks on filenames containing ", ".
#
# The wip commit message stores files as "files: a.txt, b.txt" and parses
# them back by splitting on ", ". A filename containing a literal ", "
# gets split into bogus entries, breaking restore.
#
# Expected: wip + unwip round-trips correctly for any valid filename
# Actual: unwip fails or restores wrong paths

set -euo pipefail

DIR=$(mktemp -d)
trap "rm -rf $DIR" EXIT
cd "$DIR"

git init --initial-branch=main -q
git config user.email "test@test.com"
git config user.name "Test"

# Create a file with a comma in its name
echo "original" > "hello, world.txt"
git add "hello, world.txt" && git commit -q -m "add comma file"
safegit init -q

# Modify and wip it
echo "modified" > "hello, world.txt"
WIP_OUT=$(safegit wip "hello, world.txt" 2>&1)
WIP_ID=$(echo "$WIP_OUT" | grep -oP 'wip \K[a-f0-9]+')

if [[ -z "$WIP_ID" ]]; then
    echo "SETUP FAILED: could not extract wip ID from: $WIP_OUT"
    exit 2
fi

# Try to restore
safegit unwip "$WIP_ID" 2>unwip_err.txt && UNWIP_RC=0 || UNWIP_RC=$?

if [[ $UNWIP_RC -eq 0 ]]; then
    # Check the file was actually restored with the right content
    if [[ -f "hello, world.txt" ]]; then
        CONTENT=$(cat "hello, world.txt")
        if [[ "$CONTENT" == "modified" ]]; then
            echo "PASS: wip round-trip works for comma filename"
            exit 0
        else
            echo "FAIL: file restored but with wrong content: '$CONTENT'"
            exit 1
        fi
    else
        echo "FAIL: 'hello, world.txt' not restored"
        exit 1
    fi
else
    echo "FAIL: unwip failed (exit $UNWIP_RC)"
    cat unwip_err.txt
    exit 1
fi
