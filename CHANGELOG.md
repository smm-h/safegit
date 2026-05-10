# Changelog

## 0.8.1

- **Standard `go install` path.** Moved main package from `cmd/safegit/` to project root. `go install github.com/smm-h/safegit@latest` now works without the `cmd/safegit` suffix.

## 0.8.0

- **`--flag=value` syntax.** All commands now accept `--flag=value` in addition to `--flag value`. Applies globally (e.g., `--config=/path`) and to commit (`-m="msg"`, `-F=path`, `--branch=name`), push, and rewrite-author flags.
- **Per-command `--help`.** Every command now responds to `--help`/`-h` with flag descriptions and examples. Validation errors in rewrite-author include a usage hint.
- **`--old-name` is now optional in `rewrite-author`.** Email-only rewrites work with just `--old-email`/`--new-email`. When both name and email flags are specified, matching uses AND logic (both must match).
- **`--verbose` output.** `--verbose` now produces detailed output for commit (files, ref, tree, SHA), push (remote URL, hook results, retries), doctor (per-check timing), and rewrite-author (per-commit and per-ref details).
- **Confirmation prompts for `rewrite-author`.** Prompts before rewriting history and before force-pushing. Skipped with `--force`. `--force` also skips the dirty-tree check.
- **Confirmation prompt for `doctor --uninstall`.** Prompts before removing safegit from a repository. Skipped with `--force`.
- **Output improvements.** "Parent-propagation only" replaced with "inherited (ancestors changed)". Singular/plural grammar fixed. Dry-run output includes email mapping when email flags are set.
- **Annotated tag email rewriting.** Tagger email is now rewritten alongside tagger name when `--old-email`/`--new-email` are specified.

## 0.7.3

- **Email rewriting.** `safegit rewrite-author` now accepts `--old-email` and `--new-email` flags to rewrite author/committer emails alongside names. Supports email-only rewrites when `--old-name` and `--new-name` are the same.
- **Fix: graceful error when origin remote is missing.** `--push` now checks for the `origin` remote before attempting to force-push, with a clear error message instead of a raw git error.

## 0.7.2

- **Fix: `rewrite-author` now excludes stash and notes refs.** `rev-list --all` includes `refs/stash` and `refs/notes`, which are not covered by `updateRefs`. Replaced with explicit ref globs (`refs/heads`, `refs/tags`, `refs/remotes`) so only branch, tag, and remote tracking refs are walked and rewritten.

## 0.7.1

- **Fix: `rewrite-author` now updates remote tracking refs.** Previously, `refs/remotes/` were walked during commit rewriting but not updated, causing the post-rewrite snapshot to see both old and new commits and failing verification with doubled commit counts.

## 0.7.0

- **`rewrite-author` command.** `safegit rewrite-author --old-name X --new-name Y` rewrites author/committer names across all repository history using native Go git plumbing (no external dependencies like git-filter-repo). Includes a 13-check verification framework comparing pre/post-rewrite state: commit count, tag count, branch names, tag names, old name absence, commit messages, author dates, committer dates, author emails, tree hashes, parent topology, working tree cleanliness, and tag-to-message mapping. Supports `--dry-run` for preview, `--push` for automatic force-push, annotated tag rewriting, merge commits, and multiple branches. Logged to oplog (not undoable).
- **Pre-commit hook support.** `safegit commit` now runs `.git/hooks/pre-commit` with `GIT_INDEX_FILE` pointing to the tmp index, so hooks see the correct staged files. Skipped with `--force` (matching git's `--no-verify`) and `--dry-run`. Previously, the plumbing-based pipeline bypassed git hooks entirely.
- **Directory deletion support.** `git rm --cached` now retries with `-r` when the target is a tracked directory. Previously, committing a deleted directory failed with "not removing recursively without -r".

## 0.6.2

## 0.6.1

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

## 0.4.0

### CLI consolidation (24 -> 20 commands)

- **`amend` and `reword` merged into `commit --amend`.** `safegit commit --amend -- files` replaces `safegit amend`. `safegit commit --amend -m "msg"` with no files replaces `safegit reword`. Mirrors git's native interface.
- **`unwip` merged into `wip restore`.** `safegit wip restore <id>` replaces `safegit unwip <id>`. All wip operations now live under one command.
- **`gc` merged into `doctor --fix`.** `safegit doctor --fix` replaces `safegit gc`. Doctor already diagnosed the problems; now it can fix them too.
- **`branch` removed.** Use `git branch` directly. Zero concurrency safety was added by the wrapper.

## 0.3.0

### Removed

- **`--format json` flag and all JSON output.** Every command now produces only human-readable output. The JSON envelope infrastructure (`emitJSON`, `jsonResponse`, `jsonError`, `--format` flag) is gone. 9 of 22 commands silently ignored `--format json` anyway (all guarded passthroughs), making the feature inconsistent. Agents already parse human output and git's native formats directly.

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

## 0.1.6

- Fix: `safegit unwip` now refuses to restore when files were modified since the wip was created, preventing silent data loss from overwriting another agent's edits
- Fix: platform-specific NFS detection refined into separate linux/darwin/windows files (replaces combined doctor_unix.go)
- Fix: goreleaser ldflags inject version string into release binaries
- Add Dockerfile for building safegit from source

## 0.1.5

- Add CAS retry jitter (1-10ms random sleep) to break thundering-herd stampedes under heavy concurrency
- Prevent potential slice mutation in oplog.Append

## 0.1.4

- Thread context.Context through all git helpers and callers (enables cancellation and clean shutdown)
- Signal handler cleans up lock files on SIGINT/SIGTERM (prevents 30s stale-lock timeout on macOS)
- `--branch` now errors if the target branch does not exist (prevents orphan branches from typos)
- Config validation rejects zero values (consistent with `safegit config` CLI behavior)
- Coord guard diffs against HEAD directly instead of relying on the main index (eliminates false dirty-tree refusals)

## 0.1.3

- Fix goreleaser config: add `main: ./cmd/safegit` so release binaries build correctly
- Drop Windows from build targets (Unix-only syscalls in lock, oplog, index, hooks packages)
- Fix push JSON output: hook failures now emit `ok: false` with error details

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
