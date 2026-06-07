# Submodule support

## Current limitation

safegit refuses to operate in repositories that contain `.gitmodules` or when the working directory is inside a submodule. This is a hard block -- no safegit commands work in these contexts.

## Why it exists

Submodules are separate git repositories with their own `.git` (or gitdir link), their own index, and their own HEAD. safegit's concurrency-safe locking and index management assumes a single repository context per invocation. Operating on the parent repo's index while a submodule has its own index (and vice versa) could corrupt either repo's state, especially under concurrent access from multiple sessions.

## What is needed

safegit should be able to:

- **Detect context**: distinguish between "I'm in the parent repo" and "I'm inside a submodule directory," and select the correct `.git` directory and index accordingly.
- **Commit inside submodules**: `safegit commit` should work when the cwd is within a submodule, operating on that submodule's index and HEAD -- not the parent's.
- **Undo inside submodules**: `safegit undo` should revert commits within the submodule context without touching the parent repo's recorded submodule pointer.
- **Parent repo awareness**: after committing inside a submodule, the parent repo's submodule pointer is dirty. safegit should either handle this automatically (commit the updated pointer in the parent) or surface a clear message about what the user needs to do next.
- **Locking**: lock files must be scoped to the correct `.git` directory -- a lock on the parent must not block operations inside a submodule, and vice versa.

## Use case

Monorepos that vendor forked dependencies as submodules need git operations inside those submodules. Agents working in these repos currently cannot use safegit at all, forcing them to fall back to raw git (which defeats safegit's concurrency safety guarantees). This is especially problematic when multiple agent sessions are active in the same worktree -- exactly the scenario safegit was built to handle.
