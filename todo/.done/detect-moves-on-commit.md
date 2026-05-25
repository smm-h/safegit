# Detect moves on commit

## Problem

When a user does `mv old/path new/path` and then `safegit commit -m "msg" -- new/path`, safegit only stages the new file. The deletion at `old/path` is not staged, leaving a dirty working tree. The user has to run a second commit for the deletion side, which should have been part of the same commit.

This is a footgun for any workflow involving `mv` followed by `safegit commit` — the most natural way to move files.

## Expected behavior

When committing a file that is new/untracked, safegit should check whether its content matches a deleted tracked file (i.e., git's rename detection). If so, automatically stage the deletion alongside the addition, so the commit records a proper rename.

## Implementation notes

- After staging the listed files, run `git diff --cached --diff-filter=A --name-only` to find newly added files in the commit.
- For each, check `git diff --diff-filter=D --name-only` for deleted tracked files.
- Use `git diff --no-index` or content hashing to match additions to deletions (or rely on `git's` built-in rename detection by staging both and letting git figure it out).
- Simpler approach: after staging the listed files, run `git status --porcelain` and look for `D` (deleted) entries whose content matches a staged `A` entry. Stage those deletions too.
- Must not stage unrelated deletions — only those that match a file being committed.

## Scope

Small-medium. The detection logic needs care to avoid false positives (unrelated deletions staged accidentally).
