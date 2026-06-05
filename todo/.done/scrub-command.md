# safegit scrub: surgical history rewrite for sensitive content

## Problem

Sometimes a commit contains content that shouldn't have been pushed — confidential project names, internal details in todo files, secrets accidentally committed. The content is already fixed on disk (the current version is sanitized), but the old content persists in git history, including in pushed commits and tagged releases.

Current options all have major drawbacks:
- `git filter-repo`: external dependency, rewrites ALL history, heavy
- `git rebase -i`: interactive, manual, error-prone for deep history
- Force-push after manual surgery: no tooling support, easy to miss steps

## Proposed command

`safegit scrub <file> [--from <commit>]`

Takes a file path and optionally a starting commit. Replaces the file's blob in ALL commits from `--from` to HEAD with the current on-disk content. Uses raw git plumbing (mktree, commit-tree, update-ref) — no external dependencies.

Steps:
1. Read the current on-disk content of the file
2. Create a new blob object: `git hash-object -w <file>`
3. For each commit from `--from` to HEAD that contains the file:
   - Read the commit's tree
   - Replace the file's blob hash with the new one
   - Create a new tree: `git mktree`
   - Create a new commit with the same author/message/timestamp but new tree and updated parent: `git commit-tree`
4. Update the branch ref to point to the new HEAD: `git update-ref`
5. Force-push: `git push --force-with-lease`

## Constraints

- No external dependencies (no git filter-repo)
- Must handle nested paths (file in subdirectory)
- Must preserve commit metadata (author, date, message, trailers)
- Must handle merge commits (multiple parents)
- Force-push uses `--force-with-lease` for safety
- Mandatory `--reason` flag for audit trail

## Consumers

- rlsbl release scrub (wrapper that also handles tag re-creation and GitHub Release updates)
- Any project that needs to scrub sensitive content from git history
