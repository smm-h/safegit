#!/usr/bin/env bash
# Post-release hook. Runs after a successful release (non-fatal).
# Environment: RLSBL_VERSION is set to the released version.
# Customize this for your project (e.g., local install, deploy, notify).

set -euo pipefail

echo "Post-release: v$RLSBL_VERSION"
