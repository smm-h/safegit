# Changelog

## 0.6.2

- **Migrated rlsbl layout.** Hooks moved from `scripts/` to `.rlsbl/hooks/`. Experiment scripts and stress tests moved from `scripts/` to `testdata/`. Two-step pre-push hook (rlsbl changelog check + safegit stress tests).
- **Bumped GitHub Actions to Node.js 24.** `checkout@v6`, `setup-go@v6`, `goreleaser@v7`. Added `*.local-only` gitignore pattern.

## 0.6.1

- **Post-release hook.** `scripts/post-release.sh` auto-installs the safegit binary locally on release via `go install`.
- **Removed stale experiment script.** `bug-02-reword-no-retry.sh` tested the removed `reword` command.

## 0.6.0

### Removed

- **`init` command.** Replaced by auto-initialization: any safegit command on an uninitialized repo creates `.git/safegit/` automatically. Use `safegit doctor --uninstall` to remove safegit from a repo.

## 0.5.0

### Removed

- **`wip` command and all WIP snapshot functionality.** `wip create`, `wip list`, `wip restore` are gone. The `wip-locks/` directory is no longer created. Use `safegit commit` on a temporary branch to save work-in-progress instead.
- **Submodule and Git LFS refusal check.** Repos with `.gitmodules` or `filter=lfs` are now fully supported. Both work correctly under concurrency (verified by experiment scripts).
- **Placeholder `pre-pre-push` hook installation.** No-op hook is no longer installed automatically. Use `safegit hook install` to add hooks.

### Added

- **Auto-initialization.** Running any safegit command on an uninitialized repo now creates `.git/safegit/` automatically. A one-time message is printed to stderr.

### Fixed

- **macOS ARM64 build.** `doctor_darwin.go` failed `go vet` on ARM64 due to `[]int8` to `string` conversion. Fixed by converting through `[]byte`.

### Changed

- **`IsInitialized` now checks for the `.git/safegit/` directory instead of `config.json`.**

## 0.4.0

### CLI consolidation (24 -> 20 commands)

- **`amend` and `reword` merged into `commit --amend`.** `safegit commit --amend -- files` replaces `safegit amend`. `safegit commit --amend -m "msg"` with no files replaces `safegit reword`. Mirrors git's native interface.
- **`unwip` merged into `wip restore`.** `safegit wip restore <id>` replaces `safegit unwip <id>`. All wip operations now live under one command.
- **`gc` merged into `doctor --fix`.** `safegit doctor --fix` replaces `safegit gc`. Doctor already diagnosed the problems; now it can fix them too.
- **`branch` removed.** Use `git branch` directly. Zero concurrency safety was added by the wrapper.

## 0.3.0

### Removed

- **`--format json` flag and all JSON output.** Every command now produces only human-readable output. The JSON envelope infrastructure (`emitJSON`, `jsonResponse`, `jsonError`, `--format` flag) is gone. 9 of 22 commands silently ignored `--format json` anyway (all guarded passthroughs), making the feature inconsistent. Agents already parse human output and git's native formats directly.

### Fixed

- Pre-push hook skips stress tests for tag-only pushes (no code changed, no point re-testing)
- CLAUDE.md and README updated to remove references to dropped commands (stash, tag, fetch, status, diff, log, show)

## 0.2.0

### Removed commands

Seven commands dropped to reduce surface area and eliminate code that adds no concurrency safety:

- **`safegit status`** -- JSON wrapper around `git status --porcelain`. Agents can parse porcelain format directly; it's designed to be machine-readable. The JSON re-encoding was ~100 lines of parsing code that had to be maintained as git's output evolves, with no concurrency benefit.
- **`safegit diff`** -- JSON wrapper around `git diff`. The hunk-splitting logic (`splitDiffChunks`) already had one panic bug. Agents already parse unified diff routinely, and git's native output is the source of truth.
- **`safegit log`** -- JSON wrapper around `git log`. Git's own `--format` flag already produces structured output. Marginal value over native git.
- **`safegit show`** -- JSON wrapper around `git show`. Same rationale as `log`.
- **`safegit stash`** -- Guarded passthrough, but the guard doesn't solve the real problem. In a multi-agent worktree, `git stash` captures all agents' uncommitted changes indiscriminately. The operation itself is fundamentally unsafe with concurrent agents. Use `safegit wip` instead (per-file, lock-protected).
- **`safegit tag`** -- Unguarded passthrough with zero safegit logic. Agents can use `git tag` directly with no risk since tags don't affect the working tree or index.
- **`safegit fetch`** -- Unguarded passthrough with zero safegit logic. Fetch only updates remote-tracking refs, which is safe to do concurrently. Use `git fetch` directly.

### Other

- Add blocking stress tests to pre-push hook (skip with `SKIP_STRESS=1`)
- Add WSL compatibility note to README

## 0.1.6

- Fix: `safegit unwip` now refuses to restore when files were modified since the wip was created, preventing silent data loss from overwriting another agent's edits
- Fix: platform-specific NFS detection refined into separate linux/darwin/windows files (replaces combined doctor_unix.go)
- Fix: goreleaser ldflags inject version string into release binaries
- Add Dockerfile for building safegit from source
- Add CONTRIBUTING.md with build, test, and release instructions
- Reorganize docs: design.md moved to todo/.done/, future features extracted to todo/future-features.md, req.md and research.md moved to docs/

## 0.1.5

- Add CAS retry jitter (1-10ms random sleep) to break thundering-herd stampedes under heavy concurrency
- Prevent potential slice mutation in oplog.Append
- pre-push hook now supports Go projects with VERSION file
- CI matrix expanded to macOS and Go 1.25
- Integration tests for cherry-pick, tag, stash passthrough and worktree lock sharing

## 0.1.4

- Thread context.Context through all git helpers and callers (enables cancellation and clean shutdown)
- Signal handler cleans up lock files on SIGINT/SIGTERM (prevents 30s stale-lock timeout on macOS)
- `--branch` now errors if the target branch does not exist (prevents orphan branches from typos)
- Config validation rejects zero values (consistent with `safegit config` CLI behavior)
- Coord guard diffs against HEAD directly instead of relying on the main index (eliminates false dirty-tree refusals)
- Mark unimplemented design.md features: inotify/kqueue wakeup, queue files, standalone stage/unstage

## 0.1.3

- Fix goreleaser config: add `main: ./cmd/safegit` so release binaries build correctly
- Drop Windows from build targets (Unix-only syscalls in lock, oplog, index, hooks packages)
- Fix push JSON output: hook failures now emit `ok: false` with error details
- Fix CI: skip stress tests with `-short` to prevent flaky failures

## 0.1.2

- Harden lock staleness: check hostname and /proc start-time to detect PID reuse across reboots
- Validate config values at load time (reject negatives and zero durations)
- Fix Windows build: extract NFS filesystem check into platform-specific files
- Fix `splitDiffChunks` panic on empty diff output
- Fix `safegit hook run <name>` now runs only the named hook, not all hooks
- Fix amend on root commit gives a clear error instead of cryptic git output
- Fix JSON mode for diff/log/show: strip user-supplied --color/--format/--pretty flags
- Fix uninstall now cleans shared worktree lock directory
- Fix GC reports all removal errors instead of just the last one
- Fix amend/reword SyncMainIndex guarded for cross-branch operations
- Add known limitations section to README (cross-machine, PID reuse, submodules)
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
