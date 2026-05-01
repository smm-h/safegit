// Package coord implements the coordination layer for concurrent agents.
// It checks whether the working tree is clean before allowing tree-mutating
// operations (checkout, merge, rebase, reset, pull) to proceed.
package coord

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/smm-h/safegit/internal/git"
)

// DirtyState describes why the working tree is not clean.
type DirtyState struct {
	ModifiedFiles []string // status code + path from git status --porcelain
	WipLocks      []string // "ref  (file)" entries from wip-locks/ directory
}

// Check inspects the working tree and wip state. Returns nil if clean.
func Check(safegitDir string) (*DirtyState, error) {
	var ds DirtyState

	// 1. Parse git status --porcelain for modified/untracked files
	ctx := context.Background()
	stdout, _, err := git.Run(ctx, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return nil, fmt.Errorf("running git status: %w", err)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		ds.ModifiedFiles = append(ds.ModifiedFiles, line)
	}

	// 2. Scan wip-locks/ directory for active locks
	wipLocksDir := filepath.Join(safegitDir, "wip-locks")
	entries, err := os.ReadDir(wipLocksDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading wip-locks dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(wipLocksDir, e.Name()))
		if err != nil {
			continue
		}
		wipID := strings.TrimSpace(string(data))
		if wipID == "" {
			continue
		}
		// Read the wip commit message to find which files it covers
		ref := "refs/safegit/wip/" + wipID
		files := wipFilesFromRef(ref)
		entry := ref
		if files != "" {
			entry = ref + "  (" + files + ")"
		}
		ds.WipLocks = append(ds.WipLocks, entry)
	}

	if len(ds.ModifiedFiles) == 0 && len(ds.WipLocks) == 0 {
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

	if len(d.WipLocks) > 0 {
		b.WriteString("\nActive wips:\n")
		for _, w := range d.WipLocks {
			fmt.Fprintf(&b, "  %s\n", w)
		}
	}

	b.WriteString("\nSuggestion:\n")
	b.WriteString("  safegit commit -m \"<msg>\" -- <files>\n")
	b.WriteString("  safegit wip <files>\n")
	b.WriteString("  Or pass --force to override (you may lose work).\n")

	return b.String()
}

// wipFilesFromRef reads file paths from a wip commit message.
// Supports both new ("file: " per line) and legacy ("files: " comma-separated) formats.
func wipFilesFromRef(ref string) string {
	ctx := context.Background()
	sha, err := git.RevParse(ref)
	if err != nil {
		return ""
	}
	out, _, err := git.Run(ctx, "log", "-1", "--format=%B", sha)
	if err != nil {
		return ""
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "file: ") {
			files = append(files, strings.TrimPrefix(line, "file: "))
		}
	}
	if len(files) > 0 {
		return strings.Join(files, ", ")
	}
	// Legacy format
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "files: ") {
			return strings.TrimPrefix(line, "files: ")
		}
	}
	return ""
}
