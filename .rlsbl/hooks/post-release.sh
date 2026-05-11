#!/usr/bin/env bash
# Post-release hook. Runs after a successful rlsbl release.
# Installs the freshly built safegit binary to $GOBIN (or $GOPATH/bin).

set -euo pipefail

echo "Installing safegit v$RLSBL_VERSION..."
go install .
echo "Installed: $(which safegit)"

if [ -f ~/Projects/.env ]; then
  set -a && source ~/Projects/.env && set +a
  export CLOUDFLARE_API_TOKEN="${CF_PAGES_API_TOKEN:-}"
  export CLOUDFLARE_ACCOUNT_ID="${CF_ACCOUNT_ID:-}"
fi
echo "Building and deploying docs..."
selfdoc build && selfdoc deploy
