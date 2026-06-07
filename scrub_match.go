package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/scan"
	"github.com/smm-h/safegit/internal/submodule"
)

func runScrubMatch(flags globalFlags, kwargs map[string]interface{}) int {
	const cmd = "scrub match"

	// Flag extraction
	pattern := kwargs["pattern"].(string)
	replace := kwargs["replace"].(string)
	reason := kwargs["reason"].(string)

	var scope *string
	if v := kwargs["scope"]; v != nil {
		s := v.(string)
		scope = &s
		// Validate the glob pattern at parse time.
		if _, err := path.Match(s, ""); err != nil {
			die(flags, cmd, 2, fmt.Sprintf("invalid --scope glob: %v", err))
		}
	}

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
		return scrubMatchDryRun(ctx, flags, cmd, compiledPattern, scope, gitDir)
	}

	// Execution mode
	return scrubMatchExecute(ctx, flags, cmd, compiledPattern, replace, reason, fromSHA, entireHistory, scope, gitDir, sgDir)
}

// scrubMatchDryRun scans all objects and non-object files, prints categorized
// output, and returns 0. When scope is non-nil, only blob matches whose path
// matches the glob are shown.
func scrubMatchDryRun(ctx context.Context, flags globalFlags, cmd string, compiledPattern *regexp.Regexp, scope *string, gitDir string) int {
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

	// Scan submodules for matches.
	type subScanResult struct {
		sub     submodule.SubmoduleInfo
		results *scan.ScanResults
	}
	var subResults []subScanResult

	subs, err := submodule.Enumerate(ctx, gitDir)
	if err != nil {
		// Non-fatal: warn and continue with parent-only results.
		fmt.Fprintf(os.Stderr, "warning: enumerating submodules: %v\n", err)
	} else {
		for _, sub := range subs {
			if !sub.Initialized {
				continue
			}
			subScan, err := scan.ScanObjectsWithDir(ctx, compiledPattern, sub.GitDir, sub.WorkTreePath, sub.RelativePath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: scanning submodule %s: %v\n", sub.RelativePath, err)
				continue
			}
			if err := scan.AddAttributionWithDir(ctx, subScan, sub.GitDir, sub.WorkTreePath); err != nil {
				fmt.Fprintf(os.Stderr, "warning: adding attribution for submodule %s: %v\n", sub.RelativePath, err)
			}
			if len(subScan.Matches) > 0 {
				subResults = append(subResults, subScanResult{sub: sub, results: subScan})
			}
		}
	}

	// Categorize parent matches, applying scope filter to blobs.
	var blobMatches, commitMatches, tagMatches []scan.Match
	for _, m := range results.Matches {
		switch m.ObjectType {
		case "blob":
			if scope != nil && !matchScope(*scope, m.Path) {
				continue
			}
			blobMatches = append(blobMatches, m)
		case "commit":
			commitMatches = append(commitMatches, m)
		case "tag":
			tagMatches = append(tagMatches, m)
		}
	}

	totalMatches := len(results.Matches) + len(nonObjectMatches)
	for _, sr := range subResults {
		totalMatches += len(sr.results.Matches)
	}

	// Count unique objects
	uniqueObjects := make(map[string]bool)
	for _, m := range results.Matches {
		uniqueObjects[m.SHA] = true
	}
	for _, sr := range subResults {
		for _, m := range sr.results.Matches {
			uniqueObjects[m.SHA] = true
		}
	}

	fmt.Printf("Found %d matches in %d objects:\n", totalMatches, len(uniqueObjects)+len(nonObjectMatches))

	// Print parent repo header only if submodules have matches too.
	hasSubMatches := len(subResults) > 0
	if hasSubMatches {
		fmt.Printf("\nParent repo:\n")
	}

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

	// Print submodule results grouped by submodule.
	for _, sr := range subResults {
		fmt.Printf("\n[%s]:\n", sr.sub.RelativePath)
		var subBlobs, subCommits, subTags []scan.Match
		for _, m := range sr.results.Matches {
			switch m.ObjectType {
			case "blob":
				// Apply scope filtering for submodule blobs: strip submodule
				// prefix from scope before matching.
				if scope != nil {
					subScope := scopeForSubmodule(*scope, sr.sub.RelativePath)
					if subScope == "" || !matchScope(subScope, m.Path) {
						continue
					}
				}
				subBlobs = append(subBlobs, m)
			case "commit":
				subCommits = append(subCommits, m)
			case "tag":
				subTags = append(subTags, m)
			}
		}
		if len(subBlobs) > 0 {
			fmt.Printf("  Blobs:\n")
			for _, m := range subBlobs {
				if m.Path != "" && m.CommitSHA != "" {
					fmt.Printf("    %s in commit %s (line %d): %s\n", m.Path, shortSHA(m.CommitSHA), m.Line, m.Context)
				} else if m.Path != "" {
					fmt.Printf("    %s (unreachable, line %d): %s\n", m.Path, m.Line, m.Context)
				} else {
					fmt.Printf("    blob %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
				}
			}
		}
		if len(subCommits) > 0 {
			fmt.Printf("  Commit messages:\n")
			for _, m := range subCommits {
				fmt.Printf("    commit %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
			}
		}
		if len(subTags) > 0 {
			fmt.Printf("  Tag annotations:\n")
			for _, m := range subTags {
				fmt.Printf("    tag %s (line %d): %s\n", shortSHA(m.SHA), m.Line, m.Context)
			}
		}
	}

	fmt.Printf("\nSummary: %d blob matches, %d message matches, %d tag matches, %d file matches\n",
		len(blobMatches), len(commitMatches), len(tagMatches), len(nonObjectMatches))
	if results.Skipped > 0 {
		fmt.Printf("Binary blobs skipped: %d\n", results.Skipped)
	}

	return 0
}

// scopeForSubmodule checks if a scope pattern applies to a submodule. If the
// scope path starts with the submodule's relative path, the prefix is stripped
// and the remaining path is returned as the scope within the submodule. If the
// scope doesn't match the submodule's prefix, returns empty string (meaning
// this scope doesn't apply to this submodule). A bare glob like "*.env" applies
// to all repos (parent and submodules).
func scopeForSubmodule(scope, subRelPath string) string {
	// Bare globs (no path separator) apply everywhere.
	if !strings.Contains(scope, "/") {
		return scope
	}
	// Check if scope starts with the submodule path prefix.
	prefix := subRelPath + "/"
	if strings.HasPrefix(scope, prefix) {
		return scope[len(prefix):]
	}
	// Also check using filepath separator in case of OS-specific paths.
	fpPrefix := filepath.ToSlash(subRelPath) + "/"
	if strings.HasPrefix(scope, fpPrefix) {
		return scope[len(fpPrefix):]
	}
	return ""
}

// submoduleScrubResult holds the computed rewrite state for a single submodule
// before any refs are updated. All computation is deferred so that ref updates
// can be applied atomically after all submodules + parent succeed.
type submoduleScrubResult struct {
	sub              submodule.SubmoduleInfo
	shaMap           map[string]string
	rewrittenCount   int
	blobMap          map[string]string
	messagesModified int
}

// scrubMatchExecute performs the actual rewrite: builds blob replacement map,
// walks and rewrites commits, updates refs, rewrites tag annotations, syncs
// the index, and logs to the oplog. When scope is non-nil, only blobs that
// appear at paths matching the glob are included in the replacement map.
func scrubMatchExecute(
	ctx context.Context,
	flags globalFlags,
	cmd string,
	compiledPattern *regexp.Regexp,
	replace string,
	reason string,
	fromSHA string,
	entireHistory bool,
	scope *string,
	gitDir string,
	sgDir string,
) int {
	// Scan to find all matches
	fmt.Println("Scanning objects...")
	results, err := scan.ScanObjects(ctx, compiledPattern)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("scanning objects: %v", err))
	}

	// Enumerate submodules (4.4)
	subs, subErr := submodule.Enumerate(ctx, gitDir)
	if subErr != nil {
		fmt.Fprintf(os.Stderr, "warning: enumerating submodules: %v\n", subErr)
		subs = nil
	}
	// Ensure safegit dir exists for each initialized submodule.
	for _, sub := range subs {
		if sub.Initialized {
			if err := repo.EnsureInitialized(sub.GitDir); err != nil {
				fmt.Fprintf(os.Stderr, "warning: initializing safegit for submodule %s: %v\n", sub.RelativePath, err)
			}
		}
	}

	if len(results.Matches) == 0 {
		// Check submodules too before declaring no matches.
		anySubMatches := false
		for _, sub := range subs {
			if !sub.Initialized {
				continue
			}
			subResults, err := scan.ScanObjectsWithDir(ctx, compiledPattern, sub.GitDir, sub.WorkTreePath, sub.RelativePath)
			if err != nil {
				continue
			}
			if len(subResults.Matches) > 0 {
				anySubMatches = true
				break
			}
		}
		if !anySubMatches {
			fmt.Println("No matches found. Nothing to rewrite.")
			return 0
		}
	}

	// When --scope is set, build a blob SHA -> paths index so we can filter
	// blobs by the paths they appear at.
	var scopedBlobSHAs map[string]bool
	if scope != nil {
		scopedBlobSHAs, err = buildScopedBlobSet(ctx, *scope)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("building scoped blob set: %v", err))
		}
	}

	// Count matches by type for summary
	var blobMatchCount, commitMatchCount, tagMatchCount int
	uniqueBlobSHAs := make(map[string]bool)
	for _, m := range results.Matches {
		switch m.ObjectType {
		case "blob":
			if scope != nil && !scopedBlobSHAs[m.SHA] {
				continue
			}
			blobMatchCount++
			uniqueBlobSHAs[m.SHA] = true
		case "commit":
			commitMatchCount++
		case "tag":
			tagMatchCount++
		}
	}

	// Scan submodules for matches (4.5)
	type subScanInfo struct {
		sub           submodule.SubmoduleInfo
		uniqueBlobs   map[string]bool
		blobCount     int
		commitCount   int
		tagCount      int
	}
	var subScans []subScanInfo
	for _, sub := range subs {
		if !sub.Initialized {
			continue
		}
		// Determine if this submodule is in scope.
		if scope != nil {
			subScope := scopeForSubmodule(*scope, sub.RelativePath)
			if subScope == "" {
				continue
			}
		}
		subResults, err := scan.ScanObjectsWithDir(ctx, compiledPattern, sub.GitDir, sub.WorkTreePath, sub.RelativePath)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("scanning submodule %s: %v", sub.RelativePath, err))
		}
		if len(subResults.Matches) == 0 {
			continue
		}

		// Build scoped blob set for submodule if scope applies.
		var subScopedBlobs map[string]bool
		if scope != nil {
			subScope := scopeForSubmodule(*scope, sub.RelativePath)
			if subScope != "" {
				subScopedBlobs, err = buildScopedBlobSetWithDir(ctx, subScope, sub.GitDir, sub.WorkTreePath)
				if err != nil {
					die(flags, cmd, 1, fmt.Sprintf("building scoped blob set for submodule %s: %v", sub.RelativePath, err))
				}
			}
		}

		info := subScanInfo{sub: sub, uniqueBlobs: make(map[string]bool)}
		for _, m := range subResults.Matches {
			switch m.ObjectType {
			case "blob":
				if subScopedBlobs != nil && !subScopedBlobs[m.SHA] {
					continue
				}
				info.blobCount++
				info.uniqueBlobs[m.SHA] = true
			case "commit":
				info.commitCount++
			case "tag":
				info.tagCount++
			}
		}
		if info.blobCount > 0 || info.commitCount > 0 || info.tagCount > 0 {
			subScans = append(subScans, info)
		}
	}

	// If scope filtered out all blob matches and there are no message/tag matches,
	// treat as no matches.
	totalSubBlobCount := 0
	totalSubCommitCount := 0
	totalSubTagCount := 0
	for _, si := range subScans {
		totalSubBlobCount += si.blobCount
		totalSubCommitCount += si.commitCount
		totalSubTagCount += si.tagCount
	}
	if blobMatchCount == 0 && commitMatchCount == 0 && tagMatchCount == 0 &&
		totalSubBlobCount == 0 && totalSubCommitCount == 0 && totalSubTagCount == 0 {
		fmt.Println("No matches found within scope. Nothing to rewrite.")
		return 0
	}

	totalBlobs := blobMatchCount + totalSubBlobCount
	totalCommits := commitMatchCount + totalSubCommitCount
	totalTags := tagMatchCount + totalSubTagCount
	fmt.Printf("Found %d matches (%d in blobs, %d in commit messages, %d in tag annotations)\n",
		totalBlobs+totalCommits+totalTags, totalBlobs, totalCommits, totalTags)
	if len(subScans) > 0 {
		fmt.Printf("  Submodules with matches: %d\n", len(subScans))
	}

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

	// Save parent cwd for os.Chdir back.
	parentDir, err := os.Getwd()
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("getting cwd: %v", err))
	}

	// Phase 1: Compute all rewrites (submodules first, then parent).
	// No refs are updated in this phase. If anything fails, we abort cleanly.

	// 4.5-4.6: Process each submodule.
	var subScrubResults []submoduleScrubResult
	for _, si := range subScans {
		fmt.Printf("Scrubbing submodule [%s]...\n", si.sub.RelativePath)

		// Chdir into submodule working tree so git commands target it.
		if err := os.Chdir(si.sub.WorkTreePath); err != nil {
			die(flags, cmd, 1, fmt.Sprintf("chdir to submodule %s: %v", si.sub.RelativePath, err))
		}

		// Build blob replacement map for this submodule.
		subBlobMap := make(map[string]string, len(si.uniqueBlobs))
		for blobSHA := range si.uniqueBlobs {
			content, err := git.CatFileBlob(ctx, blobSHA)
			if err != nil {
				// Chdir back before dying.
				os.Chdir(parentDir)
				die(flags, cmd, 1, fmt.Sprintf("submodule %s: reading blob %s: %v", si.sub.RelativePath, blobSHA, err))
			}
			if isBinaryContent(content) {
				continue
			}
			modified := compiledPattern.ReplaceAll(content, []byte(replace))
			if bytes.Equal(content, modified) {
				continue
			}
			newSHA, err := git.HashObjectWriteBytes(ctx, modified)
			if err != nil {
				os.Chdir(parentDir)
				die(flags, cmd, 1, fmt.Sprintf("submodule %s: writing replaced blob: %v", si.sub.RelativePath, err))
			}
			subBlobMap[blobSHA] = newSHA
			if flags.verbose {
				fmt.Fprintf(os.Stderr, "  [%s] blob %s -> %s\n", si.sub.RelativePath, shortSHA(blobSHA), shortSHA(newSHA))
			}
		}

		// Determine commit range for the submodule.
		var subSHAs []string
		if entireHistory {
			out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", "HEAD")
			if err != nil {
				os.Chdir(parentDir)
				die(flags, cmd, 1, fmt.Sprintf("submodule %s: listing commits: %v", si.sub.RelativePath, err))
			}
			subSHAs = splitNonEmpty(out)
		} else {
			// For submodules with --from, use entire history since we don't know
			// the corresponding commit boundary in the submodule.
			out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", "HEAD")
			if err != nil {
				os.Chdir(parentDir)
				die(flags, cmd, 1, fmt.Sprintf("submodule %s: listing commits: %v", si.sub.RelativePath, err))
			}
			subSHAs = splitNonEmpty(out)
		}

		subMessagesModified := 0
		subShaMap, subRewrittenCount, err := walkAndRewrite(ctx, subSHAs, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string) (CommitTransform, error) {
			var xform CommitTransform

			newTreeSHA, err := replaceInTreeByBlobMap(ctx, info.Tree, subBlobMap, nil)
			if err != nil {
				return CommitTransform{}, fmt.Errorf("replacing blobs in tree for commit %s: %w", sha, err)
			}
			if newTreeSHA != info.Tree {
				xform.TreeSHA = newTreeSHA
			}

			if compiledPattern.Match([]byte(info.Message)) {
				newMessage := compiledPattern.ReplaceAllString(info.Message, replace)
				if newMessage != info.Message {
					xform.Message = newMessage
					subMessagesModified++
				}
			}

			return xform, nil
		}, flags.verbose)
		if err != nil {
			os.Chdir(parentDir)
			die(flags, cmd, 1, fmt.Sprintf("submodule %s: walk and rewrite: %v", si.sub.RelativePath, err))
		}

		subScrubResults = append(subScrubResults, submoduleScrubResult{
			sub:              si.sub,
			shaMap:           subShaMap,
			rewrittenCount:   subRewrittenCount,
			blobMap:          subBlobMap,
			messagesModified: subMessagesModified,
		})

		fmt.Printf("  [%s] %d commits rewritten, %d blobs replaced\n",
			si.sub.RelativePath, subRewrittenCount, len(subBlobMap))
	}

	// Chdir back to parent for parent-repo processing.
	if err := os.Chdir(parentDir); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("chdir back to parent: %v", err))
	}

	// Build the combined gitlink map from all submodule SHA mappings.
	// This maps old submodule commit SHAs to new ones, so the parent's tree
	// rewriter updates gitlink entries.
	var gitlinkMap map[string]string
	if len(subScrubResults) > 0 {
		gitlinkMap = make(map[string]string)
		for _, sr := range subScrubResults {
			for old, new_ := range sr.shaMap {
				if old != new_ {
					gitlinkMap[old] = new_
				}
			}
		}
		if len(gitlinkMap) == 0 {
			gitlinkMap = nil
		}
	}

	// Parent repo: build blob replacement map.
	fmt.Println("Building blob replacement map...")
	blobMap := make(map[string]string, len(uniqueBlobSHAs))
	for blobSHA := range uniqueBlobSHAs {
		content, err := git.CatFileBlob(ctx, blobSHA)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("reading blob %s: %v", blobSHA, err))
		}
		if isBinaryContent(content) {
			continue
		}
		modified := compiledPattern.ReplaceAll(content, []byte(replace))
		if bytes.Equal(content, modified) {
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

	// Determine parent commit range.
	var shas []string
	if entireHistory {
		out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", "HEAD")
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("listing commits: %v", err))
		}
		shas = splitNonEmpty(out)
	} else {
		out, _, err := git.Run(ctx, "rev-list", "--topo-order", "--reverse", fromSHA+"..HEAD")
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("listing commits: %v", err))
		}
		shas = append([]string{fromSHA}, splitNonEmpty(out)...)
	}

	commitCount := len(shas)
	fmt.Printf("Rewriting %d commits...\n", commitCount)

	// Track how many commit messages were modified.
	messagesModified := 0
	replaceBytes := []byte(replace)

	// Walk and rewrite parent, passing gitlinkMap for submodule SHA remapping.
	shaMap, rewrittenCount, err := walkAndRewrite(ctx, shas, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string) (CommitTransform, error) {
		var xform CommitTransform

		newTreeSHA, err := replaceInTreeByBlobMap(ctx, info.Tree, blobMap, gitlinkMap)
		if err != nil {
			return CommitTransform{}, fmt.Errorf("replacing blobs in tree for commit %s: %w", sha, err)
		}
		if newTreeSHA != info.Tree {
			xform.TreeSHA = newTreeSHA
		}

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

	// Phase 2 (4.7): Apply all ref updates atomically.
	// Submodules first, then parent.

	for _, sr := range subScrubResults {
		fmt.Printf("Updating refs for submodule [%s]...\n", sr.sub.RelativePath)
		if err := os.Chdir(sr.sub.WorkTreePath); err != nil {
			die(flags, cmd, 1, fmt.Sprintf("chdir to submodule %s for ref update: %v", sr.sub.RelativePath, err))
		}

		if err := updateRefs(ctx, sr.shaMap, "", "", "", "", flags.verbose); err != nil {
			os.Chdir(parentDir)
			die(flags, cmd, 1, fmt.Sprintf("submodule %s: updating refs: %v", sr.sub.RelativePath, err))
		}

		subTagsRewritten := rewriteTagAnnotations(ctx, flags, cmd, compiledPattern, replaceBytes, sr.shaMap)
		if subTagsRewritten > 0 && flags.verbose {
			fmt.Fprintf(os.Stderr, "  [%s] %d tag annotations rewritten\n", sr.sub.RelativePath, subTagsRewritten)
		}

		if err := cleanupAfterRewrite(ctx, flags, cmd, sr.shaMap, sr.sub.SafegitDir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: submodule %s post-rewrite cleanup: %v\n", sr.sub.RelativePath, err)
		}

		// Oplog entry for submodule.
		subRef, _ := git.HeadRef(ctx)
		if subRef == "" {
			subRef = "HEAD (detached)"
		}
		subNewHead, _ := git.RevParse(ctx, "HEAD")
		_ = oplog.Append(sr.sub.SafegitDir, oplog.Entry{
			Op: "scrub-match",
			Extra: map[string]interface{}{
				"ref":              subRef,
				"pattern":          compiledPattern.String(),
				"replace":          replace,
				"reason":           reason,
				"sha":              subNewHead,
				"rewritten":        sr.rewrittenCount,
				"blobsReplaced":    len(sr.blobMap),
				"messagesModified": sr.messagesModified,
				"parentScrub":      true,
			},
		})
	}

	// Chdir back to parent for parent ref updates.
	if err := os.Chdir(parentDir); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("chdir back to parent for ref update: %v", err))
	}

	// Update parent refs.
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

	// Oplog entry for parent
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
			"submodules":       len(subScrubResults),
		},
	})

	// Surgical post-rewrite cleanup for parent
	if err := cleanupAfterRewrite(ctx, flags, cmd, shaMap, sgDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: post-rewrite cleanup: %v\n", err)
	}

	// Phase 3 (4.8): Verification -- re-scan all object stores.
	fmt.Println("Verifying secret removal...")
	exitCode := 0
	verifyErr := verifySecretRemovedScoped(ctx, compiledPattern, scope)
	if verifyErr != nil {
		fmt.Fprintf(os.Stderr, "CRITICAL: %v\n", verifyErr)
		fmt.Fprintln(os.Stderr, "Secret may still be present in the parent object store.")
		exitCode = 1
	}

	// Verify submodule object stores.
	for _, sr := range subScrubResults {
		subScope := scope
		if scope != nil {
			ss := scopeForSubmodule(*scope, sr.sub.RelativePath)
			if ss != "" {
				subScope = &ss
			} else {
				subScope = nil
			}
		}
		subResults, err := scan.ScanObjectsWithDir(ctx, compiledPattern, sr.sub.GitDir, sr.sub.WorkTreePath, sr.sub.RelativePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "CRITICAL: re-scan submodule %s failed: %v\n", sr.sub.RelativePath, err)
			exitCode = 1
			continue
		}
		if len(subResults.Matches) > 0 {
			// If scoped, filter matches.
			remaining := subResults.Matches
			if subScope != nil {
				if err := scan.AddAttributionWithDir(ctx, subResults, sr.sub.GitDir, sr.sub.WorkTreePath); err == nil {
					var filtered []scan.Match
					for _, m := range remaining {
						if m.ObjectType == "blob" && !matchScope(*subScope, m.Path) {
							continue
						}
						filtered = append(filtered, m)
					}
					remaining = filtered
				}
			}
			if len(remaining) > 0 {
				fmt.Fprintf(os.Stderr, "CRITICAL: secret still present in submodule %s (%d matches)\n",
					sr.sub.RelativePath, len(remaining))
				exitCode = 1
			}
		}
	}

	// Verify gitlinks in parent's rewritten history resolve to valid commits.
	if len(subScrubResults) > 0 {
		if err := verifyGitlinksAfterScrub(ctx, shaMap, subScrubResults); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: gitlink verification: %v\n", err)
		}
	}

	if exitCode == 0 {
		fmt.Println("Verification passed: no matches found in object stores.")
	} else {
		fmt.Fprintln(os.Stderr, "Run 'git reflog expire --expire=now --all && git gc --prune=now' to force cleanup.")
	}

	// Summary
	fmt.Printf("\nScrub complete:\n")
	fmt.Printf("  %d commits rewritten\n", rewrittenCount)
	fmt.Printf("  %d blobs replaced\n", len(blobMap))
	fmt.Printf("  %d commit messages modified\n", messagesModified)
	fmt.Printf("  %d tag annotations rewritten\n", tagsRewritten)
	if len(subScrubResults) > 0 {
		totalSubRewritten := 0
		totalSubBlobs := 0
		for _, sr := range subScrubResults {
			totalSubRewritten += sr.rewrittenCount
			totalSubBlobs += len(sr.blobMap)
		}
		fmt.Printf("  %d submodule commits rewritten\n", totalSubRewritten)
		fmt.Printf("  %d submodule blobs replaced\n", totalSubBlobs)
	}
	fmt.Printf("  Old HEAD: %s\n", oldHeadSHA[:12])
	fmt.Printf("  New HEAD: %s\n", newHeadSHA[:12])
	fmt.Printf("\nTo update the remote, run: git push --force-with-lease\n")

	return exitCode
}

