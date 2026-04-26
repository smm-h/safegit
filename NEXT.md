# Next Steps

Status: design locked (see design.md). Implementation not started.

## Phase 0 -- Scaffold
- go mod init (module path: TBD; suggest `github.com/<user>/safegit`)
- Repo layout: cmd/safegit/, internal/{repo,index,wip,coord,hooks}/, pkg/, testdata/, .github/workflows/
- Single static binary build (CGO_ENABLED=0)
- CI: golangci-lint + go test + cross-compile check on PR
- Bare CLI skeleton: `safegit version`, `safegit help`

## Phase 1 -- Core Data Model
- .git/safegit/ directory layout (tmp/, locks/, wip-locks/, log file)
- Per-invocation tmp index allocator (mkdir tmp/<pid>-<random>)
- Ref-lock primitive (flock-based, with stale recovery via PID liveness)
- Op log appender (.git/safegit/log, JSON lines)

## Phase 2 -- Commit Pipeline
- safegit commit -m "msg" -- file1 file2
- read-tree HEAD into tmp index -> apply file changes -> write-tree -> commit-tree -> update-ref CAS
- CAS retry policy from design.md
- Tests: T1, T2, T3, T4 from concurrency suite

## Phase 3 -- Hunk Staging
- safegit stage <file> [--hunks N,M]
- Diff parser (split on @@ markers)
- git apply --cached --index against tmp index
- safegit unstage <file>

## Phase 4 -- Wip
- safegit wip <file...> -> refs/safegit/wip/<id>
- Per-file lock files at .git/safegit/wip-locks/<hash>
- safegit unwip <id>, safegit wip list
- Refuse second wip on locked file

## Phase 5 -- Coordination Layer
- Detection: git status --porcelain scan
- Affected commands: safegit checkout, pull, rebase, merge, reset --hard, bisect
- --force bypass flag

## Phase 6 -- Pre-pre-push Hook
- safegit push pipeline
- Run .git/hooks/pre-pre-push (and .d/ directory) before opening transport
- Standard pre-push still runs after transport opens (via git push)

## Phase 7 -- CLI Completeness
- Lifecycle: init, doctor
- Inspection: status, log, diff, show (proxy)
- Branching: branch, checkout, merge
- Remote: push, pull, fetch
- Recovery: unlock, gc

## Phase 8 -- Concurrency Tests
- Implement T1-T17 from design.md section 9
- go test -race -count=10
- Stress test on a real corpus

## Open before any code

- Module path / repo location decision (github org? gitlab? self-hosted?)
- License (MIT? Apache-2.0?)
- Whether to vendor a minimal git CLI invocation library or shell out via os/exec directly

## Reference

- design.md -- full architecture
- req.md -- original requirements
- research.md -- survey of jj, GitButler, Sapling, go-git, etc.
