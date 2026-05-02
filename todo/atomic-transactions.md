# Atomic Git Transactions

## Context

safegit currently provides per-commit CAS safety (isolated index + compare-and-swap on ref update). Each `safegit commit` is atomic in isolation. But there is no mechanism for executing a *sequence* of git operations atomically -- for example, "fetch, then merge, then commit" as a single transaction that either fully succeeds or fully rolls back.

## The idea

A command that takes a sequence of git operations, snapshots all relevant ref SHAs at the start, executes the operations, and at commit-time verifies that no refs have moved since the snapshot. If they have, the entire sequence is retried or aborted.

```
safegit atomic "fetch origin; merge origin/main; commit -m 'sync' -- file.txt"
```

Alternatively, a begin/commit API:

```
safegit txn begin
safegit txn exec -- git fetch origin
safegit txn exec -- safegit commit -m "sync" -- file.txt
safegit txn commit   # verifies refs unchanged since begin, or aborts
```

## Hard problems

### Non-retryable operations

Merges can produce conflicts. If a transaction fails and needs to retry, the merge may have already modified the working tree with conflict markers. Unlike CAS on a single ref (which can just re-parent a commit), a multi-step transaction may leave the working tree in an inconsistent state that cannot be automatically recovered.

### Non-idempotent operations

Rebases rewrite commit history. Retrying a rebase that partially completed would create duplicate commits. Cherry-picks have similar issues -- the commit already exists in the history.

### Working tree as shared mutable state

Git operations like merge, rebase, and checkout modify the working tree as a side effect. In a multi-agent scenario, another agent may have written to files between two steps of the transaction. The transaction system would need to either:
- Lock the entire working tree for the duration (blocks all other agents)
- Use a per-transaction working tree copy (FUSE or worktree, expensive)
- Accept that working tree state is not transactional (defeats the purpose)

### Serialization vs parallelism

A FIFO queue that serializes all transactions would prevent conflicts but also prevent concurrent work, which is the opposite of what safegit exists to enable. The value of safegit is that agents CAN work in parallel safely.

### Scope of ref snapshots

Which refs to snapshot? All local refs? Only the ones touched by the operations? The transaction system would need to parse each git command to determine which refs it reads and writes, which is essentially reimplementing git's own ref-transaction logic at a higher level.

## Possible approaches

| Approach | Tradeoff |
|----------|----------|
| Per-ref CAS (current) | Already works for commits. Each commit is its own transaction. Simple, parallel-safe. |
| Working-tree lock | Block all agents during a multi-step operation. Safe but defeats parallelism. Only viable for short sequences. |
| Per-agent worktree | Each agent gets its own git worktree. Operations are isolated by default. Merge results at the end. This is the "virtual working tree" idea in future-features.md. |
| Optimistic multi-ref CAS | Snapshot N refs, execute operations, verify all N refs unchanged at the end. Retry on conflict. Only works for operations that don't modify the working tree (pure ref operations). |

## Recommendation

The per-agent worktree approach (from future-features.md) is the structural solution that makes multi-step atomicity possible without sacrificing parallelism. The transaction concept is a symptom of the shared-worktree constraint. If each agent has its own worktree, transactions become unnecessary because operations are inherently isolated.

Until per-agent worktrees exist, the current per-commit CAS approach is the best available tradeoff. It handles the most common case (concurrent commits) and delegates complex multi-step operations to the coordination guard system (which prevents them when the tree is dirty, rather than trying to make them atomic).

## Effort

Large. Any of the multi-step approaches requires either working-tree isolation (FUSE, symlinks, or git worktrees) or a command-parsing layer that understands which refs each git operation touches. Estimated weeks of work with significant design risk.
