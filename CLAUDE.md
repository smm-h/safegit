# safegit

## Build and test

- `go build -o safegit ./cmd/safegit` to build
- `go test ./... -race` to run all tests with race detection
- `go test ./internal/test/ -race -count=5 -timeout=15m` for stress tests
- `scripts/stress` for a quick stress run

## Release workflow

This project uses [rlsbl](https://github.com/smm-h/rlsbl) for release orchestration.

- Update CHANGELOG.md with a `## X.Y.Z` entry describing changes
- Run `rlsbl release [patch|minor|major]` to bump version and create a GitHub Release
- CI handles publishing via goreleaser (cross-platform static binaries)
- Never publish manually -- always use `rlsbl release`
- Use `rlsbl release --dry-run` to preview a release without making changes

## Conventions

- Always use `safegit commit` (not raw `git commit`) when committing to this repo, if safegit is installed
- All git plumbing calls go through internal/git (never shell out to git directly from other packages)
- Per-invocation tmp indexes: never write to the shared .git/index
- All ref updates use CAS (compare-and-swap) via git update-ref with old-value argument
- Lock files use O_CREAT|O_EXCL for atomic creation
- Oplog entries must be < 4096 bytes (POSIX atomic append guarantee)
- Tests in internal/test/ are integration tests that build and run the safegit binary as a subprocess
- CGO_ENABLED=0 for all builds (static binary, no C dependencies)
- `safegit undo` reverses the last commit/amend/reword via oplog
- `safegit amend --branch` and `safegit reword --branch` operate on a branch other than HEAD
- stash, cherry-pick, revert are guarded passthroughs (coordination check before git); tag is unguarded passthrough
