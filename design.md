# safegit Design Document

safegit is a Go-based wrapper around `git` that makes a single git repository safe for concurrent multi-agent (AI session) use without worktrees. Every safegit commit is a normal git commit with the same SHA and the same on-disk format. Teammates and CI see only standard git.

This document is the architecture spec for v1. It assumes the locked-in decisions in the project README:

- Build a thin wrapper, not a fork of git, jj, or GitButler.
- Per-invocation tmp index files (rebuilt every safegit invocation from `HEAD`).
- Object writes are content-addressed and parallel-safe; ref updates use per-ref locks with CAS retry.
- Hunk-level granularity via `git apply --cached --index`.
- Same-machine concurrency only (POSIX `flock(2)` semantics).
- Stale lock recovery via PID liveness check.
- Pre-pre-push hooks that run BEFORE any network I/O.
- `--format json` available everywhere; default is human-readable.

The eight numbered sections that follow describe the data model, commit pipeline, hunk staging API, pre-pre-push hook contract, full CLI surface, failure modes, the coordination layer, and the concurrency test plan.

---

## Decision Record

This section records WHY safegit exists in its current shape, rather than re-deriving it.

- **Why not Jujutsu (jj)**: jj eliminates the `.git/index` race architecturally (no index), but does not provide per-agent commit isolation -- multiple agents still share the working-copy commit `@` and must use `jj split` to separate their work. Adoption requires every agent and human to learn `jj` commands. jj's conflict format is binary in the git tree, unreadable by standard git tooling. Adopting jj imposes a new mental model and breaks teammate-side tools for a partial solution to our problem.

- **Why not GitButler (`but`)**: GitButler solves multi-agent isolation via virtual branches with native MCP and Claude Code hook support -- practically the closest fit. Rejected because: Fair Source license (becomes MIT after 2 years) introduces commercial roadmap risk, the virtual-branch state in `.git/gitbutler/` adds a parallel mental model, and we'd be a downstream consumer of someone else's evolving product rather than owning the surface.

- **Why build a thin Go wrapper instead**: a wrapper around git plumbing (write-tree, commit-tree, update-ref, apply --cached) gives ~80% of the safety properties of jj/GitButler at ~5% of their complexity, while keeping every existing git tool, CI pipeline, code-review system, and teammate workflow intact. We control the roadmap. Standard git commits go out on the wire. Failure mode if safegit breaks: drop to raw git, lose isolation, but the repo remains valid.

- **Why Go**: static single binary, native concurrency primitives (mutex, channels, goroutines for ref-lock queue), clean subprocess and JSON handling, no runtime dependency. Bash hits scaling limits past ~400 LOC. Python adds runtime dep and slow startup. Rust is overkill for a wrapper of this size.

---

## 1. Data Model

All safegit-specific state lives under `.git/safegit/`. Nothing escapes that directory, so a `rm -rf .git/safegit` returns the repository to the equivalent of "vanilla git". safegit does NOT install enforcement git hooks (see Section 4 for the rationale and the layered enforcement story).

### 1.1 Directory layout

```
.git/
  safegit/
    config.json              global safegit config (schema version, defaults)
    log                      append-only operation log (JSONL)
    locks/
      refs/
        heads/<branch>.lock  active ref lock (PID + ts + op_kind)
      objects.lock           optional coarse lock for pack writes (rare; usually unused)
    queue/                           (not implemented in v1)
      refs/
        heads/<branch>.q     FIFO queue file for ref-lock waiters (one line per waiter: <pid> <ts>)
    tmp/
      <pid>-<random>/        per-invocation scratch (index file, synthetic patches)
                             deleted when the invocation exits
  hooks/
    pre-pre-push             single-file hook (executable, optional)
    pre-pre-push.d/          directory of hooks (run in lexical order, optional)
```

There is no `.safegit/` repo-tracked directory. All hooks live in `.git/hooks/` only (see 4.1).

### 1.2 Per-invocation tmp index

There are no sessions and no session IDs. Each safegit invocation is a self-contained operation:

1. Create `.git/safegit/tmp/<pid>-<random>/` where `<random>` is a short hex suffix (4 bytes) chosen to avoid PID-reuse races within the same second.
2. Seed the tmp index from `HEAD`:

    ```
    GIT_INDEX_FILE=.git/safegit/tmp/<pid>-<random>/index \
      git --no-optional-locks read-tree HEAD
    ```

3. Apply the requested staging operations against the tmp index (see Section 3).
4. Build the tree, the commit, and update the ref (see Section 2).
5. Delete `.git/safegit/tmp/<pid>-<random>/` on exit (success or failure) via `defer`.

There is no incremental staging across safegit invocations. Each `safegit commit` takes a complete file list and produces one commit. There is no `safegit session start`, no `SAFEGIT_SID`, no agent registration, no session GC.

If the process is killed mid-invocation, the tmp directory leaks. `safegit gc` removes any tmp directory whose PID is dead.

### 1.3 Operation log schema

`.git/safegit/log` is an append-only JSON-lines file. One line per mutating operation. It is the source of truth for `safegit doctor` (and `safegit undo`, post-v1).

