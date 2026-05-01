# Changelog

## 0.1.3

- Fix goreleaser config: add `main: ./cmd/safegit` so release binaries build correctly
- Drop Windows from build targets (Unix-only syscalls in lock, oplog, index, hooks packages)
- Fix push JSON output: hook failures now emit `ok: false` with error details
- Fix CI: skip stress tests with `-short` to prevent flaky failures

## 0.1.2

- Fix Windows build: extract NFS filesystem check into platform-specific files
- Fix `splitDiffChunks` panic on empty diff output
- Fix `safegit hook run <name>` now runs only the named hook, not all hooks
- Fix amend on root commit gives a clear error instead of cryptic git output
- Fix JSON mode for diff/log/show: strip user-supplied --color/--format/--pretty flags
- Fix uninstall now cleans shared worktree lock directory
- Fix GC reports all removal errors instead of just the last one
- Fix amend/reword SyncMainIndex guarded for cross-branch operations
- Fix transient `git update-ref` lock failures are retried instead of hard-failing
- Remove stale NEXT.md
- Update design.md: push backoff values, stage/unstage status, lock mechanism

## 0.1.1

### New features
- `safegit undo` reverses the last commit, amend, or reword using the oplog
- `safegit amend --branch` and `safegit reword --branch` for cross-branch operation
- Passthrough for `stash`, `cherry-pick`, `revert` (with coordination guards) and `tag`
- Push oplog now records individual ref SHAs for auditing
- Ref locks shared across git worktrees via `git rev-parse --git-common-dir`
- Initial commit support: `safegit commit` works in empty repos with no prior commits
- Version string auto-detected via `debug.ReadBuildInfo` for `go install` users

### Bug fixes
- Cross-branch commit no longer clobbers the main `.git/index`
- Cross-branch amend/reword no longer clobbers the main `.git/index`
- Amend now checks wip-locks (previously bypassed the protection entirely)
- Reword retries on CAS miss instead of hard-failing
- Amend uses the target ref instead of literal `HEAD` (fixes TOCTOU)
- Zero-length lock files (from crashes) are now treated as stale and recovered
- Transient `git update-ref` lock failures are retried instead of hard-failing
- Doctor no longer deletes orphan tmp dirs (reports only; use `gc` to clean)
- Broken symlinks detected correctly via `os.Lstat` instead of `os.Stat`
- Filenames containing colons no longer misidentified as hunk specs
- Push retry backoff reduced from 1s/4s/16s to 1s/2s/4s
- Detached HEAD produces a clear error message instead of an opaque git error

### Breaking changes
- **Wip no longer reverts files in the working tree.** Previously, `safegit wip` would snapshot files and revert them to HEAD. Now it only snapshots and creates wip-locks. This prevents clobbering another agent's uncommitted edits in a shared worktree. Users who need to revert files after wip should do so manually.
- **Wip commit message format changed** from `files: a.txt, b.txt` (comma-separated) to one `file: <path>` line per file. This fixes parsing failures for filenames containing commas. Old wip commits using the legacy format are still supported for restore.
- `safegit unwip` no longer accepts `--force` (the clean-check it bypassed was removed)

### Code quality
- Split `main.go` (1800 lines) into 10 focused files
- Consolidated 5 duplicated `*Die` functions into a single `die()` helper
- Extracted shared hunk-spec parsing into `parseFileSpecs()`
- JSON output no longer emits `"error": null` and `"warnings": null` on success
- All git passthrough commands now route through `git.RunPassthrough` for `--no-optional-locks`
- Shared test helpers extracted into `internal/testutil`

### Testing
- Added unit tests for CLI flag parsing, hunk specs, and helpers
- Added tests for amend CAS retry, cross-branch index preservation, and empty repo commits
- Made hooks output configurable for test capture via `hooks.SetOutput`
- Bug experiment scripts in `scripts/experiments/` for regression verification

## 0.1.0

- Two-phase commit pipeline with per-invocation tmp indexes and CAS retry
- Amend and reword commands with CAS safety
- Wip snapshots via refs with per-file locking
- Coordination guard for tree-mutating operations (checkout, merge, rebase, reset, pull, bisect)
- Pre-pre-push hooks with timeout, process-group signaling, and hook-requested timeout override
- Push with retry logic and transport-error detection
- Doctor health checks: initialization, stale locks, orphan dirs, bypass detection, filesystem, hooks, unsupported features
- Garbage collection for orphan tmp dirs, wip-locks, and log rotation
- Structured JSON output (--format json) for all commands
- Hunk-level staging via file:hunk-spec syntax
- Config management with dot-separated keys
- Lock recovery via PID liveness detection
- Append-only JSONL operation log with atomic writes
- Cross-branch commit via --branch flag
