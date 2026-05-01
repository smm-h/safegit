// Amend and Reword implement tip-commit rewriting with CAS safety.
package commit

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/index"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/stage"
	"github.com/smm-h/safegit/internal/wip"
)

// AmendRequest holds inputs for an amend operation.
type AmendRequest struct {
	Message   string     // empty = keep existing message
	FileSpecs []FileSpec // files to stage into the amended commit
	Branch    string     // target branch ref; empty = HEAD
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

	// Check for wip-locked files (same guard as Execute)
	var lockedMsgs []string
	for _, fp := range filePaths {
		locked, wipID, lErr := wip.IsLocked(p.SafegitDir, fp)
		if lErr != nil {
			continue
		}
		if locked {
			lockedMsgs = append(lockedMsgs, fmt.Sprintf("%s (wip %s)", fp, wipID))
		}
	}
	if len(lockedMsgs) > 0 {
		return nil, &CommitError{
			Code:    ExitWipLocked,
			Message: fmt.Sprintf("refusing to amend wip-locked files: %s", strings.Join(lockedMsgs, ", ")),
		}
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

	// Snapshot current tip SHA (the commit we're replacing).
	// Use ref (not "HEAD") so cross-branch amend resolves the correct tip.
	headSHA, err := git.RevParse(ref)
	if err != nil {
		return nil, false, fmt.Errorf("resolving %s: %w", ref, err)
	}

	// Get parent of tip (ref^). Root commits cannot be amended.
	parentSHA, err := git.RevParse(ref + "^")
	if err != nil {
		return nil, false, fmt.Errorf("cannot amend: %s is a root commit (no parent)", ref)
	}

	// Determine message: use provided or reuse existing
	message := req.Message
	if message == "" {
		msg, err := git.CommitMessage(ref)
		if err != nil {
			return nil, false, fmt.Errorf("reading %s commit message: %w", ref, err)
		}
		message = msg
	}

	// --- Phase A: create tmp index from the resolved tip and stage new files ---
	// Use headSHA (resolved above) instead of ref to avoid a TOCTOU race:
	// if the ref moves between RevParse and index creation, the tree would be
	// based on a different commit than headSHA, silently dropping files.
	tmpIdx, err := index.New(p.SafegitDir, headSHA)
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
	refLock, err := lock.Acquire(repo.SharedSafegitDir(p.SafegitDir), p.SafegitDir, ref, "amend", lockTimeout)
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
		if isTransientRefError(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("update-ref CAS failed: %w", err)
	}

	// Sync main index only when amending the current branch
	if headRef, herr := git.HeadRef(); herr == nil && headRef == ref {
		if err := git.SyncMainIndex("HEAD"); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to sync main index: %v\n", err)
		}
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
	Branch  string // target branch ref; empty = HEAD
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
// Tree and parent remain unchanged. Retries on CAS miss.
func (p *Pipeline) Reword(ctx context.Context, req RewordRequest) (*RewordResult, error) {
	if req.Message == "" {
		return nil, fmt.Errorf("reword requires a message (-m)")
	}

	// Resolve target branch ref
	ref := req.Branch
	if ref == "" {
		var err error
		ref, err = git.HeadRef()
		if err != nil {
			return nil, fmt.Errorf("resolving HEAD: %w", err)
		}
	}
	if !strings.HasPrefix(ref, "refs/") {
		ref = "refs/heads/" + ref
	}

	maxAttempts := p.Config.Commit.CASMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, retry, err := p.tryReword(ctx, ref, req, attempt)
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

func (p *Pipeline) tryReword(
	ctx context.Context,
	ref string,
	req RewordRequest,
	attempt int,
) (*RewordResult, bool, error) {

	headSHA, err := git.RevParse(ref)
	if err != nil {
		return nil, false, fmt.Errorf("resolving %s: %w", ref, err)
	}

	treeSHA, err := git.RevParse(ref + "^{tree}")
	if err != nil {
		return nil, false, fmt.Errorf("resolving %s tree: %w", ref, err)
	}

	parentSHA, err := git.RevParse(ref + "^")
	if err != nil {
		parentSHA = ""
	}

	commitSHA, err := git.CommitTree(treeSHA, parentSHA, req.Message)
	if err != nil {
		return nil, false, &CommitError{Code: ExitCommitTree, Message: fmt.Sprintf("commit-tree failed: %v", err)}
	}

	if req.DryRun {
		return &RewordResult{
			SHA:    commitSHA,
			Ref:    ref,
			Parent: parentSHA,
			Tree:   treeSHA,
			OldSHA: headSHA,
		}, false, nil
	}

	lockTimeout := time.Duration(p.Config.Lock.AcquireTimeoutSeconds) * time.Second
	if lockTimeout <= 0 {
		lockTimeout = 30 * time.Second
	}
	refLock, err := lock.Acquire(repo.SharedSafegitDir(p.SafegitDir), p.SafegitDir, ref, "reword", lockTimeout)
	if err != nil {
		return nil, false, fmt.Errorf("acquiring lock on %s: %w", ref, err)
	}
	defer refLock.Release()

	currentTip, err := git.RevParse(ref)
	if err != nil {
		return nil, false, fmt.Errorf("re-resolving %s for CAS: %w", ref, err)
	}
	if currentTip != headSHA {
		return nil, true, nil
	}

	if err := git.UpdateRef(ref, commitSHA, headSHA); err != nil {
		if isTransientRefError(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("update-ref CAS failed: %w", err)
	}

	if headRef, herr := git.HeadRef(); herr == nil && headRef == ref {
		if err := git.SyncMainIndex("HEAD"); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to sync main index: %v\n", err)
		}
	}

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
	}, false, nil
}