```json
{"ts":"2026-04-26T11:39:42.123Z","pid":12345,"op":"commit","extra":{"ref":"refs/heads/main","tree":"<sha>","parent":"<sha>","sha":"<sha>","attempts":1}}
{"ts":"2026-04-26T11:39:43.001Z","pid":12346,"op":"commit","extra":{"ref":"refs/heads/main","tree":"<sha>","parent":"<sha>","sha":"<sha>","attempts":3}}
{"ts":"2026-04-26T11:39:45.900Z","pid":12348,"op":"push","extra":{"remote":"origin","refs":[{"localRef":"refs/heads/main","localSha":"<sha>","remoteRef":"refs/heads/main","remoteSha":"<sha>"}],"hooksRun":2}}
```

Required fields: `ts`, `pid`, `op`. Other fields are op-specific. Writes use `O_APPEND` on Linux/macOS, which guarantees atomic append for writes <= `PIPE_BUF` (4096 bytes). Lines longer than 4096 bytes are rejected; commit messages are not logged, only their SHA.

### 1.4 Lock file format

Lock files (`locks/refs/heads/<branch>.lock`) are short text files created atomically via `O_CREAT|O_EXCL` (exclusive create):

```
pid=12345
ts=2026-04-26T11:39:42.123Z
op=commit
host=hostname.local
```

`pid` and `host` are the keys for liveness. `op` is informational (for `safegit doctor` and operator inspection).

---

## 2. Commit Pipeline

The full sequence for `safegit commit -m "msg" -- file1 file2 ...`. The pipeline is split into a parallel-safe phase (object construction) and a serialized phase (ref update). A complete file list is required (no implicit "stage everything").

### 2.1 Phase A -- parallel-safe (no locks)

1. **Initialize tmp index.** Per Section 1.2, create `.git/safegit/tmp/<pid>-<random>/index` and seed from `HEAD`. Set `GIT_INDEX_FILE` to that path for all subsequent git invocations in this process.
2. **Validate file arguments.** For each `<file>` after `--`:
    - It must exist on disk OR be a known deletion (`--allow-empty` or explicit `--delete <file>`).
    - It must be inside the working tree (no `../` escape).
    - It must not be `.gitignore`'d unless `--force` is passed.
3. **Stage files into tmp index.** Apply the staging logic from Section 3 to each file.
4. **Build the tree.**

    ```
    GIT_INDEX_FILE=.git/safegit/tmp/<pid>-<random>/index \
      git --no-optional-locks write-tree
    ```

    `write-tree` is content-addressed and parallel-safe -- multiple invocations writing the same tree converge on the same SHA and writes to the object DB are idempotent.
5. **Snapshot the parent.** Resolve the current tip of the target branch:

    ```
    git rev-parse --verify refs/heads/<branch>
    ```

    Call this `<parent-sha>`. (For a detached `HEAD`, the target ref is `HEAD` and the parent is the resolved commit.)
6. **Build the commit.**

    ```
    git commit-tree <tree-sha> -p <parent-sha> -m "msg"
    ```

    Produces `<new-commit-sha>`. Also parallel-safe.

At this point we have a valid commit object in the object DB but no ref points to it. If the process dies right now, the commit is unreachable and will be GC'd by `git gc` eventually; nothing is broken.

### 2.2 Phase B -- serialized (per-ref lock + CAS)

7. **Acquire ref lock.** Attempt to atomically create `locks/refs/heads/<branch>.lock` via `O_CREAT|O_EXCL`. On success, we hold the lock.
8. **On lock contention,** poll with exponential backoff (10ms, 20ms, 50ms, 100ms, 200ms, 500ms, capped at 1s) until the lock is released or the timeout expires. On each poll, check if the lock holder is dead via `kill(pid, 0)`; if so, remove the stale lock and retry creation.
9. **Re-resolve parent.** Once we hold the lock, re-read `refs/heads/<branch>`. If it changed since step 5, we have a CAS miss.
    - **If parent unchanged:** proceed to step 10.
    - **If parent changed:** release the lock, GOTO step 5 (rebuild the commit with the new parent). After 5 attempts (configurable: `commit.casMaxAttempts`, default 5), exit with code 7 ("could not converge on branch tip; another writer is making faster progress; retry manually").
10. **Update the ref.**

    ```
    git update-ref refs/heads/<branch> <new-commit-sha> <parent-sha>
    ```

    `update-ref` itself takes the `<old-value>` argument so it performs an atomic CAS at the git level too. This is belt-and-suspenders: our lock prevents most contention, and `update-ref --old` catches any we missed.
11. **Release the ref lock.** Atomic `unlink(2)` of the lock file. ~~Pop our entry from the queue. Notify next waiter (writing to a sentinel pipe or just relying on inotify -- see 2.4).~~ Note: the queue mechanism and inotify/kqueue notification were not implemented in v1; waiters use exponential-backoff polling.
12. **Append to op log.** Write the `commit` JSONL line per Section 1.3.
13. **Cleanup.** Delete `.git/safegit/tmp/<pid>-<random>/`.

### 2.3 Sequence summary

