#!/usr/bin/env bash
# Bug: safegit unwip overwrites another agent's edits to a wip-locked file.
#
# Wip-locks only block safegit commit/amend, not filesystem writes.
# When Agent A unwips, it blindly overwrites with the wip snapshot,
# destroying Agent B's edits that were written to disk after the wip.
#
# Expected: unwip refuses when the file has been modified since wip
# Actual: unwip silently overwrites B's work

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

# Agent A modifies and wips the file
echo "agent-A-wip" > shared.txt
safegit wip shared.txt 2>/dev/null
WIP_ID=$(safegit wip list 2>/dev/null | grep -oP '^[a-f0-9]+')

if [[ -z "$WIP_ID" ]]; then
    echo "SETUP FAILED: could not extract wip ID"
    exit 2
fi

# Agent B writes to the same file (filesystem allows it)
echo "agent-B-critical-work" > shared.txt

# Agent A unwips -- should this destroy B's work?
safegit unwip "$WIP_ID" 2>unwip_err.txt && UNWIP_RC=0 || UNWIP_RC=$?

CONTENT=$(cat shared.txt)

if [[ $UNWIP_RC -ne 0 ]]; then
    echo "PASS: unwip correctly refused (file modified since wip)"
    echo "  error: $(cat unwip_err.txt)"
    exit 0
elif [[ "$CONTENT" == "agent-B-critical-work" ]]; then
    echo "PASS: B's work preserved despite unwip"
    exit 0
else
    echo "FAIL: unwip destroyed agent B's work"
    echo "  expected: 'agent-B-critical-work'"
    echo "  got:      '$CONTENT'"
    exit 1
fi
