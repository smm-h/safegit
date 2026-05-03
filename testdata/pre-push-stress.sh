#!/usr/bin/env bash
# Pre-push stress test: run concurrency safety regression checks.
# Called by .git/hooks/pre-push after rlsbl's pre-push-check.
# Skip: SKIP_STRESS=1 git push

set -euo pipefail

# Skip for tag-only pushes (stdin lines have: local_ref local_sha remote_ref remote_sha)
PUSHING_BRANCH=false
while IFS=' ' read -r local_ref _ _ _; do
  case "$local_ref" in
    refs/tags/*) ;;
    *) PUSHING_BRANCH=true ;;
  esac
done
if [ "$PUSHING_BRANCH" = true ] && [ "${SKIP_STRESS:-}" != "1" ]; then
  if [ -f testdata/stress ]; then
    echo "Running stress tests (skip with SKIP_STRESS=1)..."
    testdata/stress 2
  fi
fi
