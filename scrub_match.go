package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/scan"
)

func runScrubMatch(flags globalFlags, kwargs map[string]interface{}) int {
	const cmd = "scrub match"

	// Flag extraction
	pattern := kwargs["pattern"].(string)
	replace := kwargs["replace"].(string)
	reason := kwargs["reason"].(string)

	var from *string
	if v := kwargs["from"]; v != nil {
		s := v.(string)
		from = &s
	}
	entireHistory := kwargs["entire_history"].(bool)

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
	lk, err := lock.Acquire(sharedDir, sgDir, "safegit/rewrite", "scrub-match", timeout)
	if err != nil {
		die(flags, cmd, 1, "another rewrite operation is in progress")
	}
	defer lk.Release()

	// Resolve --from if provided
	var fromSHA string
	if from != nil {
		fromSHA, err = git.RevParse(ctx, *from)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("resolving --from %q: %v", *from, err))
		}
		isAnc, err := git.IsAncestorOf(ctx, fromSHA, "HEAD")
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("checking ancestry of --from: %v", err))
		}
		if !isAnc {
			die(flags, cmd, 1, fmt.Sprintf("--from commit %s is not an ancestor of HEAD", *from))
		}
	}

	// Neither --from nor --entire-history provided
	if from == nil && !entireHistory {
		die(flags, cmd, 2, "one of --from or --entire-history is required")
	}

	// Compile regex
	compiledPattern, err := regexp.Compile(pattern)
	if err != nil {
		die(flags, cmd, 2, fmt.Sprintf("invalid regex pattern: %v", err))
	}

	// Dry-run mode
	if flags.dryRun {
		return scrubMatchDryRun(ctx, flags, cmd, compiledPattern, gitDir)
	}

	// Execution mode
	return scrubMatchExecute(ctx, flags, cmd, compiledPattern, replace, reason, fromSHA, entireHistory, gitDir, sgDir)
}

