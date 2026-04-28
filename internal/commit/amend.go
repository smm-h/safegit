// Amend and Reword implement tip-commit rewriting with CAS safety.
package commit

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/index"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/stage"
)

// AmendRequest holds inputs for an amend operation.
type AmendRequest struct {
	Message   string     // empty = keep existing message
	FileSpecs []FileSpec // files to stage into the amended commit
	Force     bool       // skip gitignore check
	DryRun    bool
}

// AmendResult is the JSON-serializable output of a successful amend.
type AmendResult struct {
	SHA      string `json:"sha"`
	Ref      string `json:"ref"`
	Parent   string `json:"parent"`
	Tree     string `json:"tree"`
	OldSHA   string `json:"oldSha"`
	Attempts int    `json:"attempts"`
}

// Amend rewrites the tip of the current branch with new files staged.
// Uses tmp index seeded from HEAD, stages files, builds a new commit with
// parent = HEAD^ and lock-and-CAS updates the ref.
func (p *Pipeline) Amend(ctx context.Context, req AmendRequest) (*AmendResult, error) {
	repoRoot, err := git.RepoRoot()
	if err != nil {
		return nil, fmt.Errorf("resolving repo root: %w", err)
	}

	ref, err := git.HeadRef()
	if err != nil {
		return nil, fmt.Errorf("resolving HEAD: %w", err)
	}

	if len(req.FileSpecs) == 0 {
		return nil, fmt.Errorf("no files specified for amend")
	}

	// Extract paths for validation
	filePaths := make([]string, len(req.FileSpecs))
	for i, fs := range req.FileSpecs {
		filePaths[i] = fs.Path
	}

	absFiles, err := p.resolveFiles(repoRoot, filePaths, req.Force)
	if err != nil {
		return nil, err
	}

	maxAttempts := p.Config.Commit.CASMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, retry, err := p.tryAmend(ctx, ref, repoRoot, absFiles, req, attempt)
		if err != nil {
			return nil, err
		}
		if !retry {
			return result, nil
		}
	}

	return nil, &CommitError{
		Code:    ExitCASExhausted,
		Message: fmt.Sprintf("CAS convergence failure after %d attempts on %s", maxAttempts, ref),
	}
}

func (p *Pipeline) tryAmend(
	ctx context.Context,
	ref, repoRoot string,
	absFiles []string,
	req AmendRequest,
	attempt int,
) (*AmendResult, bool, error) {

	// Snapshot current HEAD SHA (the commit we're replacing)
	headSHA, err := git.RevParse("HEAD")
	if err != nil {
		return nil, false, fmt.Errorf("resolving HEAD: %w", err)
	}

	// Get parent of HEAD (HEAD^)
	parentSHA, err := git.RevParse("HEAD^")
	if err != nil {
		return nil, false, fmt.Errorf("resolving HEAD^: %w", err)
	}

	// Determine message: use provided or reuse existing
	message := req.Message
	if message == "" {
		msg, err := git.CommitMessage("HEAD")
		if err != nil {
			return nil, false, fmt.Errorf("reading HEAD commit message: %w", err)
		}
		message = msg
	}

	// --- Phase A: create tmp index from HEAD and stage new files ---
	tmpIdx, err := index.New(p.SafegitDir)
	if err != nil {
		return nil, false, fmt.Errorf("creating tmp index: %w", err)
	}
	defer tmpIdx.Cleanup()

	for i, absPath := range absFiles {
		hunks := req.FileSpecs[i].Hunks
		if hunks != nil {
			if err := stage.StageHunks(tmpIdx.IndexPath, absPath, hunks); err != nil {
				return nil, false, fmt.Errorf("staging hunks of %s: %w", absPath, err)
			}
		} else {
			if err := p.stageFile(tmpIdx.IndexPath, absPath); err != nil {
				return nil, false, fmt.Errorf("staging %s: %w", absPath, err)
			}
		}
	}

	// Build new tree
	treeSHA, err := git.WriteTree(tmpIdx.IndexPath)
	if err != nil {
		return nil, false, &CommitError{Code: ExitWriteTree, Message: fmt.Sprintf("write-tree failed: %v", err)}
	}

	// Create new commit with parent = HEAD^ (replacing HEAD)
	commitSHA, err := git.CommitTree(treeSHA, parentSHA, message)
	if err != nil {
		return nil, false, &CommitError{Code: ExitCommitTree, Message: fmt.Sprintf("commit-tree failed: %v", err)}
	}

	if req.DryRun {
		return &AmendResult{
			SHA:      commitSHA,
			Ref:      ref,
			Parent:   parentSHA,
			Tree:     treeSHA,
			OldSHA:   headSHA,
			Attempts: attempt,
		}, false, nil
	}

	// --- Phase B: lock and CAS update ---
	lockTimeout := time.Duration(p.Config.Lock.AcquireTimeoutSeconds) * time.Second
	if lockTimeout <= 0 {
		lockTimeout = 30 * time.Second
	}
	refLock, err := lock.Acquire(p.SafegitDir, ref, "amend", lockTimeout)
	if err != nil {
		return nil, false, fmt.Errorf("acquiring lock on %s: %w", ref, err)
	}
	defer refLock.Release()

	// CAS: ref must still point at headSHA (the commit we're replacing)
	currentTip, err := git.RevParse(ref)
	if err != nil {
		return nil, false, fmt.Errorf("re-resolving %s for CAS: %w", ref, err)
	}
	if currentTip != headSHA {
		return nil, true, nil // CAS miss, retry
	}

	// Update ref: old = headSHA, new = commitSHA
	if err := git.UpdateRef(ref, commitSHA, headSHA); err != nil {
		return nil, false, fmt.Errorf("update-ref CAS failed: %w", err)
	}

	// Sync main index
	if err := git.SyncMainIndex("HEAD"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to sync main index: %v\n", err)
	}

	// Oplog
	_ = oplog.Append(p.SafegitDir, oplog.Entry{
		Op: "amend",
		Extra: map[string]interface{}{
			"ref":      ref,
			"tree":     treeSHA,
			"parent":   parentSHA,
			"sha":      commitSHA,
			"oldSha":   headSHA,
			"attempts": attempt,
		},
	})

	return &AmendResult{
		SHA:      commitSHA,
		Ref:      ref,
		Parent:   parentSHA,
		Tree:     treeSHA,
		OldSHA:   headSHA,
		Attempts: attempt,
	}, false, nil
}

