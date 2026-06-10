package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/submodule"
)

func runScrubFile(flags globalFlags, kwargs map[string]interface{}) int {
	const cmd = "scrub file"

	from := kwargs["from"].(string)
	reason := kwargs["reason"].(string)
	filePath := kwargs["file"].(string)

	// Validation
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		die(flags, cmd, 4, err.Error())
	}

	ctx := context.Background()
	requireCleanTree(ctx, flags, cmd)

	sgDir := repo.SafegitDir(gitDir)

	// Acquire rewrite lock to prevent concurrent scrub operations
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("loading config: %v", err))
	}
	timeout := time.Duration(cfg.Lock.AcquireTimeoutSeconds) * time.Second
	sharedDir := repo.SharedSafegitDir(ctx, gitDir)
	lk, err := lock.Acquire(sharedDir, sgDir, "safegit/rewrite", "scrub-file", timeout)
	if err != nil {
		die(flags, cmd, 1, "another rewrite operation is in progress")
	}
	defer lk.Release()

	// Enumerate submodules to detect if file path targets a submodule.
	subs, subErr := submodule.Enumerate(ctx, gitDir)
	if subErr != nil {
		// Non-fatal: continue without submodule support.
		subs = nil
	}

	// Check if the file path starts with a submodule's relative path.
	var targetSub *submodule.SubmoduleInfo
	var subFilePath string
	for i, sub := range subs {
		if !sub.Initialized {
			continue
		}
		prefix := sub.RelativePath + "/"
		if strings.HasPrefix(filePath, prefix) {
			targetSub = &subs[i]
			subFilePath = filePath[len(prefix):]
			break
		}
	}

	if targetSub != nil {
		return runScrubFileInSubmodule(ctx, flags, cmd, filePath, subFilePath, targetSub, from, reason, gitDir, sgDir)
	}

	// Resolve --from to a full SHA
	fromSHA, err := git.RevParse(ctx, from)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("resolving --from %q: %v", from, err))
	}

	// Ancestry guard: --from must be an ancestor of (or equal to) HEAD
	isAnc, err := git.IsAncestorOf(ctx, fromSHA, "HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("checking ancestry of --from: %v", err))
	}
	if !isAnc {
		die(flags, cmd, 1, fmt.Sprintf("--from commit %s is not an ancestor of HEAD", from))
	}

	// Capture old HEAD before any changes
	oldHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("resolving HEAD: %v", err))
	}

	// Determine replacement blob: if file exists on disk, hash it as the
	// replacement. Otherwise, empty string signals removal mode.
	var newBlobSHA string
	var mode string
	if _, err := os.Stat(filePath); err == nil {
		newBlobSHA, err = git.HashObjectWrite(ctx, filePath)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("hashing file %q: %v", filePath, err))
		}
		mode = "replace"
	} else {
		mode = "remove"
	}

	// Count commits to be rewritten (inclusive of --from)
	countOut, _, err := git.Run(ctx, "rev-list", "--count", fromSHA+"..HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("counting commits: %v", err))
	}
	exclusiveCount, err := strconv.Atoi(strings.TrimSpace(countOut))
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("parsing commit count: %v", err))
	}
	commitCount := exclusiveCount + 1 // inclusive of fromSHA

	// Summary
	fmt.Printf("Scrub summary:\n")
	fmt.Printf("  File:    %s\n", filePath)
	fmt.Printf("  Mode:    %s\n", mode)
	fmt.Printf("  From:    %s\n", fromSHA[:12])
	fmt.Printf("  Commits: %d\n", commitCount)
	fmt.Printf("  Reason:  %s\n", reason)

	// Confirmation prompt (skipped with --yes)
	if !confirmOrAbort(flags, "This will rewrite %d commits. This cannot be undone. Proceed?", commitCount) {
		fmt.Println("Aborted.")
		return 0
	}

	// Dry-run check
	if flags.dryRun {
		fmt.Println("Dry run: no changes made.")
		return 0
	}

	// Commit walker: topo-order, parents before children (inclusive of fromSHA)
	out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", fromSHA+"..HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("listing commits: %v", err))
	}
	shas := append([]string{fromSHA}, splitNonEmpty(out)...)

	// Track old blob SHAs that get replaced, for post-cleanup verification.
	oldBlobSHAs := make(map[string]bool)

	shaMap, rewrittenCount, err := walkAndRewrite(ctx, shas, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string) (CommitTransform, error) {
		// Look up the old blob SHA at the target path before replacing.
		oldBlobSHA := lookupBlobAtPath(ctx, info.Tree, filePath)

		newTreeSHA, err := replaceInTree(ctx, info.Tree, filePath, newBlobSHA)
		if err != nil {
			return CommitTransform{}, fmt.Errorf("replacing in tree for commit %s: %w", sha, err)
		}
		var xform CommitTransform
		if newTreeSHA != info.Tree {
			xform.TreeSHA = newTreeSHA
			// Tree changed, so the old blob was replaced. Track it.
			if oldBlobSHA != "" && oldBlobSHA != newBlobSHA {
				oldBlobSHAs[oldBlobSHA] = true
			}
		}
		return xform, nil
	}, flags.verbose)
	if err != nil {
		die(flags, cmd, 1, err.Error())
	}

	// Update refs: reuse updateRefs from rewrite_author.go with empty author
	// params. When all four author strings are empty, rewriteAnnotatedTag
	// skips tagger matching entirely and only remaps target commit SHAs,
	// which is exactly what scrub needs.
	fmt.Println("Updating refs...")
	if err := updateRefs(ctx, shaMap, "", "", "", "", flags.verbose); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("updating refs: %v", err))
	}

	// Sync main index and working tree to match rewritten HEAD
	if _, err := git.SyncMainIndexWithWorktree(ctx, "HEAD"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to sync main index: %v\n", err)
	}

	// Post-rewrite verification
	fmt.Println("Verifying...")
	verifyFailures, verifyChecks := verifyScrub(ctx, shaMap, filePath, flags.verbose)
	if len(verifyFailures) > 0 {
		fmt.Fprintf(os.Stderr, "Verification warnings (%d failures):\n", len(verifyFailures))
		for _, f := range verifyFailures {
			fmt.Fprintf(os.Stderr, "  WARN: %s\n", f)
		}
	} else {
		fmt.Printf("Verification passed: %d checks across %d rewritten commits\n", verifyChecks, rewrittenCount)
	}

	// Resolve new HEAD
	newHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("resolving new HEAD: %v", err))
	}

	// Determine current ref for oplog
	ref, _ := git.HeadRef(ctx)
	if ref == "" {
		ref = "HEAD (detached)"
	}

	// Oplog entry
	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "scrub-file",
		Extra: map[string]interface{}{
			"ref":       ref,
			"file":      filePath,
			"from":      fromSHA,
			"reason":    reason,
			"oldHead":   oldHeadSHA,
			"sha":       newHeadSHA,
			"rewritten": rewrittenCount,
		},
	})

	// Surgical post-rewrite cleanup: expire tainted reflog entries and prune old objects
	if err := cleanupAfterRewrite(ctx, flags, cmd, shaMap, sgDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: post-rewrite cleanup: %v\n", err)
	}

	// Post-cleanup verification: check that old blob SHAs were pruned
	exitCode := 0
	if len(oldBlobSHAs) > 0 {
		fmt.Println("Verifying old blobs removed...")
		oldBlobList := make([]string, 0, len(oldBlobSHAs))
		for sha := range oldBlobSHAs {
			oldBlobList = append(oldBlobList, sha)
		}
		if err := verifyOldBlobsRemoved(ctx, oldBlobList); err != nil {
			fmt.Fprintf(os.Stderr, "CRITICAL: %v\n", err)
			fmt.Fprintln(os.Stderr, "Old file content may still be present in the local object store.")
			fmt.Fprintln(os.Stderr, "Run 'git reflog expire --expire=now --all && git gc --prune=now' to force cleanup.")
			exitCode = 1
		} else {
			fmt.Println("Verification passed: all old blobs removed from object store.")
		}
	}

	// Summary
	fmt.Printf("\nScrub complete:\n")
	fmt.Printf("  %d commits rewritten\n", rewrittenCount)
	fmt.Printf("  Old HEAD: %s\n", oldHeadSHA[:12])
	fmt.Printf("  New HEAD: %s\n", newHeadSHA[:12])
	fmt.Printf("\nTo update the remote, run: git push --force-with-lease\n")

	return exitCode
}

