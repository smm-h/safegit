// Package commit implements the two-phase commit pipeline.
// Phase A (parallel-safe): tmp index, validate, stage, write-tree, commit-tree.
// Phase B (serialized): ref lock, CAS check with retry, update-ref, oplog.
package commit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/index"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
)

// Exit codes for commit-specific errors.
const (
	ExitCASExhausted  = 7
	ExitWriteTree     = 9
	ExitCommitTree    = 10
)

// CommitError carries a structured exit code alongside the error message.
type CommitError struct {
	Code    int
	Message string
}

func (e *CommitError) Error() string { return e.Message }

// Pipeline orchestrates the full commit flow.
type Pipeline struct {
	SafegitDir string
	Config     repo.Config

	// PhaseADone is called (if non-nil) after Phase A completes but before
	// the ref lock is acquired. Used by tests to inject concurrent commits.
	PhaseADone func()
}

// CommitRequest holds all inputs for a single commit operation.
type CommitRequest struct {
	Message    string
	Files      []string
	Branch     string // empty = current branch
	AllowEmpty bool
	Force      bool // skip gitignore check
	DryRun     bool
}

// CommitResult is the JSON-serializable output of a successful commit.
type CommitResult struct {
	SHA      string `json:"sha"`
	Ref      string `json:"ref"`
	Parent   string `json:"parent"`
	Tree     string `json:"tree"`
	Attempts int    `json:"attempts"`
}

// Execute runs the full two-phase commit pipeline.
// On CAS miss it retries from Phase A up to Config.Commit.CASMaxAttempts times.
func (p *Pipeline) Execute(ctx context.Context, req CommitRequest) (*CommitResult, error) {
	repoRoot, err := git.RepoRoot()
	if err != nil {
		return nil, fmt.Errorf("resolving repo root: %w", err)
	}

	// Resolve target branch ref
	ref := req.Branch
	if ref == "" {
		ref, err = git.HeadRef()
		if err != nil {
			return nil, fmt.Errorf("resolving HEAD: %w", err)
		}
	}
	if !strings.HasPrefix(ref, "refs/") {
		ref = "refs/heads/" + ref
	}

	// Validate and normalize file paths before entering the retry loop
	absFiles, err := p.resolveFiles(repoRoot, req.Files, req.Force)
	if err != nil {
		return nil, err
	}

	maxAttempts := p.Config.Commit.CASMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, retry, err := p.tryCommit(ctx, ref, repoRoot, absFiles, req, attempt)
		if err != nil {
			return nil, err
		}
		if !retry {
			return result, nil
		}
		// CAS miss -- loop back to Phase A with fresh state
	}

	return nil, &CommitError{
		Code:    ExitCASExhausted,
		Message: fmt.Sprintf("CAS convergence failure after %d attempts on %s", maxAttempts, ref),
	}
}

