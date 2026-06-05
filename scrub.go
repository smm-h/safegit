package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
)

func runScrub(flags globalFlags, kwargs map[string]interface{}) int {
	const cmd = "scrub"

	from := kwargs["from"].(string)
	reason := kwargs["reason"].(string)
	filePath := kwargs["file"].(string)

	// Validation
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		die(flags, cmd, 4, err.Error())
	}

	sgDir := repo.SafegitDir(gitDir)
	ctx := context.Background()

	// Resolve --from to a full SHA
	fromSHA, err := git.RevParse(ctx, from)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("resolving --from %q: %v", from, err))
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

	// Count commits to be rewritten
	countOut, _, err := git.Run(ctx, "rev-list", "--count", fromSHA+"..HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("counting commits: %v", err))
	}
	commitCount, err := strconv.Atoi(strings.TrimSpace(countOut))
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("parsing commit count: %v", err))
	}

	if commitCount == 0 {
		fmt.Println("No commits to rewrite (--from is at HEAD or ahead of it).")
		return 0
	}

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

	// Commit walker: topo-order, parents before children
	out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", fromSHA+"..HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("listing commits: %v", err))
	}
	shas := splitNonEmpty(out)

	shaMap := make(map[string]string, len(shas))
	rewrittenCount := 0

	for _, sha := range shas {
		info, err := git.ParseCommit(ctx, sha)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("parsing commit %s: %v", sha, err))
		}

		// Remap parents through earlier rewrites
		remappedParents := make([]string, len(info.Parents))
		parentRemapped := false
		for i, p := range info.Parents {
			if mapped, ok := shaMap[p]; ok && mapped != p {
				remappedParents[i] = mapped
				parentRemapped = true
			} else {
				remappedParents[i] = p
			}
		}

		// Replace or remove the file in this commit's tree
		newTreeSHA, err := replaceInTree(ctx, info.Tree, filePath, newBlobSHA)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("replacing in tree for commit %s: %v", sha, err))
		}
		treeChanged := newTreeSHA != info.Tree

		// Commit needs rewriting if tree changed or any parent was remapped
		if treeChanged || parentRemapped {
			newSHA, err := git.CommitTreeWithAuthor(ctx, newTreeSHA, remappedParents, info.Message, info.Author, info.Committer)
			if err != nil {
				die(flags, cmd, 1, fmt.Sprintf("creating rewritten commit for %s: %v", sha, err))
			}
			shaMap[sha] = newSHA
			rewrittenCount++
			if flags.verbose {
				if treeChanged {
					fmt.Fprintf(os.Stderr, "  %s -> %s  (tree changed)\n", sha[:12], newSHA[:12])
				} else {
					fmt.Fprintf(os.Stderr, "  %s -> %s  (inherited)\n", sha[:12], newSHA[:12])
				}
			}
		} else {
			shaMap[sha] = sha
			if flags.verbose {
				fmt.Fprintf(os.Stderr, "  %s                   (unchanged)\n", sha[:12])
			}
		}
	}

	// Update refs: reuse updateRefs from rewrite_author.go with empty author
	// params. When all four author strings are empty, rewriteAnnotatedTag
	// skips tagger matching entirely and only remaps target commit SHAs,
	// which is exactly what scrub needs.
	fmt.Println("Updating refs...")
	if err := updateRefs(ctx, shaMap, "", "", "", "", flags.verbose); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("updating refs: %v", err))
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
		Op: "scrub",
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

	// Summary
	fmt.Printf("\nScrub complete:\n")
	fmt.Printf("  %d commits rewritten\n", rewrittenCount)
	fmt.Printf("  Old HEAD: %s\n", oldHeadSHA[:12])
	fmt.Printf("  New HEAD: %s\n", newHeadSHA[:12])
	fmt.Printf("\nTo update the remote, run: git push --force-with-lease\n")

	return 0
}
