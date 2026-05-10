#!/usr/bin/env bash
# Post-release hook. Runs after a successful rlsbl release.
# Installs the freshly built safegit binary to $GOBIN (or $GOPATH/bin).

set -euo pipefail

echo "Installing safegit v$RLSBL_VERSION..."
go install .
echo "Installed: $(which safegit)"
