// Package coord implements the coordination layer for concurrent agents.
// It checks whether the working tree is clean before allowing tree-mutating
// operations (checkout, merge, rebase, reset, pull) to proceed.
package coord

import (
	"context"
	"fmt"
	"strings"

	"github.com/smm-h/safegit/internal/git"
)

// DirtyState describes why the working tree is not clean.
type DirtyState struct {
	ModifiedFiles []string // status code + path from git status --porcelain
}

// Check inspects the working tree. Returns nil if clean.
func Check(ctx context.Context, safegitDir string) (*DirtyState, error) {
	var ds DirtyState

	// 1. Check for tracked modifications by diffing working tree against HEAD directly.
	// This avoids relying on the main .git/index which may be stale after safegit commits.
	stdout, _, err := git.Run(ctx, "diff", "HEAD", "--name-status")
	if err != nil {
		return nil, fmt.Errorf("running git diff HEAD: %w", err)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		ds.ModifiedFiles = append(ds.ModifiedFiles, line)
	}

	// 2. Check for untracked files
	untracked, _, err := git.Run(ctx, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("running git ls-files: %w", err)
	}
	for _, line := range strings.Split(untracked, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ds.ModifiedFiles = append(ds.ModifiedFiles, "? "+line)
	}

	if len(ds.ModifiedFiles) == 0 {
		return nil, nil
	}
	return &ds, nil
}

// Refuse formats a refusal message from a DirtyState.
func (d *DirtyState) Refuse(operation string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "safegit: working tree is not clean; refusing %s to avoid clobbering uncommitted work.\n", operation)

	if len(d.ModifiedFiles) > 0 {
		b.WriteString("\nModified files:\n")
		for _, f := range d.ModifiedFiles {
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}

	b.WriteString("\nSuggestion:\n")
	b.WriteString("  safegit commit -m \"<msg>\" -- <files>\n")
	b.WriteString("  Or pass --force to override (you may lose work).\n")

	return b.String()
}