| Step | Phase | Holds lock? | Touches global state? |
|---|---|---|---|
| 1. Init tmp index | A | no | no (only tmp dir) |
| 2. Validate args | A | no | reads only |
| 3. Stage to tmp index | A | no | no |
| 4. `git write-tree` | A | no | yes (writes objects, idempotent) |
| 5. Resolve parent ref | A | no | reads only |
| 6. `git commit-tree` | A | no | yes (writes one commit object) |
| 7. Acquire ref lock | B | acquiring | yes (creates lock file) |
| 8. Enqueue/wait if held | B | waiting | yes (writes queue file) -- Note: queue not implemented in v1; uses polling |
| 9. Re-resolve parent / CAS check | B | held | reads only |
| 10. `git update-ref --old` | B | held | yes (writes ref) |
| 11. Release ref lock | B | releasing | yes (unlink only; no queue/notify in v1) |
| 12. Append op log | B | not held | yes (append-only line) |
| 13. Cleanup | B | not held | tmp dir only |

### 2.4 Wakeup mechanism

> **Note:** v1 uses exponential-backoff polling instead of filesystem watchers. The inotify/kqueue design below was not implemented.

Waiters MUST be woken automatically (req: "no polling, no human intervention"). Mechanisms by platform:

- **Linux:** `inotify(7)` on the parent dir for `IN_DELETE` of `<branch>.lock`.
- **macOS:** `kqueue(2)` with `EVFILT_VNODE` on the parent dir.
- **Fallback:** exponential-backoff polling 10ms, 20ms, 50ms, 100ms, 200ms, 500ms, capped at 1s. The poll loop is bounded by `lock.acquireTimeout` (default 30s); past that we exit with code 8 ("ref lock acquisition timed out").

The Go implementation uses `github.com/fsnotify/fsnotify` (well-vetted, no CGo). (Not used in v1; polling fallback is the only implemented path.)

### 2.5 Retry policy

| Failure | Retry? | Max attempts | Backoff |
|---|---|---|---|
| CAS miss (parent changed) | yes | `commit.casMaxAttempts` (default 5) | none -- retry immediately, lock will queue us |
| Ref lock contention | yes (waiting) | unbounded under `lock.acquireTimeout` (30s) | v1: exponential-backoff polling (inotify/kqueue not implemented) |
| `git write-tree` fails | no | 0 | exit code 9 |
| `git commit-tree` fails | no | 0 | exit code 10 |
| `git update-ref` fails after lock held | yes | `commit.casMaxAttempts` (rare; usually means stale parent) | retry from step 5 |

---

## 3. Hunk Staging API

> **Note:** Standalone `safegit stage` and `safegit unstage` commands were not implemented. Hunk-level staging is available via `safegit commit -- file:hunk-spec` (see Section 5.3).

### 3.1 Hunk extraction

For `<file>`, the canonical hunk list comes from:

```
GIT_INDEX_FILE=.git/safegit/tmp/<pid>-<random>/index \
  git --no-optional-locks diff --no-color --no-ext-diff --no-renames -- <file>
```

against the working tree, where the "before" side is the content currently in the tmp index.

Parse the output:

- Header: lines starting with `diff --git`, `index `, `--- `, `+++ ` -- preserve verbatim, used to reconstruct synthetic patches.
- Hunks: each block beginning with `@@ -<old_start>,<old_count> +<new_start>,<new_count> @@`. Index hunks 1-based.

Hunk objects in memory:

```go
type Hunk struct {
    Index    int      // 1-based
    OldStart int; OldCount int
    NewStart int; NewCount int
    Header   string   // the @@ line
    Body     []string // the +/- /space lines
}
```

### 3.2 API surface

| Command | Selects |
|---|---|
| `safegit stage <file>` | Whole file (all hunks) |
| `safegit stage <file> --hunks 1,3,5` | Specific 1-based hunks |
| `safegit stage <file> --hunks 2-4` | Range (inclusive) |
| `safegit stage <file> --patch <patchfile>` | Apply arbitrary unified diff |
| `safegit stage <file> --interactive` | Proxy to `git add --patch <file>` against the tmp index |
| `safegit stage <file> --intent-to-add` | `git update-index --add --cacheinfo` for empty/new files |
| `safegit unstage <file>` | Whole file (`git reset HEAD -- <file>` against tmp index) |
| `safegit unstage <file> --hunks ...` | Inverse hunk apply via `git apply --cached --reverse` |

Hunk-level precision is the v1 floor. Line-range and sub-hunk staging are deferred (see Future Work).

### 3.3 Mechanism

For all hunk-selecting variants:

1. Extract hunks per 3.1.
2. Compute the selected-hunk subset.
3. Build a synthetic patch: header lines from the source diff + headers/bodies of selected hunks only.
4. `git apply --cached --index --whitespace=nowarn --recount` against `GIT_INDEX_FILE`.
    - `--cached` means apply to the index, not the working tree.
    - `--recount` makes `git apply` tolerant of slightly off line counts (caused by skipping intermediate hunks).
    - `--index` sanity-checks against the working tree.
