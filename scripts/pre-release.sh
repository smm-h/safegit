#!/usr/bin/env bash
# Pre-release validation hook.
# Runs before rlsbl creates a release. Exit non-zero to abort.
# Add your project-specific checks here (e.g., tests, linting, audit).

set -euo pipefail

echo "Running pre-release checks..."

# Example: run tests if available
# npm test 2>/dev/null || true

echo "Pre-release checks passed."
