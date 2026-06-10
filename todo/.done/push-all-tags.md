# safegit push: add --all and --tags flags

## Problem

`safegit push` only accepts branch-like refspecs. It resolves each refspec via `resolveRefspec` which tries `refs/heads/<name>` first, then falls back to raw `rev-parse`. Git flags like `--all` and `--tags` cannot be passed as refspecs -- `resolveRefspec` would try to resolve `--all` as a branch name and fail.

After `rewrite-author --push` was removed (v0.17.0), the post-rewrite suggestion prints raw git commands:

```
git push origin --all --force-with-lease
git push origin --tags --force-with-lease
```

This bypasses safegit's push pipeline (pre-pre-push hooks, retry logic, oplog).

## Expected behavior

`safegit push --all --force-with-lease` and `safegit push --tags --force-with-lease` should work, forwarding the flags to `git push` while still running pre-pre-push hooks and retry logic.

## Implementation

Add `--all` and `--tags` as bool flags on the push command. When set, skip `resolveRefsForPush` (which only handles branch refspecs) and pass the flags directly to `buildGitPushArgs`. The pre-pre-push hook stdin would need to enumerate all refs being pushed (for `--all`: all local branches; for `--tags`: all local tags).

## Affected files

- `main.go` (flag definitions)
- `push.go` (flag handling, refspec resolution bypass, hook stdin generation)
- `internal/test/` (push tests with --all/--tags)

## Effort

Medium. The flag plumbing is simple, but the hook stdin generation for `--all`/`--tags` needs to enumerate refs and compute local-vs-remote SHAs, which is currently done per-refspec in `resolveRefsForPush`.
