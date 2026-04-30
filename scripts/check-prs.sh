#!/usr/bin/env bash
# List open PRs for awareness at session start.
# Safe to run in hooks -- always exits 0.
cd "$(git rev-parse --show-toplevel 2>/dev/null)" || exit 0
count=$(gh pr list --state open --json number --jq length 2>/dev/null) || exit 0
if [ "$count" -gt 0 ]; then
  echo "Open PRs: $count"
  gh pr list --state open
fi
exit 0
