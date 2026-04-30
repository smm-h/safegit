#!/usr/bin/env bash
# Record a demo GIF for README. Requires: vhs (https://github.com/charmbracelet/vhs).
# Usage: ./scripts/record-gif.sh [duration_seconds]
set -euo pipefail

DURATION="${1:-10}"
ASSETS_DIR="assets"

mkdir -p "$ASSETS_DIR"

if ! command -v vhs &>/dev/null; then
  echo "Error: vhs is required."
  echo "Install: go install github.com/charmbracelet/vhs@latest"
  exit 1
fi

TAPE=$(mktemp /tmp/record-XXXX.tape)
cat > "$TAPE" <<EOF
Set FontFamily "monospace"
Set FontSize 24
Set Width 1200
Set Height 600
Set TypingSpeed 50ms
Type "safegit --help"
Enter
Sleep 3s
EOF

echo "Recording demo..."
vhs "$TAPE" -o "$ASSETS_DIR/demo.gif"
rm -f "$TAPE"

echo "Done. GIF saved to $ASSETS_DIR/demo.gif"
echo "Edit this script to customize the recording."