// scrubMatchDryRun scans all objects and non-object files, prints categorized
// output, and returns 0.
func scrubMatchDryRun(ctx context.Context, flags globalFlags, cmd string, compiledPattern *regexp.Regexp, gitDir string) int {
	fmt.Println("Scanning all objects...")
	results, err := scan.ScanObjects(ctx, compiledPattern)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("scanning objects: %v", err))
	}

	if err := scan.AddAttribution(ctx, results); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("adding attribution: %v", err))
	}

	nonObjectMatches, err := scan.ScanNonObjects(ctx, compiledPattern, gitDir)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("scanning non-object files: %v", err))
	}

	// Categorize matches
	var blobMatches, commitMatches, tagMatches []scan.Match
	for _, m := range results.Matches {
		switch m.ObjectType {
		case "blob":
			blobMatches = append(blobMatches, m)
		case "commit":
			commitMatches = append(commitMatches, m)
		case "tag":
			tagMatches = append(tagMatches, m)
		}
	}

	totalMatches := len(results.Matches) + len(nonObjectMatches)

	// Count unique objects
	uniqueObjects := make(map[string]bool)
	for _, m := range results.Matches {
		uniqueObjects[m.SHA] = true
	}

	fmt.Printf("Found %d matches in %d objects:\n", totalMatches, len(uniqueObjects)+len(nonObjectMatches))

	if len(blobMatches) > 0 {
		fmt.Printf("\nBlobs:\n")
		for _, m := range blobMatches {
			if m.Path != "" && m.CommitSHA != "" {
				fmt.Printf("  %s in commit %s (line %d): %s\n", m.Path, shortSHA(m.CommitSHA), m.Line, m.Context)
			} else if m.Path != "" {
				fmt.Printf("  %s (unreachable, line %d): %s\n", m.Path, m.Line, m.Context)
			} else if m.Reachable {
				fmt.Printf("  blob %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
			} else {
				fmt.Printf("  blob %s (unreachable, line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
			}
		}
	}

	if len(commitMatches) > 0 {
		fmt.Printf("\nCommit messages:\n")
		for _, m := range commitMatches {
			fmt.Printf("  commit %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
		}
	}

	if len(tagMatches) > 0 {
		fmt.Printf("\nTag annotations:\n")
		for _, m := range tagMatches {
			fmt.Printf("  tag %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
		}
	}

	if len(nonObjectMatches) > 0 {
		fmt.Printf("\nNon-object files:\n")
		for _, m := range nonObjectMatches {
			fmt.Printf("  %s (line %d): %s\n", m.Path, m.Line, m.Context)
		}
	}

	fmt.Printf("\nSummary: %d blob matches, %d message matches, %d tag matches, %d file matches\n",
		len(blobMatches), len(commitMatches), len(tagMatches), len(nonObjectMatches))
	if results.Skipped > 0 {
		fmt.Printf("Binary blobs skipped: %d\n", results.Skipped)
	}

	return 0
}

// scrubMatchExecute performs the actual rewrite: builds blob replacement map,
// walks and rewrites commits, updates refs, rewrites tag annotations, syncs
// the index, and logs to the oplog.
func scrubMatchExecute(
	ctx context.Context,
	flags globalFlags,
	cmd string,
	compiledPattern *regexp.Regexp,
	replace string,
	reason string,
	fromSHA string,
	entireHistory bool,
	gitDir string,
	sgDir string,
) int {
	// Scan to find all matches
	fmt.Println("Scanning objects...")
	results, err := scan.ScanObjects(ctx, compiledPattern)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("scanning objects: %v", err))
	}

	if len(results.Matches) == 0 {
		fmt.Println("No matches found. Nothing to rewrite.")
		return 0
	}

	// Count matches by type for summary
	var blobMatchCount, commitMatchCount, tagMatchCount int
	uniqueBlobSHAs := make(map[string]bool)
	for _, m := range results.Matches {
		switch m.ObjectType {
		case "blob":
			blobMatchCount++
			uniqueBlobSHAs[m.SHA] = true
		case "commit":
			commitMatchCount++
		case "tag":
			tagMatchCount++
		}
	}

	fmt.Printf("Found %d matches (%d in blobs, %d in commit messages, %d in tag annotations)\n",
		len(results.Matches), blobMatchCount, commitMatchCount, tagMatchCount)

	// Confirmation prompt
	if !flags.force {
		fmt.Printf("This will rewrite history to replace pattern matches. This cannot be undone. Proceed? [y/N] ")
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("Aborted.")
			return 0
		}
	}

	// Capture old HEAD
	oldHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("resolving HEAD: %v", err))
	}

	// Build blob replacement map
	fmt.Println("Building blob replacement map...")
	blobMap := make(map[string]string, len(uniqueBlobSHAs))
	for blobSHA := range uniqueBlobSHAs {
		content, err := git.CatFileBlob(ctx, blobSHA)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("reading blob %s: %v", blobSHA, err))
		}
		// Skip binary blobs (should already be skipped by scanner, but double-check)
		if isBinaryContent(content) {
			continue
		}
		modified := compiledPattern.ReplaceAll(content, []byte(replace))
		if bytes.Equal(content, modified) {
			// Pattern matched during scan (possibly on specific lines) but
			// ReplaceAll found nothing -- skip.
			continue
		}
		newSHA, err := git.HashObjectWriteBytes(ctx, modified)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("writing replaced blob: %v", err))
		}
		blobMap[blobSHA] = newSHA
		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  blob %s -> %s\n", shortSHA(blobSHA), shortSHA(newSHA))
		}
	}

	// Determine commit range
	var shas []string
	if entireHistory {
		out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", "HEAD")
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("listing commits: %v", err))
		}
		shas = splitNonEmpty(out)
	} else {
		// --from: inclusive of fromSHA
		out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", fromSHA+"..HEAD")
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("listing commits: %v", err))
		}
		shas = append([]string{fromSHA}, splitNonEmpty(out)...)
	}

	commitCount := len(shas)
	fmt.Printf("Rewriting %d commits...\n", commitCount)

	// Track how many commit messages were modified
	messagesModified := 0
	replaceBytes := []byte(replace)

	shaMap, rewrittenCount, err := walkAndRewrite(ctx, shas, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string) (CommitTransform, error) {
		var xform CommitTransform

		// Replace blobs in tree
		newTreeSHA, err := replaceInTreeByBlobMap(ctx, info.Tree, blobMap)
		if err != nil {
			return CommitTransform{}, fmt.Errorf("replacing blobs in tree for commit %s: %w", sha, err)
		}
		if newTreeSHA != info.Tree {
			xform.TreeSHA = newTreeSHA
		}

		// Replace pattern in commit message
		if compiledPattern.Match([]byte(info.Message)) {
			newMessage := compiledPattern.ReplaceAllString(info.Message, replace)
			if newMessage != info.Message {
				xform.Message = newMessage
				messagesModified++
			}
		}

		return xform, nil
	}, flags.verbose)
	if err != nil {
		die(flags, cmd, 1, err.Error())
	}

	// Update refs
	fmt.Println("Updating refs...")
	if err := updateRefs(ctx, shaMap, "", "", "", "", flags.verbose); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("updating refs: %v", err))
	}

	// Tag annotation rewriting (second pass for annotation text)
	tagsRewritten := rewriteTagAnnotations(ctx, flags, cmd, compiledPattern, replaceBytes, shaMap)

	// Sync main index
	if err := git.SyncMainIndex(ctx, "HEAD"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to sync main index: %v\n", err)
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
		Op: "scrub-match",
		Extra: map[string]interface{}{
			"ref":              ref,
			"pattern":          compiledPattern.String(),
			"replace":          replace,
			"reason":           reason,
			"oldHead":          oldHeadSHA,
			"sha":              newHeadSHA,
			"rewritten":        rewrittenCount,
			"blobsReplaced":    len(blobMap),
			"messagesModified": messagesModified,
			"tagsRewritten":    tagsRewritten,
		},
	})

	// Summary
	fmt.Printf("\nScrub complete:\n")
	fmt.Printf("  %d commits rewritten\n", rewrittenCount)
	fmt.Printf("  %d blobs replaced\n", len(blobMap))
	fmt.Printf("  %d commit messages modified\n", messagesModified)
	fmt.Printf("  %d tag annotations rewritten\n", tagsRewritten)
	fmt.Printf("  Old HEAD: %s\n", oldHeadSHA[:12])
	fmt.Printf("  New HEAD: %s\n", newHeadSHA[:12])
	fmt.Printf("\nTo update the remote, run: git push --force-with-lease\n")

	return 0
}

