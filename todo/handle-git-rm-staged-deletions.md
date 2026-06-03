# Handle git rm staged deletions in safegit commit

## Problem

`safegit commit` currently cannot handle files that were staged for deletion via `git rm`. It tries to `hash-object` the file, which fails because the file no longer exists on disk. This forces users to fall back to raw `git commit` for deletion commits, defeating the purpose of safegit.

## Reproduction

```bash
git rm some-file.txt
safegit commit -m "remove some-file.txt" -- some-file.txt
# ERROR: hash-object fails because some-file.txt no longer exists
```

## Fix

Check if a file is staged for deletion (appears as `D` in the index column of `git status --porcelain`) and skip `hash-object` for those files. They are already staged by `git rm` -- just include them in the commit.

Specifically:
- When processing each file argument, check `git status --porcelain -- <file>`
- If the status starts with `D ` (deleted in index), the file is already staged for deletion -- skip `hash-object` and include it in the commit as-is
- If the status starts with `D?` with unstaged deletion, that is a different case (file deleted but not staged) -- existing behavior is fine there

## Affected code

The file-processing loop in `safegit commit` that runs `git hash-object` on each file before staging.

## Effort

Small -- a conditional check in the commit flow.
