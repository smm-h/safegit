# Support undoing an undo (redo)

## Problem

`safegit undo` refuses to undo an undo with `error: cannot undo "undo"`. This isn't a deliberate safety guardrail — it's a structural gap: the `undoableOps` map only contains `commit`, `amend`, and `reword`. The undo oplog entry already stores all the information needed to reverse it (`oldSha` is the previous tip before the undo).

## Why it matters

When a user runs `safegit undo` and realizes they undid the wrong thing (e.g., undid another session's commit instead of their own), they're stuck. They have to manually `git update-ref` to recover. The oplog has all the data — it just refuses to use it.

## Root cause

`undo.go` line 19-23: `undoableOps` map doesn't include `"undo"`. Adding `"undo": "oldSha"` would mechanically enable redo, since every undo entry stores `{"sha": <target>, "oldSha": <previous tip>}`.

## Design considerations

Naively adding `"undo"` to `undoableOps` creates infinite oscillation: undo → redo → undo → redo (each operation logs a new entry that the next undo picks up). Options:

1. **One-level redo only:** Allow undoing an undo, but the resulting "redo" entry is NOT undoable. The `undoableOps` map has `"undo": "oldSha"` but the redo operation logs with a different op name (e.g., `"redo"`) that isn't in the map.

2. **Depth cap:** Allow N levels of undo/redo (e.g., 2). After the cap, refuse further undos.

3. **Undo targets the last non-undo op:** Instead of always undoing the most recent oplog entry, skip over undo/redo entries and target the last "real" operation (commit/amend/reword). This makes undo idempotent — running it twice undoes the same commit, not the undo.

## Also: session-scoped undo

The oplog records PID but never filters on it. `safegit undo` undoes whatever the last operation on the branch was, regardless of which session made it. In multi-session workflows, this means session A can accidentally undo session B's commit. The PID field exists but is unused — filtering by PID would make undo session-safe.

## Effort

Small for option 1 (one-level redo). Medium for option 3 (skip undo entries). Session-scoped undo is a separate but related improvement.