// verifyGitlinksAfterScrub checks that every gitlink in the parent's rewritten
// history resolves to a valid commit in the corresponding submodule.
func verifyGitlinksAfterScrub(ctx context.Context, parentShaMap map[string]string, subResults []submoduleScrubResult) error {
	// Build a set of all valid new submodule commit SHAs.
	validSubCommits := make(map[string]bool)
	for _, sr := range subResults {
		for _, newSHA := range sr.shaMap {
			validSubCommits[newSHA] = true
		}
	}

	// Check HEAD's tree for gitlinks.
	headSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("resolving HEAD: %w", err)
	}
	headInfo, err := git.ParseCommit(ctx, headSHA)
	if err != nil {
		return fmt.Errorf("parsing HEAD commit: %w", err)
	}
	entries, err := git.LsTree(ctx, headInfo.Tree)
	if err != nil {
		return fmt.Errorf("ls-tree HEAD: %w", err)
	}

	var failures []string
	for _, e := range entries {
		if e.ObjectType == "commit" {
			// This is a gitlink. Check that it resolves.
			if !validSubCommits[e.SHA] {
				// It might be an unmodified submodule commit (not in the rewrite set).
				// Check that the SHA exists in one of the submodule git dirs.
				found := false
				for _, sr := range subResults {
					if sr.sub.RelativePath == e.Path {
						// Check if this SHA is valid in the submodule.
						_, _, catErr := git.RunWithGitDir(ctx, sr.sub.GitDir, sr.sub.WorkTreePath, "cat-file", "-t", e.SHA)
						if catErr == nil {
							found = true
						}
						break
					}
				}
				if !found {
					failures = append(failures, fmt.Sprintf("gitlink %s at %s does not resolve to a valid commit", shortSHA(e.SHA), e.Path))
				}
			}
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("%d gitlink(s) invalid:\n  %s", len(failures), strings.Join(failures, "\n  "))
	}
	return nil
}

