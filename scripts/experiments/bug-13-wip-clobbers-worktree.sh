#!/usr/bin/env bash
# Bug #13: wip.Create reverts files via git checkout HEAD, clobbering edits.
#
# When agent B wips a file that agent A has also modified, agent A's
# uncommitted edits are silently destroyed by the git checkout HEAD.
#
# Expected: wip refuses if file has uncommitted changes by another agent,
#           or at minimum doesn't destroy the working-tree content
# Actual: working-tree content is silently reverted to HEAD

set -euo pipefail

DIR=$(mktemp -d)
trap "rm -rf $DIR" EXIT
cd "$DIR"

git init --initial-branch=main -q
git config user.email "test@test.com"
git config user.name "Test"

echo "original" > shared.txt
git add shared.txt && git commit -q -m "initial"
safegit init -q

# Agent A modifies shared.txt (uncommitted)
echo "agent-A-work" > shared.txt

# Agent B wips the same file
safegit wip shared.txt 2>/dev/null

# Check if agent A's work survived
if [[ -f shared.txt ]]; then
    CONTENT=$(cat shared.txt)
    if [[ "$CONTENT" == "agent-A-work" ]]; then
        echo "PASS: agent A's edits preserved"
        exit 0
    else
        echo "FAIL: agent A's edits destroyed (file contains: '$CONTENT')"
        exit 1
    fi
else
    echo "FAIL: shared.txt was deleted entirely"
    exit 1
fi