// tryCommit runs one attempt of the two-phase pipeline.
// Returns (result, false, nil) on success, (nil, true, nil) on CAS miss,
// or (nil, false, err) on hard failure.
func (p *Pipeline) tryCommit(
	ctx context.Context,
	ref, repoRoot string,
	absFiles []string,
	req CommitRequest,
	attempt int,
) (*CommitResult, bool, error) {

	// --- Phase A: parallel-safe (no locks) ---

	// Step 1: Create per-invocation tmp index seeded from HEAD
	tmpIdx, err := index.New(p.SafegitDir)
	if err != nil {
		return nil, false, fmt.Errorf("creating tmp index: %w", err)
	}
	defer tmpIdx.Cleanup()

	// Step 3: Stage files into tmp index
	for _, absPath := range absFiles {
		if err := p.stageFile(tmpIdx.IndexPath, absPath); err != nil {
			return nil, false, fmt.Errorf("staging %s: %w", absPath, err)
		}
	}

	// Step 4: Build tree
	treeSHA, err := git.WriteTree(tmpIdx.IndexPath)
	if err != nil {
		return nil, false, &CommitError{Code: ExitWriteTree, Message: fmt.Sprintf("write-tree failed: %v", err)}
	}

	// Step 5: Snapshot parent
	parentSHA, err := git.RevParse(ref)
	if err != nil {
		return nil, false, fmt.Errorf("resolving parent %s: %w", ref, err)
	}

	// Check for empty commit (tree unchanged)
	if !req.AllowEmpty {
		parentTree, err := p.parentTreeSHA(parentSHA)
		if err != nil {
			return nil, false, fmt.Errorf("resolving parent tree: %w", err)
		}
		if treeSHA == parentTree {
			return nil, false, fmt.Errorf("nothing to commit (tree unchanged); use --allow-empty to override")
		}
	}

	// Step 6: Build commit object
	commitSHA, err := git.CommitTree(treeSHA, parentSHA, req.Message)
	if err != nil {
		return nil, false, &CommitError{Code: ExitCommitTree, Message: fmt.Sprintf("commit-tree failed: %v", err)}
	}

	// Hook for tests to inject concurrent commits between Phase A and Phase B
	if p.PhaseADone != nil {
		p.PhaseADone()
	}

	// DryRun: return result without touching the ref
	if req.DryRun {
		return &CommitResult{
			SHA:      commitSHA,
			Ref:      ref,
			Parent:   parentSHA,
			Tree:     treeSHA,
			Attempts: attempt,
		}, false, nil
	}

	// --- Phase B: serialized (per-ref lock + CAS) ---

	// Step 7: Acquire ref lock
	lockTimeout := time.Duration(p.Config.Lock.AcquireTimeoutSeconds) * time.Second
	if lockTimeout <= 0 {
		lockTimeout = 30 * time.Second
	}
	refLock, err := lock.Acquire(p.SafegitDir, ref, "commit", lockTimeout)
	if err != nil {
		return nil, false, fmt.Errorf("acquiring lock on %s: %w", ref, err)
	}
	defer refLock.Release()

	// Step 8: Re-resolve parent (CAS check)
	currentParent, err := git.RevParse(ref)
	if err != nil {
		return nil, false, fmt.Errorf("re-resolving %s for CAS: %w", ref, err)
	}
	if currentParent != parentSHA {
		// CAS miss: ref moved since Phase A -- retry
		return nil, true, nil
	}

	// Step 9: Update ref with CAS
	if err := git.UpdateRef(ref, commitSHA, parentSHA); err != nil {
		return nil, false, fmt.Errorf("update-ref CAS failed: %w", err)
	}

	// Step 10: Sync main index to match HEAD so git status/diff work correctly.
	// Non-fatal: the commit already landed, so a read-tree failure is just a warning.
	if err := git.SyncMainIndex("HEAD"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to sync main index: %v\n", err)
	}

	// Step 11: Lock released by defer

	// Step 12: Append op log
	_ = oplog.Append(p.SafegitDir, oplog.Entry{
		Op: "commit",
		Extra: map[string]interface{}{
			"ref":      ref,
			"tree":     treeSHA,
			"parent":   parentSHA,
			"sha":      commitSHA,
			"attempts": attempt,
		},
	})

	// Step 12: Cleanup handled by defer

	return &CommitResult{
		SHA:      commitSHA,
		Ref:      ref,
		Parent:   parentSHA,
		Tree:     treeSHA,
		Attempts: attempt,
	}, false, nil
}

// resolveFiles validates and returns absolute paths for all requested files.
func (p *Pipeline) resolveFiles(repoRoot string, files []string, force bool) ([]string, error) {
	abs := make([]string, 0, len(files))
	for _, f := range files {
		var absPath string
		if filepath.IsAbs(f) {
			absPath = filepath.Clean(f)
		} else {
			absPath = filepath.Join(repoRoot, f)
		}

		// Must be inside the repo
		rel, err := filepath.Rel(repoRoot, absPath)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("file %s is outside the repository", f)
		}

		exists := true
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			exists = false
		}

		if !exists {
			// File doesn't exist on disk -- must be a tracked deletion
			tracked, err := git.IsTracked(rel)
			if err != nil {
				return nil, fmt.Errorf("checking tracked status of %s: %w", f, err)
			}
			if !tracked {
				return nil, fmt.Errorf("file %s does not exist and is not tracked by git", f)
			}
		} else if !force {
			// Check gitignore
			ignored, _ := git.IsIgnored(rel)
			if ignored {
				return nil, fmt.Errorf("file %s is gitignored; use --force to override", f)
			}
		}

		abs = append(abs, absPath)
	}
	return abs, nil
}

// stageFile stages a single file into the tmp index.
// Existing files are added; missing-but-tracked files are removed.
func (p *Pipeline) stageFile(indexPath, absPath string) error {
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return git.RmCached(indexPath, absPath)
	}
	return git.AddFile(indexPath, absPath)
}

// parentTreeSHA returns the tree SHA of a commit.
func (p *Pipeline) parentTreeSHA(commitSHA string) (string, error) {
	sha, err := git.RevParse(commitSHA + "^{tree}")
	if err != nil {
		return "", err
	}
	return sha, nil
}
