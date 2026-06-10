# scrub: emit JSON SHA mapping after rewrite

## Problem

After `safegit scrub` rewrites history, consumers need to know which old commit SHAs map to which new SHAs. Currently there is no machine-readable output — the consumer must capture `git rev-list --all` before and after, then diff to reconstruct the mapping.

## Use case

The primary consumer is `rlsbl release scrub`, which needs to:
1. Update JSONL changelog entries (replace old commit hashes with new ones)
2. Recreate git tags on rewritten commits
3. Recreate GitHub Releases pointing to new tags

All of these require knowing old SHA -> new SHA for every rewritten commit.

## Proposed solution

Add `--json` flag to `scrub file` and `scrub match`. When set, after the rewrite completes, emit a JSON object to stdout:

```json
{
  "rewrites": {
    "abc123...": "def456...",
    "789012...": "345678..."
  },
  "tags": {
    "v1.0.0": {"old": "abc123...", "new": "def456..."}
  },
  "commits_rewritten": 10,
  "commits_unchanged": 50
}
```

The `rewrites` dict maps every old full SHA to its new full SHA. Only includes commits that were actually rewritten (SHA changed). The `tags` dict shows which tags were moved and from/to which commits.

## Effort

Small-medium. The rewrite walker already knows both SHAs during the walk. Accumulate them in a map and serialize at the end.