// buildScopedBlobSetWithDir enumerates all blob-to-path mappings across all
// refs in a specific git directory and returns the set of blob SHAs that appear
// at paths matching the scope glob.
func buildScopedBlobSetWithDir(ctx context.Context, scope, gitDir, workTree string) (map[string]bool, error) {
	stdout, _, err := git.RunWithGitDir(ctx, gitDir, workTree, "rev-list", "--all", "--objects")
	if err != nil {
		return nil, fmt.Errorf("rev-list --all --objects: %w", err)
	}

	result := make(map[string]bool)
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		spaceIdx := strings.IndexByte(line, ' ')
		if spaceIdx < 0 {
			continue
		}
		sha := line[:spaceIdx]
		filePath := line[spaceIdx+1:]
		if matchScope(scope, filePath) {
			result[sha] = true
		}
	}
	return result, nil
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

// verifySecretRemovedScoped re-scans git objects for the given pattern after a
// scrub rewrite. When scope is non-nil, only blob matches at paths matching the
// scope glob are considered failures; out-of-scope blobs are expected to still
// contain the pattern. Non-blob matches (commit messages, tags) are always
// checked regardless of scope.
func verifySecretRemovedScoped(ctx context.Context, pattern *regexp.Regexp, scope *string) error {
	if scope == nil {
		return verifySecretRemoved(ctx, pattern)
	}

	results, err := scan.ScanObjects(ctx, pattern)
	if err != nil {
		return fmt.Errorf("re-scan failed: %w", err)
	}
	if len(results.Matches) == 0 {
		return nil
	}

	// Add attribution so blob matches have paths.
	if err := scan.AddAttribution(ctx, results); err != nil {
		return fmt.Errorf("adding attribution for verification: %w", err)
	}

	// Also build the scoped blob set to check blobs by SHA (some blobs may
	// appear at multiple paths, and attribution only records one).
	scopedBlobs, err := buildScopedBlobSet(ctx, *scope)
	if err != nil {
		return fmt.Errorf("building scoped blob set for verification: %w", err)
	}

	var failures []scan.Match
	for _, m := range results.Matches {
		switch m.ObjectType {
		case "blob":
			// Only report blobs that are in scope (by path match or by SHA).
			if matchScope(*scope, m.Path) || scopedBlobs[m.SHA] {
				failures = append(failures, m)
			}
		default:
			// Commit messages and tags are always checked.
			failures = append(failures, m)
		}
	}

	if len(failures) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("secret still present in %d object(s):\n", len(failures)))
	for _, m := range failures {
		reachable := "unreachable"
		if m.Reachable {
			reachable = "reachable"
		}
		sb.WriteString(fmt.Sprintf("  %s %s (%s, line %d): %s\n",
			m.ObjectType, shortSHA(m.SHA), reachable, m.Line, m.Context))
	}
	return fmt.Errorf("%s", sb.String())
}