5. On `git apply` failure, retry with `--3way` (uses blob sha info from the patch's `index` line). On second failure, exit code 12 with the apply stderr surfaced.

For `--interactive`:

- We exec `git add --patch <file>` with `GIT_INDEX_FILE` set, inheriting stdin/stdout/stderr. This is the only API that requires a TTY.

### 3.4 Edge cases

| Case | Behavior |
|---|---|
| Hunk depends on earlier unselected hunk (line-number mismatch) | `git apply --recount` handles most cases; on failure, re-run with `--3way`; on second failure, exit 12 with `Hint: try staging earlier hunks first or use --patch`. |
| Working tree changed since diff was taken | Re-extract hunks. If hunk count or content changed, abort with exit code 13 ("file changed under us; re-run") -- callers should retry. |
| File is binary | Whole-file only; `--hunks` rejected with exit 14. |
| File is new (intent-to-add) | Whole-file only; map to `git update-index --add --cacheinfo`. |
| File is deleted | `safegit stage <file> --delete` adds the deletion to the index. |
| Symlink | Treated as a special file; whole-file only. |

### 3.5 Inverse: unstage

`safegit unstage <file> --hunks ...` builds the same synthetic patch and applies it with `--reverse`. The "before" for the diff is the tmp index, the "after" is `HEAD`.

```
git apply --cached --index --reverse --recount <synthetic-patch>
```

Whole-file unstage is the simpler `git reset HEAD -- <file>` against the tmp index.

---

## 4. Pre-pre-push Hook Contract

Git's built-in `pre-push` hook fires AFTER the network connection to the remote is open. For long-running validators (smoke tests, integration suites), this means the SSH connection times out before validation finishes. safegit owns a `pre-pre-push` phase that runs BEFORE any network I/O.

### 4.0 No enforcement hooks

safegit does NOT install git hooks that block raw `git commit` / `git push` invocations. Enforcement is at the Claude Code `settings.json` layer (Bash permission rules) and via convention. There is no `SAFEGIT_AUTHORIZED` env var. Agents that bypass safegit by running `git` directly are responsible for the consequences; `safegit doctor` will surface bypasses by detecting `HEAD` movements that have no corresponding op log entry.

The only safegit-installed hook is `.git/hooks/pre-pre-push`, which is a wrapper that lets the user define long-running pre-push validators that run before the network connection opens. It is informational, not coercive.

### 4.1 Hook discovery

Hooks are discovered in this order, all are run, the first non-zero exit aborts the push:

1. `.git/hooks/pre-pre-push` (single file)
2. `.git/hooks/pre-pre-push.d/*` (lexical order, `*` skips files starting with `.` or ending in `~`)

A hook must be executable (`chmod +x`). Non-executable files in `.d/` are skipped with a warning (`safegit doctor` reports them).

Standard git hooks (post-commit, prepare-commit-msg, etc.) live in `.git/hooks/` as usual and run normally because safegit shells out to git.

### 4.2 Stdin contract

Identical to git's `pre-push` hook input. One line per ref being pushed:

```
<local-ref> SP <local-sha> SP <remote-ref> SP <remote-sha> LF
```

`<remote-sha>` is `0000000000000000000000000000000000000000` for new refs. Empty stdin on `--delete` pushes is also possible (matches git semantics).

### 4.3 Environment

safegit sets the following env for hooks:

| Var | Meaning |
|---|---|
| `SAFEGIT_REMOTE_NAME` | name of the remote being pushed to (e.g. `origin`) |
| `SAFEGIT_REMOTE_URL` | the URL we're about to dial |
| `SAFEGIT_PHASE` | `pre-pre-push` |
| `SAFEGIT_HOOK_TIMEOUT_S` | the configured timeout, for hook self-monitoring |

Inherits all other env (PATH, etc.).

### 4.4 Execution flow

```
safegit push [<remote>] [<refspec>...]
  -> resolve refs to push (local-sha, remote-sha)
  -> build hook stdin
  -> for each discovered hook in order:
       run hook with stdin, captured stdout/stderr (streamed to user)
       enforce timeout: hooks.preprepush.timeoutSeconds (default 1800 = 30 min)
       if exit != 0: print stderr, abort, exit code 20
  -> ONLY NOW: open SSH/HTTPS transport via `git push <remote> <refspec>...`
       (git's built-in pre-push still fires AFTER connection; see 4.5)
```

### 4.5 Compose with git's built-in pre-push

git's built-in `pre-push` is still useful for things that genuinely need the remote sha resolved post-handshake (e.g. checking that you're not pushing over a force-push). safegit does NOT pass `--no-verify` to the inner `git push`; the built-in `pre-push` fires after the network handshake as usual.

So the full sequence is:

1. safegit's pre-pre-push runs (no network)
2. `git push` opens the connection
3. git's built-in pre-push runs (network open)
4. Pack negotiation and transfer

If both phases are needed, the user splits expensive checks (smoke tests) into `pre-pre-push` and lightweight checks (force-push protection) into `pre-push`.

### 4.6 Timeout policy

- Default: 1800s (30 min) per hook.
- Configurable globally: `hooks.preprepush.timeoutSeconds`.
- Per-hook override: a hook may print `# safegit: timeout=NNN` on its first stdout line (parsed before forwarding to user).
- On timeout: `SIGTERM` then 5s grace then `SIGKILL`. Exit code 21 ("hook timed out").

### 4.7 Bypass

`safegit push --no-pre-pre-push` skips this phase. For agents we recommend disallowing this flag via Claude Code settings.

---

## 5. CLI Surface

Global flags applicable to every command:

| Flag | Default | Meaning |
|---|---|---|
| `--format <human\|json>` | `human` | Output format. |
| `--config <path>` | `.git/safegit/config.json` | Override config path. |
| `--quiet`, `-q` | -- | Suppress informational output. |
| `--verbose`, `-v` | -- | Debug output to stderr. |
| `--no-color` | auto | Disable ANSI color in human output. |
| `--dry-run` | -- | Print what would happen, don't mutate. |
| `--force` | -- | Bypass coordination layer checks (see Section 7). |

