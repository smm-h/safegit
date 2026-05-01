# Changelog

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