// matchScope checks whether a file path matches a glob scope pattern.
// It tries path.Match against both the full path and the basename, so
// patterns like "*.env" match "config/secret.env" as well as "secret.env".
func matchScope(scope, filePath string) bool {
	if filePath == "" {
		return false
	}
	// Try matching against the full path first (supports patterns like "config/**").
	if matched, _ := path.Match(scope, filePath); matched {
		return true
	}
	// Try matching against just the filename (supports patterns like "*.env").
	base := path.Base(filePath)
	if matched, _ := path.Match(scope, base); matched {
		return true
	}
	return false
}

// buildScopedBlobSet enumerates all blob-to-path mappings across all refs
// using `git rev-list --all --objects` and returns the set of blob SHAs that
// appear at paths matching the scope glob.
func buildScopedBlobSet(ctx context.Context, scope string) (map[string]bool, error) {
	stdout, _, err := git.Run(ctx, "rev-list", "--all", "--objects")
	if err != nil {
		return nil, fmt.Errorf("rev-list --all --objects: %w", err)
	}

	result := make(map[string]bool)
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		spaceIdx := strings.IndexByte(line, ' ')
		if spaceIdx < 0 {
			// No space: this is a commit SHA, skip.
			continue
		}
		sha := line[:spaceIdx]
		filePath := line[spaceIdx+1:]
		if matchScope(scope, filePath) {
			result[sha] = true
		}
	}
	return result, nil
}

