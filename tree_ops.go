package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/smm-h/safegit/internal/git"
)

// replaceInTree returns a new tree SHA with the blob at filePath replaced by
// newBlobSHA (or removed if newBlobSHA is empty). The original tree is never
// mutated. If filePath does not exist in the tree, the original treeSHA is
// returned unchanged (no error) so the scrub walker can skip commits where
// the file is absent.
func replaceInTree(ctx context.Context, treeSHA string, filePath string, newBlobSHA string) (string, error) {
	segments := strings.SplitN(filePath, "/", 2)
	name := segments[0]

	entries, err := git.LsTree(ctx, treeSHA)
	if err != nil {
		return "", fmt.Errorf("ls-tree %s: %w", treeSHA, err)
	}

	idx := -1
	for i, e := range entries {
		if e.Path == name {
			idx = i
			break
		}
	}

	// File not found in this tree level -- return original tree unchanged.
	if idx < 0 {
		return treeSHA, nil
	}

	if len(segments) == 2 {
		// Nested path: recurse into the subtree.
		subtreeEntry := entries[idx]
		if subtreeEntry.ObjectType != "tree" {
			// Path component exists but is not a tree -- file not found.
			return treeSHA, nil
		}
		newSubtreeSHA, err := replaceInTree(ctx, subtreeEntry.BlobSHA, segments[1], newBlobSHA)
		if err != nil {
			return "", err
		}
		// If the subtree didn't change (file not found deeper), propagate.
		if newSubtreeSHA == subtreeEntry.BlobSHA {
			return treeSHA, nil
		}
		entries[idx].BlobSHA = newSubtreeSHA
	} else {
		// Base case: leaf entry.
		if newBlobSHA == "" {
			// Remove the entry.
			entries = append(entries[:idx], entries[idx+1:]...)
		} else {
			// Replace the blob SHA, preserving mode and type.
			entries[idx].BlobSHA = newBlobSHA
		}
	}

	newTreeSHA, err := git.MkTree(ctx, entries)
	if err != nil {
		return "", fmt.Errorf("mktree: %w", err)
	}
	return newTreeSHA, nil
}
