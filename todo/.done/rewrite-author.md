# rewrite-author: History-wide author/committer name rewriting

## Problem

Git stores author and committer names in every commit object. If a user's git config has the wrong `user.name` (e.g., a work username instead of a personal one), every commit they've ever made carries the wrong name. The email may be correct (GitHub attributes by email), but `git log` and non-GitHub tools display the wrong name.

Fixing this requires rewriting every commit object in the repository — changing the author/committer name while preserving everything else: tree content, parent structure, timestamps, emails, messages, and tags.

This is a destructive, irreversible operation that changes every commit hash in the repository. It must be done carefully with comprehensive verification.

## Solution

A new `safegit rewrite-author` subcommand that:

1. Snapshots all verifiable state before rewriting
2. Walks all reachable commits in topological order, rewriting author/committer names where they match the old name
3. Updates all refs (branches, tags) to point to rewritten commits
4. Runs comprehensive verification comparing before/after state
5. Optionally force-pushes all refs to the remote

## Interface

```
safegit rewrite-author --old-name <name> --new-name <name> [--push] [--dry-run]
```

- `--old-name` (required): the author/committer name to replace
- `--new-name` (required): the replacement name
- `--push`: after successful rewrite and verification, force-push all branches and tags to origin
- `--dry-run`: show what would be rewritten without modifying anything (uses global flag)
- No `--email` filtering for now — matches by name only, rewrites both author and committer fields

## Implementation

### New code in `internal/git`

#### Commit object parser

A function to parse raw commit objects from `git cat-file -p <sha>`:

```
ParseCommit(ctx, sha) -> CommitInfo{Tree, Parents[], Author{Name, Email, Date}, Committer{Name, Email, Date}, Message}
```

The raw format is:

```
tree <sha>
parent <sha>          (zero or more)
author <name> <email> <timestamp> <timezone>
committer <name> <email> <timestamp> <timezone>
gpgsig -----BEGIN...  (optional, multi-line)

<message>
```

#### Author-aware commit creation

A function that creates a commit object with explicit author/committer fields via env vars:

```
CommitTreeWithAuthor(ctx, treeSHA, parentSHAs[], message, author, committer) -> sha
```

Uses `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_AUTHOR_DATE`, `GIT_COMMITTER_NAME`, `GIT_COMMITTER_EMAIL`, `GIT_COMMITTER_DATE` env vars passed to `git commit-tree` via `RunWithEnv`.

Must support multiple parents (merge commits).

#### Annotated tag reader/writer

- Read: `git cat-file -p <tag-sha>` to extract object, type, tag name, tagger, message
- Write: `git mktag` with stdin containing the rewritten tag object (updated tagger name, updated object SHA pointing to the rewritten commit)

### New file: `cmd/safegit/rewrite_author.go`

#### Algorithm

