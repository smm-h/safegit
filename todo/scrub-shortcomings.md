# safegit scrub: shortcomings and feature requests

## Problem

`safegit scrub` was run on 10 `.env` files containing real API keys (OpenAI `sk-proj-*`, Anthropic `sk-ant-api03-*`). It reported success and suggested `git push --force-with-lease`, but the secrets persist in multiple places.

## Where secrets still persist after scrub

1. **The original commit blob** — `9eed2581` was the commit that introduced the `.env` files. After scrub, this commit hash still exists and still contains the original file contents. The scrub appears to have rewritten descendent commits but not the introducing commit itself, or it created new commit objects but the old ones remain reachable.

2. **HEAD still has the keys** — `git show HEAD:knowledgebases/cognee/.env` still returns the real API key. Either the scrub didn't rewrite the final commit in the chain, or the working tree files were re-committed after the scrub.

3. **Reflog entries** — even after `git reflog expire --expire=now --all`, the old commit objects may be referenced by other mechanisms.

4. **Dangling/unreachable objects** — `git gc --prune=now --aggressive` should remove unreachable objects, but if any ref still points to the old commit (tags, stashes, notes, replace refs, worktree refs), the objects survive.

5. **Git notes** — `refs/notes/*` can reference old commits.

6. **Replace refs** — `refs/replace/*` (used by some rewrite tools) can keep old objects alive.

7. **Stash entries** — `git stash list` entries reference commit objects that may contain the secret.

8. **Worktree refs** — if multiple worktrees exist, their HEAD/refs can keep old commits alive.

9. **Commit messages and trailers** — scrub only handles file content, not secrets accidentally pasted into commit messages.

10. **Submodule refs** — if the repo has submodules, their pinned commit hashes could reference repos that contain secrets.

11. **Pack files** — old objects can persist in `.git/objects/pack/*.pack` even after gc if they're referenced by any of the above.

12. **fsmonitor, index, MERGE_HEAD, CHERRY_PICK_HEAD** — various git internal refs that might hold references.

## What scrub should have done

A complete scrub needs to:
1. Rewrite ALL commits that contain the file (including the introducing commit)
2. Update ALL refs (branches, tags, stash, notes, replace)
3. Expire ALL reflogs
4. Run gc with aggressive pruning
5. Verify zero matches remain across the entire object store
6. Fail loudly if verification finds surviving copies

## Feature request: `safegit search-secrets` (or `safegit scan`)

A new command that searches for patterns across EVERY place a secret could hide in a git repo:

```
safegit scan --pattern 'sk-proj-|sk-ant-|AKIA|password=' [--fix]
```

Should search:
- All reachable commit tree blobs (every file in every commit)
- All unreachable/dangling blobs (`git fsck --unreachable`)
- Reflog entries (all refs)
- Stash entries
- Git notes (`refs/notes/*`)
- Replace refs (`refs/replace/*`)
- Commit messages (all commits, reachable and unreachable)
- Commit trailers
- Tag messages (annotated tags)
- Worktree refs
- The working tree and index
- `.git/config` (credentials can be stored here)
- `.git/hooks/*` (scripts might contain secrets)
- Pack files (enumerate all objects in all packs)
- Loose objects (`.git/objects/??/*`)

Output should be:
```
FOUND in commit abc123 (file: knowledgebases/cognee/.env, line 1)
FOUND in reflog refs/heads/main@{5} -> commit abc123
FOUND in dangling blob def456
FOUND in working tree: knowledgebases/cognee/.env:1
CLEAN: commit messages
CLEAN: tags
CLEAN: notes
...
Total: 15 occurrences in 4 locations
```

With `--fix`, it should:
1. Rewrite all commits using `git filter-repo` or equivalent
2. Delete all reflog entries referencing tainted commits
3. Delete stash entries referencing tainted commits
4. Delete replace refs and notes referencing tainted commits
5. Run `git gc --prune=now --aggressive`
6. Re-scan to verify
7. Print a summary of what was cleaned

This is essentially `git filter-repo` + `trufflehog` + `gitleaks` combined into one safegit command that both finds AND fixes.

## Workaround for now

The immediate fix for this repo is:
1. Rotate the API keys (OpenAI dashboard, Anthropic console, AWS Secrets Manager) — makes the committed keys invalid
2. Replace .env file contents with placeholders
3. Consider using `git filter-repo --invert-paths --path '*.env'` to remove all .env files from history entirely
4. Or start a fresh repo with `git init` and copy clean files
