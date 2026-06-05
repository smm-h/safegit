// Package scan iterates all git objects (reachable and unreachable) and
// matches patterns against their textual content. Blobs, commit messages,
// and tag annotations are searched. Binary blobs (NUL in first 8KB) are
// skipped. Tree objects are excluded by the cat-file iterator.
package scan

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/smm-h/safegit/internal/git"
)

// Match records a single pattern hit inside a git object.
type Match struct {
	SHA        string // object SHA
	ObjectType string // "blob", "commit", "tag"
	Line       int    // 1-based line number of match
	Path       string // file path (for blobs; empty for commits/tags) -- reserved for future attribution
	CommitSHA  string // which commit contains this blob -- reserved for future attribution
	Reachable  bool   // whether this object is reachable from current refs
	Context    string // surrounding text with match replaced by <MATCH>
	IsBinary   bool   // true if this was a binary blob (skipped)
}

// ScanResults holds the aggregate output of a scan across all git objects.
type ScanResults struct {
	Matches []Match
	Skipped int // count of binary blobs skipped
	Scanned int // count of objects scanned
}

// contextRadius is the number of characters to include before and after
// a match in the Context snippet.
const contextRadius = 40

// binaryCheckSize is how many bytes to inspect for NUL to detect binary content.
const binaryCheckSize = 8192

// ScanObjects iterates every object in the git object store and searches
// for pattern matches. Binary blobs are detected and skipped. Commit and
// tag bodies (everything after the first blank line) are searched.
func ScanObjects(ctx context.Context, pattern *regexp.Regexp) (*ScanResults, error) {
	// Build the reachable set: all object SHAs reachable from any ref.
	reachable, err := buildReachableSet(ctx)
	if err != nil {
		return nil, fmt.Errorf("build reachable set: %w", err)
	}

	iter, err := git.CatFileBatchAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("cat-file batch-all: %w", err)
	}
	defer iter.Close()

	results := &ScanResults{}

	for {
		entry, err := iter.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("iterate objects: %w", err)
		}

		results.Scanned++
		isReachable := reachable[entry.SHA]

		switch entry.Type {
		case "blob":
			if isBinary(entry.Content) {
				results.Skipped++
				continue
			}
			matches := findMatches(entry.SHA, "blob", entry.Content, pattern, isReachable)
			results.Matches = append(results.Matches, matches...)

		case "commit":
			body := extractBody(entry.Content)
			if len(body) == 0 {
				continue
			}
			matches := findMatches(entry.SHA, "commit", body, pattern, isReachable)
			results.Matches = append(results.Matches, matches...)

		case "tag":
			body := extractBody(entry.Content)
			if len(body) == 0 {
				continue
			}
			matches := findMatches(entry.SHA, "tag", body, pattern, isReachable)
			results.Matches = append(results.Matches, matches...)
		}
	}

	return results, nil
}

// buildReachableSet runs git rev-list --all --objects and collects every
// SHA into a set.
func buildReachableSet(ctx context.Context) (map[string]bool, error) {
	stdout, _, err := git.Run(ctx, "rev-list", "--all", "--objects")
	if err != nil {
		return nil, err
	}

	set := make(map[string]bool)
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Lines are "<sha>" or "<sha> <path>" (for blobs).
		sha := line
		if idx := strings.IndexByte(line, ' '); idx > 0 {
			sha = line[:idx]
		}
		set[sha] = true
	}
	return set, nil
}

// isBinary returns true if any byte in the first binaryCheckSize bytes is NUL.
func isBinary(content []byte) bool {
	limit := len(content)
	if limit > binaryCheckSize {
		limit = binaryCheckSize
	}
	return bytes.ContainsRune(content[:limit], 0)
}

// extractBody returns the body portion of a commit or tag object -- everything
// after the first blank line ("\n\n"). Returns nil if there is no body.
func extractBody(content []byte) []byte {
	idx := bytes.Index(content, []byte("\n\n"))
	if idx < 0 {
		return nil
	}
	return content[idx+2:]
}

// findMatches searches content line-by-line for pattern, returning a Match
// for each hit with line number and a context snippet.
func findMatches(sha, objType string, content []byte, pattern *regexp.Regexp, reachable bool) []Match {
	var matches []Match
	lines := bytes.Split(content, []byte("\n"))

	for i, line := range lines {
		locs := pattern.FindAllIndex(line, -1)
		if len(locs) == 0 {
			continue
		}
		for _, loc := range locs {
			ctx := buildContext(string(line), loc[0], loc[1])
			matches = append(matches, Match{
				SHA:        sha,
				ObjectType: objType,
				Line:       i + 1, // 1-based
				Reachable:  reachable,
				Context:    ctx,
			})
		}
	}
	return matches
}

// buildContext extracts a snippet around a match, replacing the matched text
// with "<MATCH>" to avoid printing secrets.
func buildContext(line string, matchStart, matchEnd int) string {
	// Compute the window boundaries.
	ctxStart := matchStart - contextRadius
	if ctxStart < 0 {
		ctxStart = 0
	}
	ctxEnd := matchEnd + contextRadius
	if ctxEnd > len(line) {
		ctxEnd = len(line)
	}

	before := line[ctxStart:matchStart]
	after := line[matchEnd:ctxEnd]

	var sb strings.Builder
	if ctxStart > 0 {
		sb.WriteString("...")
	}
	sb.WriteString(before)
	sb.WriteString("<MATCH>")
	sb.WriteString(after)
	if ctxEnd < len(line) {
		sb.WriteString("...")
	}
	return sb.String()
}
