# Scrub match: warn about working tree divergence

## Context

After `scrub match` rewrites blobs in history, tracked files in the working tree retain their original content. Every rewritten tracked file shows as "modified" in `git status`, even though the user intentionally scrubbed history.

This is correct — scrub shouldn't destroy working tree content. But it should inform the user and offer options.

## Current behavior

After a successful `scrub match`, `SyncMainIndex(ctx, "HEAD")` runs `git read-tree HEAD`, updating the index to match rewritten blobs. But working tree files are untouched (would need `-u` flag). The summary prints statistics and suggests `git push --force-with-lease`, but says nothing about working tree divergence.

No warning. The user discovers the divergence later via `git status` and has to reason about why tracked files are modified.

## Expected behavior

After a successful `scrub match`, for each tracked file whose committed blob changed:

1. Warn that N tracked files now diverge from their rewritten committed versions.
2. Suggest next steps:
   - If the files should stay tracked with scrubbed content: `git checkout -- <files>` to overwrite working tree with the rewritten versions.
   - If the files should become untracked (runtime-only): `git rm --cached <files>` and add to `.gitignore`.
   - If the divergence is intentional: no action needed, but the user should know.

## Implementation

Available data at the warning site (around line 887 of `scrub_match.go`):
- `blobMap` maps old-to-new blob SHAs (which blobs changed, not which paths)
- The index is already synced to the rewritten HEAD via `SyncMainIndex`

Simplest approach: run `git diff --name-only` after the `SyncMainIndex` call (line 777). Since the index reflects the rewritten content but the working tree doesn't, this gives exactly the list of divergent files. Print a warning with the three suggested actions.

## Affected files

- `scrub_match.go` (or equivalent): add post-rewrite working tree report after `SyncMainIndex`

## Effort

Small. A `git diff --name-only` call + formatted warning output.