Exit code conventions:

| Range | Meaning |
|---|---|
| 0 | Success |
| 1 | Generic / unspecified error |
| 2 | Usage error (bad flags) |
| 3 | Not in a git repo |
| 4 | safegit not initialized |
| 5 | Coordination layer refusal (working tree dirty, see Section 7) |
| 7-19 | Pipeline-specific (commit/stage; see Section 2/3) |
| 20-29 | Hook errors (see Section 4) |
| 30-39 | Lock errors |
| 40-49 | Network errors (push/pull/fetch) |

JSON output schema is uniform for every command: `{"ok": bool, "command": str, "data": <op-specific>, "error": {"code": int, "message": str} | null, "warnings": [str]}`.

### 5.1 Lifecycle

- **`safegit init`** -- bootstrap `.git/safegit/`, install `.git/hooks/pre-pre-push` wrapper, write default `config.json`. Refuses if `.gitmodules` exists or if `.gitattributes` contains an LFS filter (see 6.7). `--uninstall` reverses everything.
    - JSON: `{"installed": true, "hookPath": ".git/hooks/pre-pre-push", "configPath": ".git/safegit/config.json"}`.

There are no `safegit session` subcommands; sessions do not exist (see 1.2).

### 5.2 Inspection (read-only, proxy to git)

These are thin wrappers that forward to git. They never block on locks.

- **`safegit status`** -- `git status` (working tree vs `HEAD`).
- **`safegit diff [<paths>...]`** -- working tree vs `HEAD`.
- **`safegit log [<args>...]`** -- direct passthrough.
- **`safegit show <rev>`** -- direct passthrough.

JSON: each wraps the git porcelain v2 output and translates to structured form (paths, status flags, hunks).

### 5.3 Staging

Staging is folded into `safegit commit` via the `file:hunk-spec` syntax (e.g., `safegit commit -m "msg" -- file.txt:1,3`). There is no standalone `safegit stage` or `safegit unstage` command. The design below describes the original plan; in practice, hunk selection is specified inline at commit time.

### 5.4 Committing

- **`safegit commit -m <msg> [-F <file>] [--allow-empty] [--branch <ref>] -- <files>...`** -- run the pipeline in Section 2. A complete file list after `--` is required.
    - JSON: `{"sha": "...", "ref": "...", "parent": "...", "tree": "...", "attempts": 1}`.
- **`safegit amend [-m <msg>] [--branch <ref>] -- <files>...`** -- amend the tip of the current branch (or `--branch`). Implemented as: build new tree (with optional file changes), `commit-tree` with the parent of `HEAD`, lock-and-update-ref. Refuses if the branch tip moved since the amend was prepared.
- **`safegit reword [<rev>] -m <msg> [--branch <ref>]`** -- rewrite a commit message. v1 only allows rewording the tip of the current branch (or `--branch`).
- **`safegit undo`** -- reverse the last commit, amend, or reword on the current branch by reading the oplog and resetting the ref to the previous value via CAS.

### 5.5 Branching and tree-mutating ops

These commands all pass through the coordination layer in Section 7. They refuse if the working tree is dirty (modified, deleted, or untracked files present) unless `--force` is passed.

- **`safegit branch [<name>] [--from <rev>]`** -- create a branch (ref-locked). Does not touch working tree; coordination layer not invoked.
- **`safegit branch --list [--format json]`**.
- **`safegit branch --delete <name> [--force]`**.
- **`safegit checkout <ref>`** -- switches the working tree. Subject to coordination layer.
- **`safegit merge <branch>`** -- three-way merge implemented as: acquire ref lock for HEAD's branch, invoke `git merge --no-commit <branch>` in a subprocess, capture conflict state, build commit object via `git commit-tree`, update ref via CAS. We do not implement merge ourselves; we shell out to git's merge-recursive. Subject to coordination layer.
- **`safegit rebase <upstream>`** -- subprocess to `git rebase`. Subject to coordination layer.
- **`safegit reset --hard <rev>`** -- subject to coordination layer.
- **`safegit bisect <subcommand>`** -- subject to coordination layer for tree-moving subcommands.
- **`safegit stash [<args>...]`** -- guarded passthrough: coordination check, then `git stash`.
- **`safegit cherry-pick <commit>...`** -- guarded passthrough: coordination check, then `git cherry-pick`.
- **`safegit revert <commit>...`** -- guarded passthrough: coordination check, then `git revert`.
- **`safegit tag [<args>...]`** -- unguarded passthrough to `git tag` (no coordination check).

### 5.6 Remote

- **`safegit push [<remote>] [<refspec>...] [--no-pre-pre-push] [--force]`** -- runs Section 4 hooks, then `git push`.
    - JSON: `{"remote": "origin", "refs": [{"local": "...", "remote": "...", "status": "..."}], "hooksRun": [...]}`.
- **`safegit pull [<remote>] [<branch>] [--ff-only]`** -- fetch + ff-only merge by default. Subject to coordination layer.
- **`safegit fetch [<remote>] [<refspec>...]`** -- direct passthrough (no tree mutation).

