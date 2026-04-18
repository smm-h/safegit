# safegit Requirements

## Core: Multi-Agent Safety

- Multiple AI agent sessions must be able to work on the same repo concurrently without worktrees
- No shared mutable staging area -- agents must not be able to stage or commit each other's files
- Commits must be serialized or isolated so that concurrent commits never produce corrupt or mixed results
- If two agents edit the same file, the conflict must be surfaced as data, not silently lost or merged

## Concurrency Mechanism

- Lock-free or minimally-locking design preferred over a global mutex
- If a queue/lock is used, it must notify waiting agents automatically (no polling, no human intervention)
- Agent crash must not leave the repo in a permanently locked state (stale lock recovery)

## Git Compatibility

- Must read and write standard `.git` repositories
- Commits must be normal git commits (same SHA, same format) visible to any git tool
- Push/pull to GitHub, GitLab, and any standard git remote must work
- Teammates using plain `git` must see normal branches and commits
- CI/CD pipelines and code review tools must work without modification

## CLI

- Must be usable as a CLI tool (no GUI dependency)
- Must support non-interactive / headless operation for agent consumption
- Structured output (JSON) for agent parsing is preferred

## Reliability

- Every mutating operation should be undoable
- No operation should silently lose work
- Crash recovery must be automatic or trivial
