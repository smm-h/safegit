# safegit Design Document

safegit is a Go-based wrapper around `git` that makes a single git repository safe for concurrent multi-agent (AI session) use without worktrees. Every safegit commit is a normal git commit with the same SHA and the same on-disk format. Teammates and CI see only standard git.

This document is the architecture spec for v1. It assumes the locked-in decisions in the project README:

- Build a thin wrapper, not a fork of git, jj, or GitButler.
- Per-agent virtual indexes via `GIT_INDEX_FILE`.
- Object writes are content-addressed and parallel-safe; ref updates use per-ref locks with CAS retry.
- Hunk-level granularity via `git apply --cached --index`.
- Direct-`git` mutating ops are denied via three layers (Claude Code hook, `SAFEGIT_AUTHORIZED` env, installed git `pre-commit` hook).
- Stale lock recovery via PID liveness check.
- Pre-pre-push hooks that run BEFORE any network I/O.
- `--format json` available everywhere; default is human-readable.

The seven sections that follow describe the data model, commit pipeline, hunk staging API, pre-pre-push hook contract, full CLI surface, failure modes, and concurrency test plan.

---

## 1. Data Model

All safegit-specific state lives under `.git/safegit/`. Nothing escapes that directory, so a `rm -rf .git/safegit` returns the repository to the equivalent of "vanilla git" (the `pre-commit` hook installed under `.git/hooks/` is the only exception and is removed by `safegit init --uninstall`).

### 1.1 Directory layout

```
.git/
  safegit/
    config.json              global safegit config (schema version, defaults)
    log                       append-only operation log (JSONL)
    sessions.json             registry of active sessions (mtime-based GC hints)
    agents/
      <sid>/
        index                 per-agent virtual index (binary, git-format)
        env                   key=value env captured at session start
        pid                   holder PID (text)
        started               RFC3339 start timestamp (text)
        meta.json             optional: agent label, model, parent_sid
        cwd                   absolute path to working tree at session start
    queue/
      refs/
        heads/<branch>.q      FIFO queue file for ref-lock waiters (one line per waiter: <sid> <pid> <ts>)
    locks/
      refs/
        heads/<branch>.lock   active ref lock (PID + ts + op_kind)
      objects.lock            optional coarse lock for pack writes (rare; usually unused)
    hooks/
      pre-pre-push            single-file hook (executable)
      pre-pre-push.d/         directory of hooks, run in lexical order
    tmp/
      <sid>/                  scratch for synthetic patches, intermediate trees
.safegit/                      OPTIONAL repo-tracked hooks (checked into VCS)
  hooks/
    pre-pre-push
    pre-pre-push.d/
```

`.git/safegit/hooks/` is local; `.safegit/hooks/` is repo-tracked and shared with teammates. Both are run, repo-tracked first, then local. This mirrors `core.hooksPath` semantics without breaking the regular `.git/hooks/` directory.

### 1.2 Session lifecycle

A "session" is one agent's working context. Sessions are explicit:

- `safegit session start [--label LABEL] [--parent-sid SID]` creates `agents/<sid>/`, writes `pid`, `started`, `env` (the relevant whitelist of `CLAUDE_*`, `SAFEGIT_*`, and `GIT_*` vars), seeds `index` from the current `HEAD` tree, and prints the sid (and a shell snippet to export `SAFEGIT_SID=<sid>` if `--shell` is passed).
- `safegit session end <sid>` removes `agents/<sid>/` and any stale queue entries.
- `safegit session list [--format json]` enumerates live and dead sessions.
- `safegit gc` collects dead sessions (PID gone, `started` older than `gc.maxSessionAge`).

Implicit sessions: if `SAFEGIT_SID` is unset, safegit will auto-create an ephemeral session keyed off `(hostname, ppid, hash(cwd))`. Ephemeral sessions are GC'd aggressively (`gc.maxEphemeralAge`, default 1h after `pid` becomes dead).

### 1.3 Session ID scheme

Candidate schemes (open question, see end of doc for the unresolved tradeoff):

| Scheme | Pros | Cons |
|---|---|---|
| Random UUIDv7 | Stable across reconnects, sortable by time | Agent must track and pass `SAFEGIT_SID` |
| `pid-<PID>-<rand4>` | Easy to debug | PID reuse across long sessions |
| `sha256(CLAUDE_CONFIG_DIR + ppid + start_ts)[:12]` | Auto-derivable, no state to pass | Collisions if shell is re-exec'd |

v1 ships UUIDv7 as the canonical scheme; if a session-derivable hash is needed for auto-resume after a crash, we add it as a secondary index in `sessions.json`.

### 1.4 Per-session vs global state

