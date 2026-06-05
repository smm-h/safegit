package commit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/git"
)

// detectMoves identifies files that were moved (same content, new path) and
// auto-stages the corresponding deletions in the custom index. It returns
// the repo-relative paths of all auto-staged deletions.
func detectMoves(
	ctx context.Context,
	parentSHA string,
	indexPath string,
	absFiles []string,
	fileSpecs []FileSpec,
	repoRoot string,
) ([]string, error) {
	if parentSHA == "" {
		return nil, nil
	}

	if len(absFiles) != len(fileSpecs) {
		return nil, fmt.Errorf("detectMoves: absFiles length (%d) != fileSpecs length (%d)", len(absFiles), len(fileSpecs))
	}

	// Get all blobs in the parent tree.
	entries, err := git.LsTreeAll(ctx, parentSHA)
	if err != nil {
		return nil, err
	}

	pathToBlob := make(map[string]string, len(entries))
	blobToPaths := make(map[string][]string)
	for _, e := range entries {
		pathToBlob[e.Path] = e.SHA
		blobToPaths[e.SHA] = append(blobToPaths[e.SHA], e.Path)
	}

	// Build a set of explicitly-listed repo-relative paths.
	explicitSet := make(map[string]struct{}, len(absFiles))
	for _, abs := range absFiles {
		rel, err := filepath.Rel(repoRoot, abs)
		if err != nil {
			return nil, err
		}
		explicitSet[rel] = struct{}{}
	}

	// Identify new files (whole-file additions not present in parent tree).
	type newFile struct {
		absPath string
		relPath string
	}
	var newFiles []newFile
	for i, abs := range absFiles {
		if fileSpecs[i].Hunks != nil {
			continue
		}
		// Skip files that don't exist on disk — they are deletions, not
		// new additions that could be the destination of a move.
		if _, err := os.Lstat(abs); err != nil {
			continue
		}
		rel, err := filepath.Rel(repoRoot, abs)
		if err != nil {
			return nil, err
		}
		if _, exists := pathToBlob[rel]; exists {
			continue
		}
		newFiles = append(newFiles, newFile{absPath: abs, relPath: rel})
	}

	if len(newFiles) == 0 {
		return nil, nil
	}

	var staged []string
	for _, nf := range newFiles {
		blobSHA, err := git.HashObject(ctx, nf.absPath)
		if err != nil {
			return nil, err
		}

		candidates := blobToPaths[blobSHA]
		if len(candidates) == 0 {
			continue
		}

		// Filter to deleted candidates not in the explicit set.
		var deleted []string
		for _, cp := range candidates {
			if _, isExplicit := explicitSet[cp]; isExplicit {
				continue
			}
			if _, err := os.Lstat(filepath.Join(repoRoot, cp)); err == nil {
				continue
			}
			deleted = append(deleted, cp)
		}

		if len(deleted) == 0 {
			continue
		}

		// Pick the best match by path similarity.
		best := deleted[0]
		if len(deleted) > 1 {
			sort.Slice(deleted, func(i, j int) bool {
				si := pathSimilarity(nf.relPath, deleted[i])
				sj := pathSimilarity(nf.relPath, deleted[j])
				if si != sj {
					return si > sj
				}
				return deleted[i] < deleted[j]
			})
			best = deleted[0]
		}

		if err := git.RmCached(ctx, indexPath, filepath.Join(repoRoot, best)); err != nil {
			return nil, err
		}
		staged = append(staged, best)
	}

	return staged, nil
}

// pathSimilarity scores how similar two repo-relative paths are.
// Used to tiebreak when multiple deleted paths share the same blob SHA.
// Higher score = more similar.
func pathSimilarity(a, b string) int {
	score := 0

	if filepath.Base(a) == filepath.Base(b) {
		score += 10
	}

	partsA := strings.Split(a, "/")
	partsB := strings.Split(b, "/")

	// Count matching path components from the end (common suffix).
	// Start from len-2 to skip the basename (already scored above).
	// Only run the loop when both paths have at least 2 components.
	ia := len(partsA) - 2
	ib := len(partsB) - 2
	for ia >= 0 && ib >= 0 {
		if partsA[ia] != partsB[ib] {
			break
		}
		score++
		ia--
		ib--
	}

	return score
}
