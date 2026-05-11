#!/usr/bin/env bash
# Post-release hook. Runs after a successful rlsbl release.
# Installs the freshly built safegit binary to $GOBIN (or $GOPATH/bin).

set -euo pipefail

echo "Installing safegit v$RLSBL_VERSION..."
go install .
echo "Installed: $(which safegit)"

if command -v selfdoc &>/dev/null && [ -f selfdoc.json ]; then
  [ -f ~/Projects/.env ] && set -a && source ~/Projects/.env && set +a
  echo "Building and deploying docs..."
  selfdoc build && selfdoc deploy
fi