// runScrubFileInSubmodule handles `scrub file` when the target path lives inside
// a submodule. It scrubs the file within the submodule's git history, collects
// the commit SHA mapping, then rewrites the parent's gitlinks to point to the
// new submodule commits.
func runScrubFileInSubmodule(
	ctx context.Context,
	flags globalFlags,
	cmd string,
	fullPath string, // original path as user provided (e.g., "vendor/sub/secret.env")
	subFilePath string, // path within the submodule (e.g., "secret.env")
	sub *submodule.SubmoduleInfo,
	from string,
	reason string,
	gitDir string,
	sgDir string,
) int {
	fmt.Printf("File %q is inside submodule [%s], scrubbing as %q within submodule.\n",
		fullPath, sub.RelativePath, subFilePath)

	// Ensure safegit is initialized for the submodule.
	if err := repo.EnsureInitialized(sub.GitDir); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("initializing safegit for submodule %s: %v", sub.RelativePath, err))
	}

	// Save parent cwd.
	parentDir, err := os.Getwd()
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("getting cwd: %v", err))
	}

	// Chdir into the submodule.
	if err := os.Chdir(sub.WorkTreePath); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("chdir to submodule %s: %v", sub.RelativePath, err))
	}

	// Resolve --from within the submodule. Since the user's --from likely
	// refers to the parent repo, we use the submodule's entire history.
	// The parent's --from will be used when rewriting parent gitlinks.
	subFromSHA, subFromErr := git.RevParse(ctx, from)
	useEntireSubHistory := subFromErr != nil

	if !useEntireSubHistory {
		// Verify it's an ancestor of submodule HEAD.
		isAnc, err := git.IsAncestorOf(ctx, subFromSHA, "HEAD")
		if err != nil || !isAnc {
			useEntireSubHistory = true
		}
	}

	// Determine replacement blob within the submodule context.
	var newBlobSHA string
	var mode string
	if _, err := os.Stat(subFilePath); err == nil {
		newBlobSHA, err = git.HashObjectWrite(ctx, subFilePath)
		if err != nil {
			os.Chdir(parentDir)
			die(flags, cmd, 1, fmt.Sprintf("hashing file %q in submodule: %v", subFilePath, err))
		}
		mode = "replace"
	} else {
		mode = "remove"
	}

	// Get submodule commit range.
	var subSHAs []string
	if useEntireSubHistory {
		out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", "HEAD")
		if err != nil {
			os.Chdir(parentDir)
			die(flags, cmd, 1, fmt.Sprintf("submodule: listing commits: %v", err))
		}
		subSHAs = splitNonEmpty(out)
	} else {
		out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", subFromSHA+"..HEAD")
		if err != nil {
			os.Chdir(parentDir)
			die(flags, cmd, 1, fmt.Sprintf("submodule: listing commits: %v", err))
		}
		subSHAs = append([]string{subFromSHA}, splitNonEmpty(out)...)
	}

	subCommitCount := len(subSHAs)

	// Summary
	fmt.Printf("Scrub summary:\n")
	fmt.Printf("  File:       %s (in submodule %s)\n", subFilePath, sub.RelativePath)
	fmt.Printf("  Mode:       %s\n", mode)
	fmt.Printf("  Sub commits: %d\n", subCommitCount)
	fmt.Printf("  Reason:     %s\n", reason)

	// Confirmation prompt (skipped with --yes)
	if !confirmOrAbort(flags, "This will rewrite submodule + parent history. This cannot be undone. Proceed?") {
		fmt.Println("Aborted.")
		os.Chdir(parentDir)
		return 0
	}

	if flags.dryRun {
		fmt.Println("Dry run: no changes made.")
		os.Chdir(parentDir)
		return 0
	}

	// Walk and rewrite submodule commits.
	oldSubBlobSHAs := make(map[string]bool)
	subShaMap, subRewrittenCount, err := walkAndRewrite(ctx, subSHAs, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string) (CommitTransform, error) {
		oldBlobSHA := lookupBlobAtPath(ctx, info.Tree, subFilePath)
		newTreeSHA, err := replaceInTree(ctx, info.Tree, subFilePath, newBlobSHA)
		if err != nil {
			return CommitTransform{}, fmt.Errorf("replacing in tree for commit %s: %w", sha, err)
		}
		var xform CommitTransform
		if newTreeSHA != info.Tree {
			xform.TreeSHA = newTreeSHA
			if oldBlobSHA != "" && oldBlobSHA != newBlobSHA {
				oldSubBlobSHAs[oldBlobSHA] = true
			}
		}
		return xform, nil
	}, flags.verbose)
	if err != nil {
		os.Chdir(parentDir)
		die(flags, cmd, 1, fmt.Sprintf("submodule walk and rewrite: %v", err))
	}

	// Update submodule refs.
	fmt.Printf("Updating refs in submodule [%s]...\n", sub.RelativePath)
	if err := updateRefs(ctx, subShaMap, "", "", "", "", flags.verbose); err != nil {
		os.Chdir(parentDir)
		die(flags, cmd, 1, fmt.Sprintf("submodule: updating refs: %v", err))
	}

	// Cleanup submodule.
	if err := cleanupAfterRewrite(ctx, flags, cmd, subShaMap, sub.SafegitDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: submodule post-rewrite cleanup: %v\n", err)
	}

	// Log submodule oplog.
	subRef, _ := git.HeadRef(ctx)
	if subRef == "" {
		subRef = "HEAD (detached)"
	}
	subNewHead, _ := git.RevParse(ctx, "HEAD")
	_ = oplog.Append(sub.SafegitDir, oplog.Entry{
		Op: "scrub-file",
		Extra: map[string]interface{}{
			"ref":       subRef,
			"file":      subFilePath,
			"reason":    reason,
			"sha":       subNewHead,
			"rewritten": subRewrittenCount,
		},
	})

	fmt.Printf("  [%s] %d commits rewritten\n", sub.RelativePath, subRewrittenCount)

	// Chdir back to parent.
	if err := os.Chdir(parentDir); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("chdir back to parent: %v", err))
	}

	// Build gitlink map from submodule SHA mappings.
	gitlinkMap := make(map[string]string)
	for old, new_ := range subShaMap {
		if old != new_ {
			gitlinkMap[old] = new_
		}
	}

	if len(gitlinkMap) == 0 {
		fmt.Println("No submodule commits were rewritten; parent history unchanged.")
		return 0
	}

	// Capture old parent HEAD.
	oldHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("resolving parent HEAD: %v", err))
	}

	// Parent commit range: use entire history since --from is a submodule SHA
	// that doesn't exist in the parent repo.
	out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", "HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("listing parent commits: %v", err))
	}
	parentSHAs := splitNonEmpty(out)

	fmt.Printf("Rewriting %d parent commits (gitlink updates)...\n", len(parentSHAs))

	// Walk parent, only updating gitlinks (no blob changes, no message changes).
	parentShaMap, parentRewrittenCount, err := walkAndRewrite(ctx, parentSHAs, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string) (CommitTransform, error) {
		newTreeSHA, err := replaceInTreeByBlobMap(ctx, info.Tree, nil, gitlinkMap)
		if err != nil {
			return CommitTransform{}, fmt.Errorf("updating gitlinks in tree for commit %s: %w", sha, err)
		}
		var xform CommitTransform
		if newTreeSHA != info.Tree {
			xform.TreeSHA = newTreeSHA
		}
		return xform, nil
	}, flags.verbose)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("parent walk and rewrite: %v", err))
	}

	// Update parent refs.
	fmt.Println("Updating parent refs...")
	if err := updateRefs(ctx, parentShaMap, "", "", "", "", flags.verbose); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("updating parent refs: %v", err))
	}

	// Sync parent index and working tree to match rewritten HEAD.
	if _, err := git.SyncMainIndexWithWorktree(ctx, "HEAD"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to sync parent main index: %v\n", err)
	}

	// Cleanup parent.
	if err := cleanupAfterRewrite(ctx, flags, cmd, parentShaMap, sgDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: parent post-rewrite cleanup: %v\n", err)
	}

	// Resolve new parent HEAD.
	newHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("resolving new parent HEAD: %v", err))
	}

	// Parent oplog entry.
	ref, _ := git.HeadRef(ctx)
	if ref == "" {
		ref = "HEAD (detached)"
	}
	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "scrub-file",
		Extra: map[string]interface{}{
			"ref":       ref,
			"file":      fullPath,
			"from":      from,
			"reason":    reason,
			"oldHead":   oldHeadSHA,
			"sha":       newHeadSHA,
			"rewritten": parentRewrittenCount,
			"submodule": sub.RelativePath,
		},
	})

	// Verification: check old blobs are not reachable from submodule refs.
	exitCode := 0
	if len(oldSubBlobSHAs) > 0 {
		os.Chdir(sub.WorkTreePath)
		fmt.Println("Verifying old blobs unreachable in submodule...")
		oldBlobList := make([]string, 0, len(oldSubBlobSHAs))
		for sha := range oldSubBlobSHAs {
			oldBlobList = append(oldBlobList, sha)
		}
		reachableBlobs, err := buildReachableBlobSet(ctx)
		if err == nil {
			for _, sha := range oldBlobList {
				if reachableBlobs[sha] {
					fmt.Fprintf(os.Stderr, "CRITICAL: old blob %s still reachable in submodule\n", shortSHA(sha))
					exitCode = 1
				}
			}
			if exitCode == 0 {
				fmt.Println("Verification passed: old blobs unreachable in submodule.")
			}
		}
		os.Chdir(parentDir)
	}

	// Summary.
	fmt.Printf("\nScrub complete:\n")
	fmt.Printf("  %d submodule commits rewritten\n", subRewrittenCount)
	fmt.Printf("  %d parent commits rewritten (gitlink updates)\n", parentRewrittenCount)
	fmt.Printf("  Old HEAD: %s\n", oldHeadSHA[:12])
	fmt.Printf("  New HEAD: %s\n", newHeadSHA[:12])
	fmt.Printf("\nTo update the remote, run: git push --force-with-lease\n")

	return exitCode
}

// runScrubMatch is in scrub_match.go