| Path | Per-session | Global |
|---|---|---|
| `agents/<sid>/index` | yes | -- |
| `agents/<sid>/env`, `pid`, `started` | yes | -- |
| `tmp/<sid>/` | yes | -- |
| `log` (append-only) | -- | yes |
| `queue/refs/heads/<branch>.q` | -- | yes |
| `locks/refs/heads/<branch>.lock` | -- | yes |
| `config.json` | -- | yes |
| `sessions.json` | -- | yes (registry of all sids) |

### 1.5 Operation log schema

`.git/safegit/log` is an append-only JSON-lines file. One line per mutating operation. It is the source of truth for `safegit undo` (post-v1) and `safegit doctor`.

```json
{"ts":"2026-04-26T11:39:42.123Z","sid":"01927e8a-...-a","op":"stage","file":"lib/foo.go","hunks":[1,3],"index_sha_before":"abc","index_sha_after":"def"}
{"ts":"2026-04-26T11:39:43.001Z","sid":"01927e8a-...-a","op":"commit","ref":"refs/heads/main","tree":"<sha>","parent":"<sha>","new":"<sha>","attempts":1}
{"ts":"2026-04-26T11:39:44.500Z","sid":"01927e8a-...-b","op":"commit","ref":"refs/heads/main","tree":"<sha>","parent":"<sha>","new":"<sha>","attempts":3,"cas_misses":2}
{"ts":"2026-04-26T11:39:45.900Z","sid":"01927e8a-...-c","op":"push","ref":"refs/heads/main","local_sha":"<sha>","remote_sha":"<sha>","hooks_run":["pre-pre-push","pre-pre-push.d/lint"]}
```

Required fields: `ts`, `sid`, `op`. Other fields are op-specific. Writes use `O_APPEND` on Linux/macOS, which guarantees atomic append for writes <= `PIPE_BUF` (4096 bytes). Lines longer than 4096 bytes are rejected (the schema has no field large enough to risk this in v1; commit messages are not logged, only their SHA).

### 1.6 Lock file format

Lock files (`locks/refs/heads/<branch>.lock`, `locks/objects.lock`) are short text files written atomically via `tmpfile + rename(2)`:

```
pid=12345
sid=01927e8a-...-a
ts=2026-04-26T11:39:42.123Z
op=commit
host=hostname.local
```

`pid` and `host` are the keys for liveness. `op` is informational (for `safegit doctor` and operator inspection).

### Open questions

- UUIDv7 vs deterministic-hash sid scheme.
- Whether to namespace `.git/safegit/` differently when the repo is bare or worktreed (multiple worktrees share `.git/`).

---

## 2. Commit Pipeline

The full sequence for `safegit commit -m "msg" [-- file1 file2 ...]`. The pipeline is deliberately split into two phases: a parallel-safe phase (object construction) and a serialized phase (ref update).

### 2.1 Phase A -- parallel-safe (no locks)

1. **Resolve session.** Read `SAFEGIT_SID` from env, or auto-create per 1.2. Verify `agents/<sid>/` exists. Set `GIT_INDEX_FILE=$(.git/safegit/agents/<sid>/index)` for all subsequent git invocations.
2. **Validate file arguments.** For each `<file>` (after `--`):
    - It must exist on disk OR be a known deletion (`--allow-empty` or explicit `--delete <file>`).
    - It must be inside the working tree (no `../` escape).
    - It must not be `.gitignore`'d unless `--force` is passed.
