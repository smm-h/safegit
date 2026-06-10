# Scrub: untrack rewritten files that are gitignored

## Problem

When `scrub match` rewrites a tracked file's blob, and that file is also in `.gitignore`, the current behavior (`read-tree --reset -u`) overwrites the working tree copy with the scrubbed content. This destroys the runtime value the user was keeping on disk — for a file they already tried to protect via `.gitignore`.

This happens when a file (e.g., a config file with secrets) was committed by mistake, then gitignored after the fact. The gitignore prevents future staging but doesn't untrack the file. Scrub rewrites the committed blob, then `read-tree -u` overwrites the working tree copy.

## Expected behavior

After rewriting blobs, for each rewritten file that is both tracked and gitignored:

1. Run `git rm --cached <file>` to untrack it (keeps the working tree copy intact).
2. Report in the summary: "Untracked N gitignored files that were rewritten: <list>".

The gitignore entry proves the user's intent — the file should not be tracked. Auto-untracking is the safe default. The working tree copy is preserved, and future `read-tree -u` calls can't touch it.

## Implementation

After the `SyncMainIndexWithWorktree` call, before printing the summary:

1. Collect the list of paths whose blobs were rewritten (derivable from `blobMap` + tree walk, or by recording paths during the rewrite).
2. For each path, check `git check-ignore <path>`.
3. If ignored and tracked, run `git rm --cached <path>`.
4. Include the count and file list in the summary output.

## Effort

Small. The data is already available at the summary site. The check is `git check-ignore` per rewritten path, and the fix is `git rm --cached`.