// rewriteTagAnnotations does a second pass over annotated tags after updateRefs,
// checking each tag's annotation body for the pattern and rewriting if matched.
// Returns the count of tags whose annotations were rewritten.
func rewriteTagAnnotations(ctx context.Context, flags globalFlags, cmd string, compiledPattern *regexp.Regexp, replaceBytes []byte, shaMap map[string]string) int {
	out, _, err := git.Run(ctx, "for-each-ref", "--format=%(refname) %(objecttype) %(objectname)", "refs/tags/")
	if err != nil {
		if flags.verbose {
			fmt.Fprintf(os.Stderr, "warning: failed to list tags for annotation rewriting: %v\n", err)
		}
		return 0
	}

	tagsRewritten := 0
	lines := splitNonEmpty(out)
	for _, line := range lines {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			continue
		}
		refname, objecttype, objectname := parts[0], parts[1], parts[2]

		if objecttype != "tag" {
			continue
		}

		// Read the tag object
		tagContent, _, err := git.Run(ctx, "cat-file", "-p", objectname)
		if err != nil {
			if flags.verbose {
				fmt.Fprintf(os.Stderr, "warning: failed to read tag object %s: %v\n", objectname, err)
			}
			continue
		}

		// Split into header and body
		headerEnd := strings.Index(tagContent, "\n\n")
		if headerEnd < 0 {
			// No body -- nothing to search
			continue
		}
		header := tagContent[:headerEnd]
		body := tagContent[headerEnd+2:]

		// Check if body matches the pattern
		if !compiledPattern.MatchString(body) {
			continue
		}

		newBody := compiledPattern.ReplaceAllString(body, string(replaceBytes))
		if newBody == body {
			continue
		}

		// Reconstruct tag object and write it
		newContent := header + "\n\n" + newBody
		newTagSHA, _, err := git.RunWithEnvStdin(ctx, nil, []byte(newContent), "hash-object", "-t", "tag", "-w", "--stdin")
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("writing rewritten tag annotation for %s: %v", refname, err))
		}
		newTagSHA = strings.TrimSpace(newTagSHA)

		// Update the tag ref
		if err := git.UpdateRef(ctx, refname, newTagSHA, objectname); err != nil {
			die(flags, cmd, 1, fmt.Sprintf("updating tag ref %s after annotation rewrite: %v", refname, err))
		}

		tagsRewritten++
		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  tag annotation %s: %s -> %s\n", refname, shortSHA(objectname), shortSHA(newTagSHA))
		}
	}

	return tagsRewritten
}

// shortSHA returns the first 8 characters of a SHA.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// isBinaryContent checks if content is binary (NUL byte in first 8KB).
func isBinaryContent(content []byte) bool {
	limit := len(content)
	if limit > 8192 {
		limit = 8192
	}
	return bytes.ContainsRune(content[:limit], 0)
}

