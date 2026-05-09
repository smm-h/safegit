# safegit

## Release workflow

This project uses [rlsbl](https://github.com/smm-h/rlsbl) for release orchestration.

- Update CHANGELOG.md with a `## X.Y.Z` entry describing changes
- Run `rlsbl release [patch|minor|major]` to bump version and create a GitHub Release
- CI handles publishing automatically via the publish workflow
- Never publish manually — always use `rlsbl release`
- GoReleaser handles binary publishing via GitHub Actions (no secrets needed)
- Use `rlsbl release --dry-run` to preview a release without making changes

## Conventions

- No tokens or secrets in command-line arguments (use env vars or config files)
- All file writes to shared state should be atomic (write to tmp, then rename)
- External calls (APIs, CLI tools) must have timeouts and graceful fallbacks
- Use `npm link` (npm) or `uv pip install -e .` (Python) for local development
- CI runs smoke tests on every push; manual testing for UI/UX changes