### 5.7 Hooks

- **`safegit hook install <name> --from <path>`** -- copy a hook into `.git/hooks/`, `chmod +x`.
- **`safegit hook list [--format json]`** -- list discovered hooks per Section 4.1.
- **`safegit hook run <name>`** -- invoke a hook manually for testing (synthesizes stdin).

### 5.8 Recovery

- **`safegit unlock <ref> [--force]`** -- force-release a stale ref lock. Without `--force`, refuses to release a lock whose holder is alive.
- **`safegit gc [--dry-run]`** -- remove `tmp/<pid>-<rand>/` directories whose PID is dead, ~~prune empty queue files~~ (queue not implemented in v1), compact `log` (rotates at `log.maxSize`, default 100MB).

### 5.9 Utility

- **`safegit version`** -- print version, build info, git version.
- **`safegit doctor`** -- sanity-check repo state. Reports: orphan tmp directories, stale ref locks (with holder PID), non-executable hook files, NFS-mounted repo, presence of `.gitmodules` or LFS filters, `HEAD` movements without op log entries (raw-git bypass detection).
    - JSON: `{"healthy": bool, "issues": [{"severity": "warn"|"error", "message": "...", "fixHint": "..."}]}`.

---

## 6. Failure Modes

For each, the design must specify both detection AND recovery. Recovery is automatic unless explicitly noted.

### 6.1 Process crashes mid-stage

- **Symptom:** orphan tmp directory at `.git/safegit/tmp/<pid>-<rand>/`.
- **Detection:** `safegit doctor` and any `safegit gc` invocation list tmp directories, parse the leading PID, and call `kill -0 pid`. Dead PID = orphan.
- **Recovery:** `safegit gc` removes the tmp directory. Object DB is unaffected (no orphan blobs from staging; staging only writes blobs for new tree contents and they're GC'd by `git gc` later if unreferenced).

### 6.2 Process crashes mid-commit (lock held)

- **Symptom:** `locks/refs/heads/<branch>.lock` exists with a dead `pid`.
- **Detection:** any commit acquisition attempt reads the lock, parses `pid` and `host`, calls `kill -0 pid` (Unix) or checks `/proc/<pid>` (Linux). If `host` differs from local `hostname`, treat as alive (cross-machine fence; see 6.6).
- **Recovery:** the stale lock file is removed via `os.Remove`, then a new lock is created with `O_CREAT|O_EXCL`. The new holder logs a `lock_recovered` op log entry. Corrupt or zero-length lock files (from crashes mid-create) are treated as stale.

### 6.3 Two invocations commit to same branch simultaneously

