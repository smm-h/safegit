# Future Features

Extracted from design.md. These were explicitly deferred from v1.

## Staging precision

- **Line-level staging:** `safegit commit -- file.txt:L1-L2` to stage specific lines within a hunk. Currently only whole hunks are supported via `file:hunk-spec`.
- **Sub-hunk staging:** Stage some lines of a hunk, leave others. Requires synthesizing a smaller patch via line-level diff.

## Lock wakeup

- **inotify/kqueue wakeup:** Replace exponential-backoff polling (10ms-1s) with filesystem watchers for near-instant lock-release wakeup. Would reduce latency under contention.

## Broader compatibility

- **Cross-machine support:** NFS distributed coordinator, cross-host PID liveness. Currently same-machine only with hostname check refusing cross-host lock reclaim.
- **Submodules:** Per-submodule coordination extensions. Currently refuses to init on repos with `.gitmodules`.
- **Git LFS:** Smudge/clean filters during `git apply --cached`. Currently refuses to init on repos with `filter=lfs`.
- **Windows support:** Four packages use Unix-only syscalls (flock, kill, /proc, Setpgid). Would need platform-specific files throughout.

## Advanced features

- **Per-agent virtual working tree:** GitButler-style workspace isolation where each agent sees its own view of the working tree. Would eliminate the coordination guard's "must be clean" requirement. Requires FUSE or file-overlay.
- **Persistent virtual indexes:** Incremental staging across invocations (`safegit stage` then later `safegit commit`). Currently the index is rebuilt every invocation.
- **`safegit absorb`:** Auto-distribute hunks into the right commit in a stack (jj-style).
- **`pre-pre-fetch` hook:** Symmetric to pre-pre-push for fetch operations.

## Enforcement

- **`SAFEGIT_AUTHORIZED` env var + git hook:** Install a `pre-commit` hook that aborts unless `SAFEGIT_AUTHORIZED=1` is set. Would prevent raw `git commit` from bypassing safegit. Currently relies on convention and `safegit doctor` bypass detection.

## Standalone stage/unstage

- **`safegit stage` / `safegit unstage`:** Preview and validate staging operations without committing. Currently folded into `safegit commit -- file:hunk-spec`.
