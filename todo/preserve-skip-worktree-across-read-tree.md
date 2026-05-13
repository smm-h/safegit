# Preserve skip-worktree flags across read-tree

## Problem

`SyncMainIndex` (`internal/git/git.go:184`) runs `git read-tree HEAD` after every commit to the current branch (`internal/commit/commit.go:295`). This rebuilds the index from scratch, which **clears all skip-worktree flags**.

In the veliu-bag repo, CLAUDE.md files are deleted from worktrees and marked with `--skip-worktree` so git ignores the deletions. Every `safegit commit` destroys these flags, causing the files to reappear as uncommitted deletions in `git status`, `git diff`, and any tooling that reads working tree state.

The same `syncMainIndex` call also runs after checkout, pull, merge, rebase, reset, bisect, cherry-pick, and revert (`coord_cmd.go`).

## Root cause trace

1. `v branch create` runs the worktree-init extension, which sets skip-worktree on 8 CLAUDE.md files
2. The first `safegit commit` on the branch calls `SyncMainIndex("HEAD")` (commit.go:295)
3. `SyncMainIndex` runs `git read-tree HEAD` (git.go:185)
4. `git read-tree` replaces the index with the tree from HEAD — skip-worktree bits are not part of the tree object, they're index-only metadata, so they're lost
5. All subsequent `git status` / `git diff` / tooling sees the CLAUDE.md files as deleted

## Fix

In `SyncMainIndex`, preserve skip-worktree flags across the read-tree:

1. Before `read-tree`: run `git ls-files -v`, collect all files with the `S ` prefix (skip-worktree flag)
2. Run `git read-tree HEAD` as before
3. After `read-tree`: for each saved file, run `git update-index --skip-worktree <file>`

This is the same pattern used in codehome's rebase operations (`plugins/core/commands/git_cmd.py` `_detect_skip_worktree` / `_restore_skip_worktree`).

The fix should apply to `SyncMainIndex` itself (not each caller), since every call site has the same problem.

## Affected code

| File | Line | What it does |
|------|------|-------------|
| `internal/git/git.go:184-186` | `SyncMainIndex` | The function that needs the fix |
| `internal/commit/commit.go:295` | Step 8 of commit | Caller: after committing to current branch |
| `coord_cmd.go:88,172,217,263,314,365,405` | `syncMainIndex` | Caller: after checkout, pull, merge, rebase, reset, bisect, cherry-pick/revert |

## Effort

Small. ~15 lines added to `SyncMainIndex`. The git commands are simple and fast (ls-files + N update-index calls, where N is typically <10).

## Current workaround

A band-aid was added to `v branch select` (`plugins/core/commands/activation.py`) that re-applies skip-worktree flags on every branch switch. This should be removed once safegit is fixed.
