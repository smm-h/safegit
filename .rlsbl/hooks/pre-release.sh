#!/usr/bin/env bash
# Pre-release validation hook.
# Runs before rlsbl creates a release. Exit non-zero to abort.

set -euo pipefail

echo "Running pre-release checks..."

echo "  go vet..."
go vet ./...

echo "  go build..."
go build -o /dev/null ./cmd/safegit

echo "  go test..."
go test ./... -race -short -count=1

echo "Pre-release checks passed."