// RewordRequest holds inputs for a reword operation.
type RewordRequest struct {
	Message string // required
	DryRun  bool
}

// RewordResult is the JSON-serializable output of a successful reword.
type RewordResult struct {
	SHA    string `json:"sha"`
	Ref    string `json:"ref"`
	Parent string `json:"parent"`
	Tree   string `json:"tree"`
	OldSHA string `json:"oldSha"`
}

// Reword rewrites only the commit message of the tip of the current branch.
// Tree and parent remain unchanged.
func (p *Pipeline) Reword(ctx context.Context, req RewordRequest) (*RewordResult, error) {
	if req.Message == "" {
		return nil, fmt.Errorf("reword requires a message (-m)")
	}

	ref, err := git.HeadRef()
	if err != nil {
		return nil, fmt.Errorf("resolving HEAD: %w", err)
	}

	headSHA, err := git.RevParse("HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolving HEAD: %w", err)
	}

	// Get tree and parent from current HEAD
	treeSHA, err := git.RevParse("HEAD^{tree}")
	if err != nil {
		return nil, fmt.Errorf("resolving HEAD tree: %w", err)
	}

	parentSHA, err := git.RevParse("HEAD^")
	if err != nil {
		// Could be a root commit (no parent)
		parentSHA = ""
	}

	// Create new commit with same tree and parent, new message
	commitSHA, err := git.CommitTree(treeSHA, parentSHA, req.Message)
	if err != nil {
		return nil, &CommitError{Code: ExitCommitTree, Message: fmt.Sprintf("commit-tree failed: %v", err)}
	}

	if req.DryRun {
		return &RewordResult{
			SHA:    commitSHA,
			Ref:    ref,
			Parent: parentSHA,
			Tree:   treeSHA,
			OldSHA: headSHA,
		}, nil
	}

	// Lock and CAS update
	lockTimeout := time.Duration(p.Config.Lock.AcquireTimeoutSeconds) * time.Second
	if lockTimeout <= 0 {
		lockTimeout = 30 * time.Second
	}
	refLock, err := lock.Acquire(p.SafegitDir, ref, "reword", lockTimeout)
	if err != nil {
		return nil, fmt.Errorf("acquiring lock on %s: %w", ref, err)
	}
	defer refLock.Release()

	// CAS check
	currentTip, err := git.RevParse(ref)
	if err != nil {
		return nil, fmt.Errorf("re-resolving %s for CAS: %w", ref, err)
	}
	if currentTip != headSHA {
		return nil, fmt.Errorf("branch tip moved (expected %s, got %s); aborting reword", headSHA[:8], currentTip[:8])
	}

	if err := git.UpdateRef(ref, commitSHA, headSHA); err != nil {
		return nil, fmt.Errorf("update-ref CAS failed: %w", err)
	}

	// Sync main index
	if err := git.SyncMainIndex("HEAD"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to sync main index: %v\n", err)
	}

	// Oplog
	_ = oplog.Append(p.SafegitDir, oplog.Entry{
		Op: "reword",
		Extra: map[string]interface{}{
			"ref":    ref,
			"sha":    commitSHA,
			"oldSha": headSHA,
		},
	})

	return &RewordResult{
		SHA:    commitSHA,
		Ref:    ref,
		Parent: parentSHA,
		Tree:   treeSHA,
		OldSHA: headSHA,
	}, nil
}

