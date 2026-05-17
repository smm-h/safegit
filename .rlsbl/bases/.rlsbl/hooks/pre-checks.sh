#!/usr/bin/env bash
set -euo pipefail
# This hook runs BEFORE built-in pre-release checks (tests, lint).
# Use it for setup tasks: starting services, setting env vars, etc.
# Built-in checks run after this hook. Custom validation goes in pre-release.sh.