1. **Enumerate all refs**: branches (`refs/heads/*`), tags (`refs/tags/*`), HEAD
2. **Snapshot pre-rewrite state** (see Verification section)
3. **Collect all reachable commits**: `git rev-list --all --topo-order --reverse` to get commits in dependency order (parents before children)
4. **Build old-to-new SHA mapping**: for each commit in order:
   - Parse the commit object
   - Remap parent SHAs using the mapping (parents were already processed)
   - If author name or committer name matches `--old-name`, replace with `--new-name`
   - Create new commit object with `CommitTreeWithAuthor`
   - Record `oldSHA -> newSHA` in mapping
   - If nothing changed (name didn't match AND no parents were remapped), the commit still gets a new SHA if any ancestor was rewritten (because parent SHAs changed)
5. **Update all branch refs**: for each branch, update to the mapped SHA
6. **Update all tags**:
   - Lightweight tags: update ref to mapped commit SHA
   - Annotated tags: read tag object, rewrite tagger name if it matches, update object SHA to mapped commit, create new tag object, update ref
7. **Run verification** (see below)
8. **Optionally push**: `git push origin --all --force` and `git push origin --tags --force`

#### Ref updates

Use `git update-ref` directly (not the CAS variant) since we're the only operation modifying these refs during a rewrite. The CAS old-value is the pre-rewrite SHA which we've already recorded.

Actually — use CAS. We have the old values from the snapshot, and another safegit session could theoretically be committing concurrently. CAS costs nothing and prevents corruption.

### Verification

All checks compare pre-rewrite snapshots against post-rewrite state. Failure on any check aborts and reports the discrepancy.

Snapshot format: ordered lists captured before rewriting, compared against equivalent lists captured after.

| # | Check | How |
|---|-------|-----|
| 1 | Commit count | `git rev-list --all --count` before == after |
| 2 | Tag count | `git tag -l \| wc -l` before == after |
| 3 | Branch names | `git branch --format='%(refname:short)'` sets must match |
| 4 | Tag names | `git tag -l` sets must match |
| 5 | No remaining old name | `git log --all --format='%an%n%cn'` must not contain old name |
| 6 | Commit messages | Ordered list of `git log --all --topo-order --format='%s'` must match |
| 7 | Author dates | Ordered list of `git log --all --topo-order --format='%ai'` must match |
| 8 | Committer dates | Ordered list of `git log --all --topo-order --format='%ci'` must match |
| 9 | Author emails | Ordered list of `git log --all --topo-order --format='%ae'` must match |
| 10 | Tree hashes | Ordered list of `git log --all --topo-order --format='%T'` must match (strongest single check — proves all file content is byte-identical) |
| 11 | Parent topology | Ordered list of `git log --all --topo-order --format='%P'` parent counts must match (can't compare SHAs since they changed, but count and merge structure must be identical) |
| 12 | HEAD file content | Working tree has no unexpected modifications (no files changed on disk) |
| 13 | Tag-to-message mapping | For each tag name, the commit it points to (directly or through an annotated tag) must have the same commit message as before |

### Oplog

Log a single entry with op `rewrite-author` containing:
- `oldName`, `newName`
- `commitsRewritten` (count)
- `refs` (list of updated ref names)

This is informational only — `safegit undo` should NOT attempt to reverse a history rewrite. The undo handler should explicitly refuse this op type.

### Dry-run mode

When `--dry-run` is active:
- Parse all commits and report how many would be rewritten
- Show sample before/after for the first few commits
- Do not create any objects or update any refs

## Testing

### Unit tests

- `internal/git`: test `ParseCommit` with various commit formats (single parent, merge, root commit, GPG-signed, multi-line messages)
- `internal/git`: test `CommitTreeWithAuthor` produces commits with correct author/committer fields

### Integration tests in `internal/test/`

- Basic rewrite: create repo with N commits by old name, rewrite, verify all 13 checks pass
- Mixed authors: repo with commits by multiple authors, only the matching ones get rewritten
- Merge commits: verify parent topology is preserved after rewrite
- Annotated tags: verify tag objects are rewritten correctly
- Lightweight tags: verify they point to the correct rewritten commits
- Multiple branches: verify all branches are updated
- No-op: run on repo where no commits match — nothing changes, exit cleanly
- Idempotency: running twice with the same args should be a no-op the second time

## Affected files

| File | Change |
|------|--------|
| `internal/git/git.go` | Add `ParseCommit`, `CommitTreeWithAuthor`, tag read/write functions, new `CommitInfo`/`Author` types |
| `cmd/safegit/main.go` | Add `case "rewrite-author"` to switch, add to usage text |
| `cmd/safegit/rewrite_author.go` | New file: `runRewriteAuthor`, verification logic, snapshot types |
| `internal/test/rewrite_test.go` | New file: integration tests |
| `internal/git/git_test.go` | New unit tests for `ParseCommit`, `CommitTreeWithAuthor` |

## Effort estimate

Medium-large. The commit parser and tree-walking algorithm are straightforward but the verification framework and annotated tag handling add surface area. Integration tests are the bulk of the work.

- Commit parser + author-aware commit creation: small
- Rewrite algorithm (walk, remap, update refs): medium
- Annotated tag rewriting: small-medium
- Verification framework (13 checks): medium
- Integration tests: medium-large
- Total: ~600-900 lines of Go