3. **Stage files into virtual index.** For each file argument, run the staging logic from Section 3 against the virtual index. If no `--` arguments are passed, nothing is staged in this step (the virtual index's existing contents are used).
4. **Build the tree.** Run:

    ```
    GIT_INDEX_FILE=.git/safegit/agents/<sid>/index \
      git --no-optional-locks write-tree
    ```

    This produces `<tree-sha>`. `write-tree` is content-addressed and parallel-safe -- multiple agents writing the same tree converge on the same SHA and writes to the object DB are idempotent.
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

At this point we have a valid commit object in the object DB but no ref points to it. If the agent dies right now, the commit is unreachable and will be GC'd by `git gc` eventually; nothing is broken.

### 2.2 Phase B -- serialized (per-ref lock + CAS)

7. **Acquire ref lock.** Attempt to atomically create `locks/refs/heads/<branch>.lock` via `O_CREAT|O_EXCL`. On success, we hold the lock.
8. **On lock contention,** enqueue our `(sid, pid, ts)` line to `queue/refs/heads/<branch>.q` (append, one line). Then `inotify`/`kqueue` watch the lock file for deletion. On macOS without kqueue support for the case, fall back to short polling (see "polling fallback" in 2.4). When awoken, attempt acquisition again. Stale-holder check: if the holder's `pid` is dead (per Section 1.6 + `kill -0` / `/proc` check), atomically replace the lock (via `tmpfile + rename(2)` overwrite, with an extra `flock` over the parent dir to avoid races between concurrent stale-replacers).
9. **Re-resolve parent.** Once we hold the lock, re-read `refs/heads/<branch>`. If it changed since step 5, we have a CAS miss.
    - **If parent unchanged:** proceed to step 10.
    - **If parent changed:** release the lock, GOTO step 5 (rebuild the commit with the new parent). After 5 attempts (configurable: `commit.casMaxAttempts`, default 5), exit with code 7 ("could not converge on branch tip; another writer is making faster progress; retry manually").
10. **Update the ref.**

    ```
    git update-ref refs/heads/<branch> <new-commit-sha> <parent-sha>
    ```

    `update-ref` itself takes the `<old-value>` argument so it performs an atomic CAS at the git level too. This is belt-and-suspenders: our lock prevents most contention, and `update-ref --old` catches any we missed.
11. **Release the ref lock.** Atomic `unlink(2)` of the lock file. Pop our entry from the queue. Notify next waiter (writing to a sentinel pipe or just relying on inotify -- see 2.4).
12. **Append to op log.** Write the `commit` JSONL line per Section 1.5.
13. **Cleanup.** The virtual index is NOT cleared. It now reflects the just-committed tree; subsequent `safegit stage`/`safegit commit` operations on the same session can build on it. (See open question on persistence.)

### 2.3 Sequence summary

| Step | Phase | Holds lock? | Touches global state? |
|---|---|---|---|
| 1. Resolve session | A | no | no |
| 2. Validate args | A | no | no |
| 3. Stage to virtual index | A | no | no (only `agents/<sid>/index`) |
| 4. `git write-tree` | A | no | yes (writes objects, idempotent) |
| 5. Resolve parent ref | A | no | reads only |
| 6. `git commit-tree` | A | no | yes (writes one commit object) |
| 7. Acquire ref lock | B | acquiring | yes (creates lock file) |
| 8. Enqueue/wait if held | B | waiting | yes (writes queue file) |
| 9. Re-resolve parent / CAS check | B | held | reads only |
| 10. `git update-ref --old` | B | held | yes (writes ref) |
| 11. Release ref lock | B | releasing | yes (unlink + notify) |
| 12. Append op log | B | not held | yes (append-only line) |
| 13. Cleanup | B | not held | per-session only |

### 2.4 Wakeup mechanism

Waiters MUST be woken automatically (req: "no polling, no human intervention"). Mechanisms by platform:

- **Linux:** `inotify(7)` on the parent dir for `IN_DELETE` of `<branch>.lock`.
- **macOS:** `kqueue(2)` with `EVFILT_VNODE` on the parent dir.
- **Fallback (any platform, including misconfigured FUSE mounts):** exponential-backoff polling 10ms, 20ms, 50ms, 100ms, 200ms, 500ms, capped at 1s. The poll loop is bounded by `lock.acquireTimeout` (default 30s); past that we exit with code 8 ("ref lock acquisition timed out").

The `inotify`/`kqueue` watcher is a goroutine per waiter. The Go implementation uses `github.com/fsnotify/fsnotify` (well-vetted, no CGo).

### 2.5 Retry policy

| Failure | Retry? | Max attempts | Backoff |
|---|---|---|---|
| CAS miss (parent changed) | yes | `commit.casMaxAttempts` (default 5) | none -- retry immediately, lock will queue us |
| Ref lock contention | yes (waiting) | unbounded under `lock.acquireTimeout` (30s) | inotify-driven, no backoff; polling fallback uses exp backoff |
| `git write-tree` fails | no | 0 | exit code 9 |
| `git commit-tree` fails | no | 0 | exit code 10 |
| `git update-ref` fails after lock held | yes | `commit.casMaxAttempts` (rare; usually means stale parent) | retry from step 5 |

### Open questions

- Whether to persist the virtual index across invocations (current spec: yes; alternative: snapshot and rebuild from `HEAD` each time, paying a `read-tree` cost per command).
- Whether `safegit commit` without `--` should commit *all* pending changes in the virtual index (git semantics) or refuse and require explicit file list (safer but verbose).

---

## 3. Hunk Staging API

### 3.1 Hunk extraction

For `<file>`, the canonical hunk list comes from:

```
git --no-optional-locks diff --no-color --no-ext-diff --no-renames \
  -- <file>
```

against the working tree, where the "before" side is the content currently in the agent's virtual index (i.e. `git diff` with `GIT_INDEX_FILE=...`).

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
| `safegit stage <file> --lines 10-25` | All hunks whose `NewStart..NewStart+NewCount-1` overlaps the range |
| `safegit stage <file> --patch <patchfile>` | Apply arbitrary unified diff |
| `safegit stage <file> --interactive` | Proxy to `git add --patch <file>` against the virtual index |
| `safegit stage <file> --intent-to-add` | `git update-index --add --cacheinfo` for empty/new files |
| `safegit unstage <file>` | Whole file (`git reset HEAD -- <file>` against virtual index) |
| `safegit unstage <file> --hunks ...` | Inverse hunk apply via `git apply --cached --reverse` |

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

For `--lines L1-L2`:

- Translate to hunks first by overlap predicate: a hunk is selected iff `[NewStart, NewStart+NewCount) ∩ [L1, L2+1) != ∅`.
- If the selection partially covers a hunk, v1 stages the entire hunk and warns. (Sub-hunk granularity requires synthesizing a smaller patch; it is feasible but deferred -- see open questions.)

For `--interactive`:

- We exec `git add --patch <file>` with `GIT_INDEX_FILE` set, inheriting stdin/stdout/stderr. This is the only API that requires a TTY.

### 3.4 Edge cases

| Case | Behavior |
|---|---|
| Overlapping hunks via `--hunks` and `--lines` | Union selection; warn on duplicate selection. |
| Hunk depends on earlier unselected hunk (line-number mismatch) | `git apply --recount` handles most cases; on failure, re-run with `--3way`; on second failure, exit 12 with `Hint: try staging earlier hunks first or use --patch`. |
| Working tree changed since diff was taken | Re-extract hunks. If hunk count or content changed, abort with exit code 13 ("file changed under us; re-run") -- callers should retry. |
| File is binary | Whole-file only; `--hunks` rejected with exit 14. |
| File is new (intent-to-add) | Whole-file only; map to `git update-index --add --cacheinfo`. |
| File is deleted | `safegit stage <file> --delete` adds the deletion to the index. |
| Symlink | Treated as a special file; whole-file only. |

### 3.5 Inverse: unstage

`safegit unstage <file> --hunks ...` builds the same synthetic patch and applies it with `--reverse`. The "before" for the diff is the agent's virtual index, the "after" is `HEAD`.

```
git apply --cached --index --reverse --recount <synthetic-patch>
```

Whole-file unstage is the simpler `git reset HEAD -- <file>` against the virtual index.

### Open questions

- Sub-hunk (line-range within a hunk) precision -- currently rounds up to whole hunk.
- Whether `--lines` should be relative to the working tree (the "+" side) or to `HEAD` (the "-" side). Spec says "+" side; this is the agent-friendly choice but differs from `git blame -L`.

---

## 4. Pre-pre-push Hook Contract

Git's built-in `pre-push` hook fires AFTER the network connection to the remote is open. For long-running validators (smoke tests, integration suites), this means the SSH connection times out before validation finishes. safegit owns a `pre-pre-push` phase that runs BEFORE any network I/O.

### 4.1 Hook discovery

Hooks are discovered in this order, all are run, the first non-zero exit aborts the push:

1. `.safegit/hooks/pre-pre-push` (repo-tracked, single file)
2. `.safegit/hooks/pre-pre-push.d/*` (repo-tracked, lexical order, `*` skips files starting with `.` or ending in `~`)
3. `.git/safegit/hooks/pre-pre-push` (local, single file)
4. `.git/safegit/hooks/pre-pre-push.d/*` (local, lexical order)

A hook must be executable (`chmod +x`). Non-executable files in `.d/` directories are skipped with a warning (`safegit doctor` reports them).

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
| `SAFEGIT_SID` | the agent session sid |
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
  -> ONLY NOW: open SSH/HTTPS transport via `git push --no-verify <remote> <refspec>...`
       (--no-verify because we still want git's internal pre-push to fire AFTER connection;
        actually we want both: see 4.5)
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

`safegit push --no-pre-pre-push` skips this phase. For agents we recommend disallowing this flag via Claude Code settings hook (see Section 6 / project README).

### Open questions

- Whether `pre-pre-push.d/` files should be run in parallel (current spec: serial, lexical order).
- Whether to expose a corresponding `pre-pre-fetch` hook (probably yes; deferred).

---

## 5. CLI Surface

Global flags applicable to every command:

| Flag | Default | Meaning |
|---|---|---|
| `--format <human\|json>` | `human` | Output format. |
| `--sid <SID>` | env `SAFEGIT_SID` | Agent session sid. |
| `--config <path>` | `.git/safegit/config.json` | Override config path. |
| `--quiet`, `-q` | -- | Suppress informational output. |
| `--verbose`, `-v` | -- | Debug output to stderr. |
| `--no-color` | auto | Disable ANSI color in human output. |
| `--dry-run` | -- | Print what would happen, don't mutate. |

Exit code conventions:

| Range | Meaning |
|---|---|
| 0 | Success |
| 1 | Generic / unspecified error |
| 2 | Usage error (bad flags) |
| 3 | Not in a git repo |
| 4 | safegit not initialized |
| 5 | No active session (and `--sid` not given) |
| 6 | Permission denied (Claude Code hook denied operation) |
| 7-19 | Pipeline-specific (commit/stage; see Section 2/3) |
| 20-29 | Hook errors (see Section 4) |
| 30-39 | Lock errors |
| 40-49 | Network errors (push/pull/fetch) |

JSON output schema is uniform for every command: `{"ok": bool, "command": str, "data": <op-specific>, "error": {"code": int, "message": str} | null, "warnings": [str]}`.

### 5.1 Lifecycle

- **`safegit init`** -- bootstrap `.git/safegit/`, install `.git/hooks/pre-commit` enforcer, write default `config.json`. `--uninstall` reverses everything.
    - JSON: `{"installed": true, "hookPath": ".git/hooks/pre-commit", "configPath": ".git/safegit/config.json"}`.
- **`safegit session start [--label LABEL] [--parent-sid SID] [--shell]`** -- create a new session, print sid (or shell snippet).
    - JSON: `{"sid": "...", "started": "<rfc3339>", "indexPath": "..."}`.
- **`safegit session end <sid>`** -- terminate session, remove `agents/<sid>/`.
- **`safegit session list [--all]`** -- list active sessions; `--all` includes recently dead ones.
    - JSON: `{"sessions": [{"sid": "...", "pid": N, "alive": bool, "label": "...", "started": "..."}]}`.

### 5.2 Inspection (read-only, proxy to git)

These are thin wrappers that set `GIT_INDEX_FILE` to the agent's virtual index and forward to git. They never block on locks.

- **`safegit status`** -- `git status` against the virtual index.
- **`safegit diff [<paths>...]`** -- working tree vs virtual index.
- **`safegit diff --cached [<paths>...]`** -- virtual index vs `HEAD`.
- **`safegit log [<args>...]`** -- direct passthrough.
- **`safegit show <rev>`** -- direct passthrough.

JSON: each wraps the git porcelain v2 output and translates to structured form (paths, status flags, hunks).

### 5.3 Staging

- **`safegit stage <file> [--hunks ...] [--lines ...] [--patch <p>] [--interactive] [--delete] [--intent-to-add] [--force]`** -- see Section 3.
    - JSON: `{"file": "...", "hunksApplied": [1,3], "indexShaBefore": "...", "indexShaAfter": "..."}`.
- **`safegit unstage <file> [--hunks ...]`** -- inverse.

### 5.4 Committing

- **`safegit commit -m <msg> [-F <file>] [--allow-empty] [--branch <ref>] [-- <files>...]`** -- run the pipeline in Section 2.
    - JSON: `{"sha": "...", "ref": "...", "parent": "...", "tree": "...", "attempts": 1}`.
- **`safegit amend [-m <msg>] [-- <files>...]`** -- amend the tip of the current branch. Implemented as: build new tree (with optional file changes), `commit-tree` with the parent of `HEAD`, lock-and-update-ref. Refuses to amend if the agent's `HEAD` differs from the branch tip (concurrency hazard).
- **`safegit reword [<rev>] -m <msg>`** -- rewrite a commit message. v1 only allows rewording the tip of the current branch (no in-place history rewrite for non-tips).

### 5.5 Branching

- **`safegit branch [<name>] [--from <rev>]`** -- create a branch (ref-locked).
- **`safegit branch --list [--format json]`**.
- **`safegit branch --delete <name> [--force]`**.
- **`safegit checkout <ref>`** -- switches the working tree AND the agent's virtual index. Acquires the per-session lock implicitly (other operations within the same session block while a checkout is in flight).
- **`safegit merge <ref>`** -- v1: fast-forward only (`git merge --ff-only`). Three-way merge deferred (it requires a much heavier conflict-handling story; see open questions).

### 5.6 Remote

- **`safegit push [<remote>] [<refspec>...] [--no-pre-pre-push] [--force]`** -- runs Section 4 hooks, then `git push`.
    - JSON: `{"remote": "origin", "refs": [{"local": "...", "remote": "...", "status": "..."}], "hooksRun": [...]}`.
- **`safegit pull [<remote>] [<branch>] [--ff-only]`** -- fetch + ff-only merge by default.
- **`safegit fetch [<remote>] [<refspec>...]`** -- direct passthrough.

### 5.7 Hooks

- **`safegit hook install <name> --from <path>`** -- copy a hook into `.git/safegit/hooks/`, `chmod +x`.
- **`safegit hook list [--format json]`** -- list discovered hooks per Section 4.1.
- **`safegit hook run <name>`** -- invoke a hook manually for testing (synthesizes stdin).

### 5.8 Recovery

- **`safegit unlock <ref> [--force]`** -- force-release a stale ref lock. Without `--force`, refuses to release a lock whose holder is alive.
- **`safegit gc [--dry-run] [--max-session-age <duration>]`** -- clean dead sessions, prune empty queue files, compact `log` (rotates at `log.maxSize`, default 100MB).

### 5.9 Utility

- **`safegit version`** -- print version, build info, git version.
- **`safegit doctor`** -- sanity-check repo state. Reports: orphan virtual indexes, stale locks (with holder PID), non-executable hook files, sessions with dead PIDs, bypasses logged in `log` (raw `git commit` invocations detected via missing log entries).
    - JSON: `{"healthy": bool, "issues": [{"severity": "warn"|"error", "message": "...", "fixHint": "..."}]}`.

### Open questions

- Whether to add a `safegit absorb`-style command (auto-distribute hunks into the right commit in a stack); deferred.
- Whether `safegit checkout` should refuse to switch branches with a non-empty virtual index (probably yes, with `--force`).

---

## 6. Failure Modes

For each, the design must specify both detection AND recovery. Recovery is automatic unless explicitly noted.

### 6.1 Agent crashes mid-stage

- **Symptom:** orphan virtual index at `agents/<sid>/index`, possibly partially-applied hunks.
- **Detection:** `safegit doctor` and any `safegit gc` invocation check `agents/<sid>/pid` against `kill -0` / `/proc`. Dead PID + `started` older than `gc.maxSessionAge` (default 24h) = orphan.
- **Recovery:** `safegit gc` removes the session directory. Object DB is unaffected (no orphan blobs from staging; staging only writes blobs for new tree contents and they're GC'd by `git gc` later if unreferenced). The op log retains the partial `stage` record.

### 6.2 Agent crashes mid-commit (lock held)

- **Symptom:** `locks/refs/heads/<branch>.lock` exists with a dead `pid`.
- **Detection:** any commit acquisition attempt reads the lock, parses `pid` and `host`, calls `kill -0 pid` (Unix) or checks `/proc/<pid>` (Linux). If `host` differs from local `hostname`, treat as alive (cross-machine fence; see 6.9).
- **Recovery:** atomic replace via `tmpfile + rename(2)`, with a parent-dir `flock(2)` to serialize concurrent stale-replacers. The new holder logs a `lock_recovered` op log entry.

### 6.3 Two agents commit to same branch simultaneously

- **Symptom:** both reach step 7 of Section 2 around the same time.
- **Detection:** `O_CREAT|O_EXCL` lock acquisition fails for the second; second waits via inotify.
- **Recovery:** lock orders them. After the first commits and releases, the second wakes, re-resolves parent (now the first agent's commit), detects CAS miss, rebuilds commit with new parent, then succeeds. End result: linear history, no corruption, both commits land.

### 6.4 Network failure mid-push

- **Symptom:** `git push` exits non-zero with a transport error after the pre-pre-push hooks have already run.
- **Detection:** non-zero exit from inner `git push`.
- **Recovery:** safegit retries the push up to `push.retryAttempts` (default 3) with exponential backoff (1s, 4s, 16s) for transient errors (DNS, connection reset, TLS handshake). NOT retried: HTTP 401/403, "non-fast-forward", "remote rejected". Pre-pre-push hooks are NOT re-run on retry (they already validated the local state). Op log records each attempt.

### 6.5 Disk full during object write

- **Symptom:** `git write-tree` or `git commit-tree` fails with `ENOSPC`.
- **Detection:** non-zero exit + stderr scan for `ENOSPC` / "No space left".
- **Recovery:** abort the operation with exit code 9 or 10. The object DB may have partial loose objects -- these are harmless (git `gc` cleans them). Virtual index is untouched (we hadn't modified it). No corruption. User must free disk and retry.

### 6.6 Corrupted virtual index

- **Symptom:** `git read-tree` or any plumbing op against the virtual index fails with "bad index file" / "index file corrupt".
- **Detection:** `safegit doctor` runs `git ls-files --stage` against each `agents/<sid>/index` and catches errors.
- **Recovery:** rebuild from `HEAD` tree:

    ```
    rm .git/safegit/agents/<sid>/index
    GIT_INDEX_FILE=... git read-tree HEAD
    ```

    The agent's pending unstaged work in the working tree is preserved (the working tree is untouched). Pending STAGED work is lost -- but staging was always reproducible from working-tree diffs, so the agent can re-stage. We log a `index_rebuilt` event and warn the user.

### 6.7 Race between safegit and raw git

- **Symptom:** a user (or another tool) ran `git commit` directly without `SAFEGIT_AUTHORIZED=1`.
- **Detection:** the installed `.git/hooks/pre-commit` enforcer aborts the commit with a clear message:

    ```
    safegit: bare 'git commit' is not allowed in this repo.
    Use 'safegit commit ...' or set SAFEGIT_AUTHORIZED=1 to bypass.
    ```

    If the user explicitly bypasses with `SAFEGIT_AUTHORIZED=1 git commit`, safegit cannot prevent it; instead, on the next safegit invocation, `safegit doctor` notices that `HEAD` advanced without a corresponding entry in the op log and warns:

    ```
    WARN: HEAD moved from <sha> to <sha> with no safegit op log entry.
    Possible bypass via 'git commit'.
    ```

- **Recovery:** none needed mechanically -- git semantics still hold. We just surface the bypass for transparency.

### 6.8 Pre-pre-push hook hangs

- **Symptom:** hook process doesn't exit within `hooks.preprepush.timeoutSeconds`.
- **Detection:** Go context timeout fires.
- **Recovery:** `SIGTERM`, 5s grace, `SIGKILL`. Push aborts with exit code 21. Op log records `hook_timeout`. No partial state -- we never opened the network connection.

### 6.9 Multiple safegit instances: same machine vs across machines

v1 supports same-machine concurrency only. Cross-machine concurrency on a shared filesystem (NFS, SSHFS) is OUT OF SCOPE. Rationale: PID-based liveness is meaningless across hosts; `flock(2)` semantics on NFS are unreliable.

- **Detection:** lock files include `host`. If a lock holder's `host` differs from local `hostname` AND PID liveness can't be checked, we conservatively treat the lock as alive and wait. After `lock.acquireTimeout`, exit code 8. The user must run `safegit unlock --force` on the holder's host or accept that cross-machine use is unsupported.
- **Recovery:** documented as an explicit non-goal for v1. `safegit doctor` detects cross-host state and warns.

### Open questions

- Whether disk-full mid-`update-ref` (rare; `update-ref` writes a tiny ref file) can leave a half-written ref. git's own atomic-rename for ref updates should prevent this; we should test on `tmpfs` with quotas.
- Submodules and LFS interactions with the virtual index -- deferred to v2.

---

## 7. Concurrency Tests

The design must be falsifiable. This section enumerates the test scenarios that prove or disprove the architecture. All tests are Go tests under `internal/test/concurrent_test.go` using `t.Parallel()`, goroutines, and a temp git repo per test (set up via `t.TempDir()` + `git init`).

### 7.1 Test harness

Helper: `newRepo(t *testing.T) *RepoFixture`

- creates a temp dir
- runs `git init` and `safegit init`
- returns a fixture exposing:
    - `fixture.NewSession(t) string` -- returns sid
    - `fixture.RunSafegit(sid, args...) (stdout, stderr, exitcode)`
    - `fixture.HeadSHA(branch) string`
    - `fixture.LogEntries() []OpLogEntry`

Helper: `parallel(n int, fn func(i int))` -- spawns n goroutines, calls `fn(i)`, waits via `sync.WaitGroup`. Used by every test.

### 7.2 Test scenarios

| # | Name | Setup | Action | Expected |
|---|------|-------|--------|----------|
| T1 | StageDifferentFiles | Repo with 10 tracked files | 10 sessions each `safegit stage file_i` in parallel | All 10 succeed; each session's virtual index contains only its own file staged; no `agents/<sid>/index` is corrupted; op log has 10 stage entries. |
| T2 | CommitDifferentBranches | 10 branches | 10 sessions each commit to their own branch | All succeed; 10 new commits; each branch tip advances exactly once; no lock contention (all 10 acquire and release without queueing). |
| T3 | CommitSameBranch | 1 branch, 10 sessions | 10 sessions commit to `main` | All 10 commits land linearly; `git log --oneline main` shows 10 new commits; CAS miss count >= 9 in op log; final tree on `main` reflects the 10th commit. |
| T4 | KillMidCommit | 1 session starts a commit, blocked on a synthetic lock held by a sleeping process | `kill -9 <safegit pid>` after lock acquisition; verify lock holder PID is dead | Next `safegit commit` invocation detects dead PID, replaces lock, succeeds; op log records `lock_recovered`. |
| T5 | ConcurrentStageAndCommitSameSession | 1 session | goroutine A: `safegit stage` in a loop; goroutine B: `safegit commit` in a loop | Documented as illegal: safegit MUST refuse the second op (file lock on `agents/<sid>/.busy`) with exit code 6 ("session busy"). Test asserts at least one refusal occurred. |
| T6 | ConcurrentPush | 2 sessions both push `main` to the same remote | Both run `safegit push origin main` after each commits to `main` | First wins; second sees ref-moved (CAS at the remote: `non-fast-forward`); safegit re-fetches and either succeeds (after rebase prompt) or exits with code 41. v1: fail with 41. |
| T7 | ConcurrentDifferentBranchPush | 2 sessions push to different branches concurrently | -- | Both succeed; no contention. |
| T8 | StaleLockReplace | One session holds a lock; kill it; another waits | -- | Replacement happens within 100ms; no lock leak. |
| T9 | InotifyWakeup | One session holds a lock for 500ms; another waits | -- | The waiter wakes within 50ms of release (verifies inotify path, not polling fallback). |
| T10 | CrashRecovery | 100 sessions started, half SIGKILL'd, `safegit gc` run | -- | `safegit gc` removes exactly the dead sessions; live sessions untouched; op log intact. |
| T11 | OpLogIntegrity | Run T3 (10 concurrent commits to same branch) under `strace` to confirm `O_APPEND` writes | -- | Op log has exactly 10 commit lines, no partial/interleaved lines, all parseable as JSON. |
| T12 | HookTimeout | Configure `pre-pre-push` to `sleep 60`; set `hooks.preprepush.timeoutSeconds=2`; run push | -- | Push aborts within 8s with exit code 21. |
| T13 | RawGitBypassDetection | `SAFEGIT_AUTHORIZED=1 git commit -am "bypass"`; then `safegit doctor` | -- | Doctor reports the bypass; exit code 0 with warnings array containing the bypass detection. |
| T14 | DiskFullSimulated | Mount a small `tmpfs` at `.git/objects`; run `safegit commit` of a file >tmpfs size | -- | Exit code 9; no corruption; subsequent `safegit commit` after freeing space succeeds. |

### 7.3 Running

```
go test ./internal/test/... -race -count=10 -timeout=10m
```

The `-race` flag catches data races in shared safegit state (the in-process state is small but non-zero -- config cache, etc.). `-count=10` reruns each test 10 times to flush out timing-dependent flakes.

For T1, T3, T9 specifically, also run under `stress-ng --io 4 --vm 2 --timeout 60s` in CI to expose disk/memory pressure regressions.

### 7.4 Continuous integration

GitHub Actions matrix: ubuntu-latest, macos-latest, on Go 1.22, 1.23, 1.24. Each runs:

1. `go vet ./...`
2. `go test ./... -race`
3. `go test ./internal/test/concurrent_test.go -race -count=20 -timeout=15m` (the brutal pass)
4. `go build -o /tmp/safegit ./cmd/safegit && /tmp/safegit doctor` against a temp repo as a smoke test

### Open questions

- Whether to also fuzz the JSON op log parser (`go test -fuzz`).
- Whether to add property-based tests (e.g. with `gopter`) for the hunk-selection logic in Section 3.

---

## Appendix: Open questions, consolidated

These are surfaced for discussion before implementation. Each affects the data model or CLI surface in non-trivial ways.

- **Session ID scheme.** UUIDv7 (current spec) vs PID-based vs `sha256(CLAUDE_CONFIG_DIR + ppid + start_ts)[:12]`. Trade is between ease-of-debug and auto-resume after a shell exec.
- **Virtual index persistence.** Persist across invocations (current spec) or rebuild from `HEAD` each time? Persistence is faster but introduces orphan-state failure modes.
- **`git stash` semantics.** Do we need a per-agent stash? The virtual index already isolates work-in-progress; an explicit stash command might still help for "park this for later, work on something else" workflows.
- **Submodules.** Support in v1 or defer? They complicate the virtual index (each submodule has its own index).
- **LFS.** Support in v1 or defer? `git lfs` filters need to run during `git apply --cached`, which works in principle but needs testing.
- **Distribution shape.** Single static Go binary that self-installs hooks (current preference) vs a binary + a separate hooks installer script. Single binary keeps install-time fragility low but means the binary must include hook templates.
- **Reflog semantics for agent-private refs.** If we add per-agent refs (e.g. `refs/safegit/agents/<sid>/HEAD`), do they get a reflog? Probably yes, but only as long as the session lives.
- **Three-way merge.** Whether v1 includes `safegit merge` with three-way merge or only fast-forward. Three-way merge requires a conflict-handling story safegit currently lacks (the design hand-waves it: "conflicts surface as data" per the requirements doc, but the mechanism is undefined).
- **Sub-hunk staging precision.** Currently rounds `--lines L1-L2` up to the enclosing hunk(s). True sub-hunk precision requires synthesizing a smaller patch; deferred.
- **Cross-machine support.** Explicitly out of scope for v1. Should we add `lock.allowCrossHost = false` as a config (default) and document the upgrade path for future versions?