- **Symptom:** both reach step 7 of Section 2 around the same time.
- **Detection:** `O_CREAT|O_EXCL` lock acquisition fails for the second; second waits via exponential-backoff polling (design called for inotify; v1 uses polling).
- **Recovery:** lock orders them. After the first commits and releases, the second wakes, re-resolves parent (now the first invocation's commit), detects CAS miss, rebuilds commit with new parent, then succeeds. End result: linear history, no corruption, both commits land.

### 6.4 Network failure mid-push

- **Symptom:** `git push` exits non-zero with a transport error after the pre-pre-push hooks have already run.
- **Detection:** non-zero exit from inner `git push`.
- **Recovery:** safegit retries the push up to `push.retryAttempts` (default 3) with exponential backoff (1s, 2s, 4s) for transient errors (DNS, connection reset, TLS handshake). NOT retried: HTTP 401/403, "non-fast-forward", "remote rejected". Pre-pre-push hooks are NOT re-run on retry (they already validated the local state). Op log records each attempt.

### 6.5 Disk full during object write

- **Symptom:** `git write-tree` or `git commit-tree` fails with `ENOSPC`.
- **Detection:** non-zero exit + stderr scan for `ENOSPC` / "No space left".
- **Recovery:** abort the operation with exit code 9 or 10. The object DB may have partial loose objects -- these are harmless (git `gc` cleans them). No corruption. User must free disk and retry.

### 6.6 Cross-machine / NFS use is unsupported

v1 supports same-machine concurrency only. Cross-machine concurrency on a shared filesystem (NFS, SSHFS, FUSE-over-network) is OUT OF SCOPE. Rationale: PID-based liveness is meaningless across hosts; `flock(2)` semantics on NFS are unreliable and silently break the safety guarantees.

- **Detection:** `safegit doctor` reads the device of `.git/` via `statfs(2)` and warns if the filesystem type is in a denylist (`nfs`, `nfs4`, `fuse.sshfs`, `cifs`, `smbfs`).
- **Detection at runtime:** lock files include `host`. If a lock holder's `host` differs from local `hostname`, we conservatively treat the lock as alive and wait. After `lock.acquireTimeout`, exit code 8. The user must run `safegit unlock --force` on the holder's host or accept that cross-machine use is unsupported.
- **Recovery:** documented as an explicit non-goal for v1.

### 6.7 Unsupported repo features

`safegit init` refuses to initialize on repos that use features the v1 design does not handle:

| Condition | Error |
|---|---|
| `.gitmodules` present at repo root | "safegit v1 does not support submodules; remove .gitmodules or use raw git." |
| `.gitattributes` contains a line matching `filter=lfs` | "safegit v1 does not support Git LFS." |

If these features are added to the repo after `safegit init`, `safegit doctor` reports an error and refuses subsequent mutating operations until they are removed or the project upgrades to a version that supports them (see Future Work).

### 6.8 Pre-pre-push hook hangs

- **Symptom:** hook process doesn't exit within `hooks.preprepush.timeoutSeconds`.
- **Detection:** Go context timeout fires.
- **Recovery:** `SIGTERM`, 5s grace, `SIGKILL`. Push aborts with exit code 21. Op log records `hook_timeout`. No partial state -- we never opened the network connection.

### 6.9 Raw-git bypass

- **Symptom:** a user (or another tool) ran `git commit` directly. safegit does not install enforcement hooks (Section 4.0), so this is allowed by design.
- **Detection:** on any safegit invocation, the op log's most recent ref-update entry is compared against the actual ref tip. If the tip has advanced without an entry, `safegit doctor` and a warning on the next mutating command surface the bypass:

    ```
    WARN: HEAD moved from <sha> to <sha> with no safegit op log entry.
    Possible bypass via raw 'git commit'.
    ```

- **Recovery:** none needed mechanically -- git semantics still hold. We just surface the bypass for transparency.

---

## 7. Coordination Layer

The coordination layer prevents tree-mutating operations from clobbering uncommitted work belonging to another concurrent invocation. It is the alternative to per-agent virtual working trees.

### 7.1 Affected commands

| Command | Reason |
|---|---|
| `safegit checkout <ref>` | Switches branches; can clobber working-tree edits. |
| `safegit pull` | Fetch + merge; can clobber working-tree edits. |
| `safegit rebase` | Replays commits; rewrites working tree. |
| `safegit merge` | Three-way merge; may modify working tree. |
| `safegit reset --hard` | Resets working tree to a ref. |
| `safegit bisect <good|bad|reset>` | Moves the working tree across commits. |

Read-only commands (`safegit status`, `safegit diff`, `safegit log`, `safegit fetch`) and pure ref-update commands (`safegit branch --create`, `safegit commit`) are NOT affected.

### 7.2 Detection algorithm

Before invoking the underlying git operation:

```
dirty := []string{}
porcelain := exec("git status --porcelain --untracked-files=normal")
for line in porcelain:
    parse status code + path
    if status != "  " (clean):
        dirty.append(path)

if len(dirty) > 0:
    refuse(dirty)
```

Refusal output:

```
safegit: working tree is not clean; refusing <op> to avoid clobbering uncommitted work.

Modified files:
  M src/foo.go
  M src/bar.go
?? scratch.txt

Suggestion:
  safegit commit -m "<msg>" -- src/foo.go src/bar.go
  Or pass --force to override (you may lose work).
```

Exit code 5.

### 7.3 The "calling agent's stage" exception

Because each safegit invocation rebuilds its tmp index from `HEAD` and there is no persistent stage, the coordination check has no need to whitelist files belonging to "this agent's pending stage." The rule is simply: working tree must be clean.

This is what makes the design safe under concurrency: no agent can have hidden in-flight state that another agent might trample. Any in-flight state must be committed, which is visible to every other invocation.

### 7.4 Bypass

`--force` skips the dirty-tree check. Useful for recovery scenarios (e.g. you intentionally want `safegit reset --hard` to discard local edits). A warning is printed and an op log entry `coordination_bypassed` is recorded.

---

## 8. Concurrency Tests

Note: the test names below (T1-T17) are from the original design. The actual test names in `internal/test/` differ but cover the same scenarios.

The design must be falsifiable. This section enumerates the test scenarios that prove or disprove the architecture. All tests are Go tests under `internal/test/concurrent_test.go` using `t.Parallel()`, goroutines, and a temp git repo per test (set up via `t.TempDir()` + `git init`).

### 8.1 Test harness

Helper: `newRepo(t *testing.T) *RepoFixture`

- creates a temp dir
- runs `git init` and `safegit init`
- returns a fixture exposing:
    - `fixture.RunSafegit(args...) (stdout, stderr, exitcode)`
    - `fixture.HeadSHA(branch) string`
    - `fixture.LogEntries() []OpLogEntry`

Helper: `parallel(n int, fn func(i int))` -- spawns n goroutines, calls `fn(i)`, waits via `sync.WaitGroup`. Used by every test.

### 8.2 Test scenarios

| # | Name | Setup | Action | Expected |
|---|------|-------|--------|----------|
| T1 | StageDifferentFiles | Repo with 10 tracked files | 10 parallel `safegit commit -m m -- file_i` invocations | All 10 succeed; 10 commits land on `main`; CAS misses >= 9. |
| T2 | CommitDifferentBranches | 10 branches | 10 invocations each commit to their own branch | All succeed; 10 new commits; each branch tip advances exactly once; no lock contention. |
| T3 | CommitSameBranch | 1 branch | 10 invocations commit to `main` | All 10 commits land linearly; `git log --oneline main` shows 10 new commits; CAS miss count >= 9 in op log. |
| T4 | KillMidCommit | 1 invocation acquires the ref lock on a sleeping helper | `kill -9 <safegit pid>` after lock acquisition; verify lock holder PID is dead | Next `safegit commit` invocation detects dead PID, replaces lock, succeeds; op log records `lock_recovered`. |
| T5 | TmpDirGc | Spawn 10 invocations, kill 5 mid-commit, run `safegit gc` | -- | The 5 dead invocations' tmp dirs are deleted; the 5 live ones are untouched. |
| T6 | ConcurrentPush | 2 invocations both push `main` to the same remote | Both run `safegit push origin main` after each commits to `main` | First wins; second sees `non-fast-forward`; safegit re-fetches and either succeeds (after rebase prompt) or exits with code 41. v1: fail with 41. |
| T7 | ConcurrentDifferentBranchPush | 2 invocations push different branches concurrently | -- | Both succeed; no contention. |
| T8 | StaleLockReplace | Create a lock file with a dead PID; another invocation waits | -- | Replacement happens within 100ms; no lock leak. |
| T9 | InotifyWakeup | One invocation holds a lock for 500ms; another waits | -- | The waiter wakes within 50ms of release (verifies inotify path, not polling fallback). Note: not implemented in v1; v1 uses polling only. |
| T10 | OpLogIntegrity | Run T3 (10 concurrent commits to same branch) under `strace` to confirm `O_APPEND` writes | -- | Op log has exactly 10 commit lines, no partial/interleaved lines, all parseable as JSON. |
| T11 | HookTimeout | Configure `pre-pre-push` to `sleep 60`; set `hooks.preprepush.timeoutSeconds=2`; run push | -- | Push aborts within 8s with exit code 21. |
| T12 | RawGitBypassDetection | `git commit -am "bypass"`; then `safegit doctor` | -- | Doctor reports the bypass; exit code 0 with warnings array containing the bypass detection. |
| T13 | DiskFullSimulated | Mount a small `tmpfs` at `.git/objects`; run `safegit commit` of a file >tmpfs size | -- | Exit code 9; no corruption; subsequent `safegit commit` after freeing space succeeds. |
| T14 | CheckoutRefusedDirty | Working tree has a modified file; invocation B runs `safegit checkout other-branch` | -- | B exits with code 5; ref unchanged; working tree unchanged. |
| T15 | CheckoutCleanProceeds | Working tree clean; `safegit checkout other-branch` | -- | Succeeds; HEAD moves; op log records `checkout`. |

### 8.3 Running

```
go test ./internal/test/... -race -count=10 -timeout=10m
```

The `-race` flag catches data races in shared safegit state. `-count=10` reruns each test 10 times to flush out timing-dependent flakes.

For T1, T3, T9 specifically, also run under `stress-ng --io 4 --vm 2 --timeout 60s` in CI to expose disk/memory pressure regressions.

### 8.4 Continuous integration

GitHub Actions matrix: ubuntu-latest, macos-latest, on Go 1.22, 1.23, 1.24. Each runs:

1. `go vet ./...`
2. `go test ./... -race`
3. `go test ./internal/test/concurrent_test.go -race -count=20 -timeout=15m` (the brutal pass)
4. `go build -o /tmp/safegit ./cmd/safegit && /tmp/safegit doctor` against a temp repo as a smoke test

---

## Distribution

safegit is shipped as a single static Go binary built with `CGO_ENABLED=0`. The binary embeds the `pre-pre-push` hook wrapper template. `safegit init` writes the wrapper to `.git/hooks/pre-pre-push` (this is the only hook safegit installs; there are no enforcement hooks per Section 4.0).

Build matrix: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64. Released as GitHub Release artifacts; no package-manager presence in v1.

---

## Appendix: Future Work

These items are explicitly punted to v2 (or later) and are NOT part of v1 scope. They are listed here so the v1 design's sharp edges have a documented escape hatch.

- **Line-level stage precision.** v1 stages whole hunks. `safegit stage <file> --lines L1-L2` would translate a working-tree line range into the overlapping hunk(s); v2 may add this.
- **Sub-hunk staging.** True sub-hunk precision (stage some lines of a hunk, leave others) requires synthesizing a smaller patch via line-level diff. Deferred.
- **Cross-machine support.** NFS, distributed coordinator, cross-host PID liveness. v1 is same-machine only and `safegit doctor` warns on NFS.
- **Submodules.** Each submodule has its own index and HEAD; the coordination layer would need per-submodule extensions. v1 refuses to init on a repo with `.gitmodules`.
- **Git LFS.** LFS smudge/clean filters need to run during `git apply --cached`; works in principle but needs testing. v1 refuses to init on a repo with an LFS filter line.
- **Per-agent virtual working tree.** GitButler-style workspace isolation (each agent sees its own view of the working tree). Would obviate the coordination layer's "must be clean" rule. Requires either FUSE or a custom file-overlay; significant complexity.
- **Persistent virtual indexes.** Incremental staging across invocations (`safegit stage` then later `safegit commit`). v1 rebuilds the index every invocation for simplicity.
- **`SAFEGIT_AUTHORIZED` env var + git hook enforcement.** v1 relies on Claude Code `settings.json` permission rules and discipline. If discipline-only proves insufficient in practice, a v2 may add an installed `pre-commit` hook that aborts unless `SAFEGIT_AUTHORIZED=1` is set.
- **`safegit absorb`.** Auto-distribute hunks into the right commit in a stack (jj-style). Deferred.
- **`pre-pre-fetch` hook.** Symmetric to `pre-pre-push` for fetch operations. Probably yes, deferred.
