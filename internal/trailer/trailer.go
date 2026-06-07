// Package trailer injects git trailers (key-value metadata lines) into commit messages for AI agent traceability and session attribution.
package trailer

import (
	"os"
	"regexp"
	"strings"
)

// SessionKey is the git trailer key used to record the Claude Code session ID.
const SessionKey = "Claude-Code-Session-Id"

const envVar = "CLAUDE_CODE_SESSION_ID"

// trailerLine matches lines of the form "Key-Name: value".
var trailerLine = regexp.MustCompile(`^[A-Za-z0-9][-A-Za-z0-9]*:\s`)

// Inject reads CLAUDE_CODE_SESSION_ID from the environment and appends
// a Claude-Code-Session-Id trailer to the commit message if present.
// For amend: deduplicates if the same session ID already exists as a trailer;
// keeps both if a different session's trailer is present.
func Inject(message string) string {
	sessionID := os.Getenv(envVar)
	if sessionID == "" {
		return message
	}

	trailer := SessionKey + ": " + sessionID

	// Check if this exact trailer already exists (dedup for same-session amend)
	for _, line := range strings.Split(message, "\n") {
		if strings.TrimSpace(line) == trailer {
			return message
		}
	}

	// Find whether the message already ends with a trailer block.
	// A trailer block is one or more consecutive trailer-format lines at the end,
	// preceded by an empty line (or at the start of the message).
	trimmed := strings.TrimRight(message, "\n")
	lines := strings.Split(trimmed, "\n")

	if endsWithTrailerBlock(lines) {
		// Append directly to the existing trailer block
		return trimmed + "\n" + trailer + "\n"
	}

	// No existing trailer block: add blank line separator before trailer
	return trimmed + "\n\n" + trailer + "\n"
}

// AppendCustom appends user-provided trailers to the commit message.
// Each trailer should be in "Key: Value" format. If trailers is empty,
// the message is returned unchanged. Follows the same format as Inject:
// appends to an existing trailer block, or adds a blank line separator first.
func AppendCustom(message string, trailers []string) string {
	if len(trailers) == 0 {
		return message
	}

	trimmed := strings.TrimRight(message, "\n")
	lines := strings.Split(trimmed, "\n")

	var b strings.Builder
	if endsWithTrailerBlock(lines) {
		b.WriteString(trimmed)
		b.WriteByte('\n')
	} else {
		b.WriteString(trimmed)
		b.WriteString("\n\n")
	}
	for _, t := range trailers {
		b.WriteString(t)
		b.WriteByte('\n')
	}
	return b.String()
}

// endsWithTrailerBlock returns true if the lines end with a block of
// trailer-format lines preceded by a blank line (or if the entire message
// is trailer lines, which happens with single-line messages that look like trailers).
func endsWithTrailerBlock(lines []string) bool {
	if len(lines) == 0 {
		return false
	}

	// Walk backwards from the end to find consecutive trailer lines
	i := len(lines) - 1
	for i >= 0 && trailerLine.MatchString(lines[i]) {
		i--
	}

	trailerCount := len(lines) - 1 - i
	if trailerCount == 0 {
		return false
	}

	// The line before the trailer block must be blank (standard git trailer convention)
	if i >= 0 && strings.TrimSpace(lines[i]) == "" {
		return true
	}

	return false
}
