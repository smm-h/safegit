#!/usr/bin/env bash
# Bug #32: safegit can't make the first commit in an empty repo.
#
# RevParse(ref) fails when no commits exist, so the commit pipeline
# aborts before even trying to create the initial commit.
#
# Expected: safegit creates the initial commit (root commit, no parent)
# Actual: fails with "resolving parent" error

set -euo pipefail

DIR=$(mktemp -d)
trap "rm -rf $DIR" EXIT
cd "$DIR"

git init --initial-branch=main -q
git config user.email "test@test.com"
git config user.name "Test"
safegit init -q

echo "first file" > hello.txt
safegit commit -m "initial commit" -- hello.txt 2>err.txt && RC=0 || RC=$?

if [[ $RC -eq 0 ]]; then
    # Verify the commit actually landed
    COUNT=$(git rev-list --count HEAD 2>/dev/null || echo 0)
    if [[ "$COUNT" -ge 1 ]]; then
        echo "PASS: initial commit created successfully"
        exit 0
    else
        echo "FAIL: command succeeded but no commits on HEAD"
        exit 1
    fi
else
    echo "FAIL: safegit can't make the first commit (exit $RC)"
    cat err.txt
    exit 1
fi
