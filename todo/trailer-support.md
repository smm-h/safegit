# Support --trailer flag in safegit commit

## Problem

`git commit --trailer "Key: Value"` atomically appends trailers to the commit message during commit. safegit's `commit` command accepts `-m` but does not forward `--trailer` to the underlying git command. Users who want trailers (e.g., `Signed-off-by`, `Co-authored-by`, or custom AI agent traceability trailers) must manually edit commits after the fact.

## Proposed fix

Forward `--trailer` flags from `safegit commit` to the underlying `git commit` call. Multiple `--trailer` flags should be supported (git allows repeating the flag).

```bash
safegit commit -m "feat: add feature" --trailer "Agent-Session: abc123" -- file1 file2
```

## Effort

Small. Parse and forward the flag in the commit command handler.
