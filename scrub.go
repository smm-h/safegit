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

	// Confirmation prompt (skipped with --force)
	if !flags.force {
		fmt.Printf("\nThis will rewrite %d commits. This cannot be undone. Proceed? [y/N] ", commitCount)
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("Aborted.")
			return 0
		}
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

	// Sync main index so git status/diff reflect the rewritten HEAD
	if err := git.SyncMainIndex(ctx, "HEAD"); err != nil {
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

	// Summary
	fmt.Printf("\nScrub complete:\n")
	fmt.Printf("  %d commits rewritten\n", rewrittenCount)
	fmt.Printf("  Old HEAD: %s\n", oldHeadSHA[:12])
	fmt.Printf("  New HEAD: %s\n", newHeadSHA[:12])
	fmt.Printf("\nTo update the remote, run: git push --force-with-lease\n")

	return 0
}

// runScrubMatch is in scrub_match.go
