# Session Trailers for AI Agent Traceability

## Context

safegit is a Go CLI that wraps `git commit` for safe concurrent multi-agent use. When multiple AI agent sessions share a worktree and commit via safegit, there is no way to trace which session produced which commit. `git blame` shows authorship but not which agent session was responsible. Adding git trailers (RFC 822-style key-value pairs at the end of commit messages) would solve this.

The proposed trailers:

- `Safegit-Session: <session-id>` -- identifies the specific agent session
- `Safegit-Agent: <agent-string>` -- identifies the agent software and version

## Problem

Multiple AI agent sessions committing to the same repo via safegit are indistinguishable in git history. There is no structured metadata linking a commit to the session that created it. This makes debugging, auditing, and attribution difficult when things go wrong.

## Research: Available Identifiers

| Identifier | Source | Availability | Session-specific |
|---|---|---|---|
| `AI_AGENT` env var | Claude Code | Always set in Bash subprocesses | No (agent-level, not session-level) |
| `session_id` in hook JSON stdin | Claude Code hooks | Only in hook stdin, not env | Yes |
| `CLAUDE_CODE_REMOTE_SESSION_ID` | Claude Code cloud | Only in remote/cloud sessions | Yes |
| `CLAUDE_SESSION_ID` env var | Does not exist yet | N/A (frequently requested upstream, multiple open GitHub issues) | Would be yes |

Key finding: there is currently no way for an external tool to automatically obtain a per-session ID from a local Claude Code CLI session via environment variables alone. The `session_id` is available in hook JSON stdin but not exported to the environment.

## Solutions

### Option A: Generic `SAFEGIT_SESSION` env var

safegit reads `SAFEGIT_SESSION` at commit time and appends a `Safegit-Session: <value>` trailer to the commit message.

| Pros | Cons |
|---|---|
| Simple implementation | Not automatic; caller must set the env var |
| Agent-agnostic; works with any tool | No traceability if caller forgets to set it |
| Zero coupling to any specific AI platform | |
| Caller controls the identifier format | |

### Option B: Claude Code hook that exports session_id

Install a Claude Code `SessionStart` hook that extracts `session_id` from the hook's JSON stdin and writes it to a known file (e.g., `.safegit-session`). safegit reads that file at commit time.

| Pros | Cons |
|---|---|
| Fully automatic for Claude Code users | Couples safegit to Claude Code's hook system |
| No manual env var setup needed | Fragile: depends on hook JSON schema stability |
| | Complex setup: requires hook installation and file management |
| | Does not help non-Claude-Code agents |
| | Stale file risk if hook fails or session ends without cleanup |

### Option C: Hybrid -- `SAFEGIT_SESSION` + auto-detect `AI_AGENT`

Two independent behaviors:

- **Agent detection**: automatically read the `AI_AGENT` env var (if set) and add a `Safegit-Agent: <value>` trailer. Zero configuration needed; this is free metadata.
- **Session identification**: read `SAFEGIT_SESSION` env var (if set) and add a `Safegit-Session: <value>` trailer. Opt-in by the caller.

| Pros | Cons |
|---|---|
| Agent type is captured automatically with zero setup | Two trailer keys to manage |
| Session ID available when the caller provides it | Session ID still requires manual setup |
| Forward-compatible: when `CLAUDE_SESSION_ID` ships upstream, add it to the fallback chain | Slightly more implementation surface than Option A alone |
| Useful even without a session ID (agent type alone has value) | |
| Agent-agnostic for session ID; auto-detect for known agents | |

### Option D: Wait for upstream `CLAUDE_SESSION_ID`

Do nothing now. Wait for Anthropic to ship `CLAUDE_SESSION_ID` as an env var in Claude Code, then read it automatically.

| Pros | Cons |
|---|---|
| Zero implementation effort now | No control over timeline (could be months or never) |
| Guaranteed to be the "right" identifier | No traceability in the meantime |
| | Only helps Claude Code, not other agents |

Can be combined with A or C as a future fallback source.

## Recommendation

**Option C (Hybrid)** is the recommended approach.

It provides immediate value via automatic `AI_AGENT` detection, opt-in session tracing via `SAFEGIT_SESSION`, and a clear path forward: when upstream eventually ships `CLAUDE_SESSION_ID`, add it to the fallback chain for automatic session detection without any user-facing changes.

The env var fallback chain for session ID would be:

1. `SAFEGIT_SESSION` (explicit, highest priority)
2. `CLAUDE_SESSION_ID` (future, auto-detect when available)
3. Absent (no session trailer added)

The env var detection for agent type would be:

1. `AI_AGENT` (auto-detect, currently set by Claude Code)
2. Absent (no agent trailer added)

## Behavioral Design Decisions

These need to be decided before implementation:

### When should trailers be added?

- **Always-on when env vars are present** (recommended): if the env var exists, the trailer is added. No flags needed. This is the least surprising behavior and matches how `GIT_AUTHOR_NAME` etc. work.
- **Opt-in via `--trailer` flag**: caller must explicitly request trailer injection. Safer but more friction.
- **Opt-out via `--no-trailer` flag**: trailers are added by default, caller can suppress. Good middle ground but adds flag complexity.

### Should trailer keys be configurable?

- **No** (recommended for now): fixed keys (`Safegit-Session`, `Safegit-Agent`) keep things simple and greppable. Configurability can be added later if needed.
- **Yes, via env vars or config**: e.g., `SAFEGIT_SESSION_KEY=My-Session-Id`. Adds flexibility but also complexity and documentation burden.

### What about human commits (no AI env vars set)?

- **No trailer** (recommended): if no env vars are detected, no trailers are added. Human commits remain clean. The absence of a trailer implicitly signals "human or unconfigured agent."
- **Add `Safegit-Session: human`**: explicit marker but adds noise to every human commit and is arguably wrong (a human does not have a "session" in the same sense).

### Interaction with `--amend` and `--branch`

- When amending, should existing trailers be preserved, replaced, or stripped?
- Recommended: replace with current session's trailers. The amending session is the one that should be traced to the final commit.

### Interaction with `safegit undo`

- The undo operation creates a new commit (revert). Should the revert commit also get session trailers?
- Recommended: yes, since the revert was performed by a specific session and that is useful to trace.

## Files and Directories That May Change

- `cmd/safegit/` -- commit subcommand flag registration (if opt-in/opt-out flags are added)
- `internal/commit/` or equivalent -- trailer injection into commit messages before passing to git
- Possibly a new `internal/trailer/` package -- encapsulates env var reading, trailer formatting, and fallback chain logic
- `internal/test/` -- integration tests for trailer behavior (env var set, env var absent, amend, undo)
- Unit tests alongside the new package

## Relative Effort

**Low to medium.**

The core feature is:

- Read two env vars
- Append zero, one, or two lines to the commit message before passing it to `git commit`
- Respect the git trailer format (blank line separator, `Key: Value` syntax)

The implementation is straightforward Go. The main cost is in:

- Design decisions (behavioral questions above)
- Integration tests covering the matrix of scenarios (env set/unset, amend, undo, concurrent commits)
- Ensuring trailer injection works correctly with safegit's per-invocation temporary index approach
