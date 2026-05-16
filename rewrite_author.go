package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
)

func runRewriteAuthor(flags globalFlags, kwargs map[string]interface{}) int {
	const cmd = "rewrite-author"

	// Extract optional string flags (nil when not provided).
	var oldName, newName, oldEmail, newEmail string
	if v := kwargs["old_name"]; v != nil {
		oldName = v.(string)
	}
	if v := kwargs["new_name"]; v != nil {
		newName = v.(string)
	}
	if v := kwargs["old_email"]; v != nil {
		oldEmail = v.(string)
	}
	if v := kwargs["new_email"]; v != nil {
		newEmail = v.(string)
	}
	push := kwargs["push"].(bool)

	// At least one pair must be provided (CoRequired ensures pairs, but both
	// pairs could be absent).
	if oldName == "" && oldEmail == "" {
		fmt.Fprintf(os.Stderr, "error: at least one of --old-name or --old-email is required\n")
		fmt.Fprintf(os.Stderr, "Run 'safegit rewrite-author --help' for usage.\n")
		return 2
	}

	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		die(flags, cmd, 4, err.Error())
	}

	sgDir := repo.SafegitDir(gitDir)
	ctx := context.Background()

	// Check for uncommitted changes
	statusOut, _, err := git.Run(ctx, "status", "--porcelain")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("checking working tree: %v", err))
	}
	if strings.TrimSpace(statusOut) != "" && !flags.force {
		die(flags, cmd, 1, "working tree is dirty; commit changes or use --force to skip this check")
	}

	// Dry-run mode
	if flags.dryRun {
		dryArgs := append([]string{"rev-list", "--topo-order", "--reverse"}, refGlobs...)
		out, _, err := git.Run(ctx, dryArgs...)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("listing commits: %v", err))
		}
		shas := splitNonEmpty(out)

		var affected []string
		for _, sha := range shas {
			info, err := git.ParseCommit(ctx, sha)
			if err != nil {
				die(flags, cmd, 1, fmt.Sprintf("parsing commit %s: %v", sha, err))
			}
			nameMatch := oldName != "" && (info.Author.Name == oldName || info.Committer.Name == oldName)
			emailMatch := oldEmail != "" && (info.Author.Email == oldEmail || info.Committer.Email == oldEmail)
			var commitMatches bool
			if oldName != "" && oldEmail != "" {
				commitMatches = nameMatch && emailMatch
			} else {
				commitMatches = nameMatch || emailMatch
			}
			if commitMatches {
				affected = append(affected, sha)
			}
		}

		var rewriteDesc string
		switch {
		case oldName != "" && oldEmail != "":
			rewriteDesc = fmt.Sprintf("name: %s -> %s, email: %s -> %s", oldName, newName, oldEmail, newEmail)
		case oldName != "":
			rewriteDesc = fmt.Sprintf("name: %s -> %s", oldName, newName)
		default:
			rewriteDesc = fmt.Sprintf("email: %s -> %s", oldEmail, newEmail)
		}
		fmt.Printf("Would rewrite %d of %d commits (%s)\n",
			len(affected), len(shas), rewriteDesc)
		limit := len(affected)
		if limit > 5 {
			limit = 5
		}
		for _, sha := range affected[:limit] {
			info, _ := git.ParseCommit(ctx, sha)
			fmt.Printf("  %s  author=%s committer=%s\n", sha[:12], info.Author.Name, info.Committer.Name)
		}
		if len(affected) > 5 {
			fmt.Printf("  ... and %d more\n", len(affected)-5)
		}
		return 0
	}

	// Confirmation prompt (skipped with --force)
	if !flags.force {
		countArgs := append([]string{"rev-list", "--topo-order", "--reverse"}, refGlobs...)
		out, _, err := git.Run(ctx, countArgs...)
		if err != nil {
			die(flags, cmd, 1, fmt.Sprintf("listing commits: %v", err))
		}
		total := len(splitNonEmpty(out))

		fmt.Printf("About to rewrite %d commits. This cannot be undone. Proceed? [y/N] ", total)
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("Aborted.")
			return 0
		}
	}

	// Actual rewrite
	fmt.Println("Capturing pre-rewrite snapshot...")
	before, err := captureSnapshot(ctx)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("capturing pre-rewrite snapshot: %v", err))
	}

	fmt.Println("Rewriting commits...")
	shaMap, nameChanged, err := rewriteCommits(ctx, oldName, newName, oldEmail, newEmail, flags.verbose)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("rewriting commits: %v", err))
	}

	fmt.Println("Updating refs...")
	if err := updateRefs(ctx, shaMap, oldName, newName, oldEmail, newEmail, flags.verbose); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("updating refs: %v", err))
	}

	fmt.Println("Capturing post-rewrite snapshot...")
	after, err := captureSnapshot(ctx)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("capturing post-rewrite snapshot: %v", err))
	}

	fmt.Println("Verifying...")
	failures := compareSnapshots(before, after, oldName, newName, oldEmail)

	// Also verify working tree is clean after rewrite
	statusOut, _, err = git.Run(ctx, "status", "--porcelain")
	if err != nil {
		failures = append(failures, fmt.Sprintf("checking working tree after rewrite: %v", err))
	} else if strings.TrimSpace(statusOut) != "" {
		failures = append(failures, "working tree is dirty after rewrite")
	}

	if len(failures) > 0 {
		for _, f := range failures {
			fmt.Fprintf(os.Stderr, "  FAIL: %s\n", f)
		}
		die(flags, cmd, 1, "VERIFICATION FAILED")
	}

	// Count checks: compareSnapshots runs 12 categories + tag-to-message per tag + working tree = 13 base checks
	fmt.Println("Verification passed: all checks OK")

	// Summary
	identity := 0
	for k, v := range shaMap {
		if k == v {
			identity++
		}
	}
	total := len(shaMap)
	parentOnly := total - nameChanged - identity
	if parentOnly == 1 {
		fmt.Printf("Rewrote %d commits (%d had name changes, %d was inherited (ancestors changed))\n",
			total, nameChanged, parentOnly)
	} else {
		fmt.Printf("Rewrote %d commits (%d had name changes, %d were inherited (ancestors changed))\n",
			total, nameChanged, parentOnly)
	}

	// Oplog entry
	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "rewrite-author",
		Extra: map[string]interface{}{
			"oldName":          oldName,
			"newName":          newName,
			"commitsRewritten": len(shaMap),
			"nameChanged":      nameChanged,
		},
	})

	// Push confirmation (skipped with --force)
	if push && !flags.force {
		fmt.Printf("Force-push ALL branches and tags to origin? [y/N] ")
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("Push skipped.")
			push = false
		}
	}

	// Push if requested
	if push {
		if _, _, err := git.Run(ctx, "remote", "get-url", "origin"); err != nil {
			die(flags, cmd, 1, "no 'origin' remote configured; add one with git remote add origin <url>")
		}
		fmt.Println("Force-pushing all branches and tags...")
		if _, _, err := git.Run(ctx, "push", "origin", "--all", "--force"); err != nil {
			die(flags, cmd, 1, fmt.Sprintf("pushing branches: %v", err))
		}
		if _, _, err := git.Run(ctx, "push", "origin", "--tags", "--force"); err != nil {
			die(flags, cmd, 1, fmt.Sprintf("pushing tags: %v", err))
		}
		fmt.Println("Push complete.")
	}
	return 0
}

// rewriteSnapshot captures the state of a repository's history for
// verification before and after an author-rewrite operation. Fields
// ordered by topo-order are parallel slices -- index i in each slice
// corresponds to the same commit.
type rewriteSnapshot struct {
	CommitCount    int
	TagCount       int
	BranchNames    []string
	TagNames       []string
	Messages       []string            // full commit messages, topo-order
	AuthorDates    []string            // topo-order
	CommitterDates []string            // topo-order
	AuthorEmails   []string            // topo-order
	AuthorNames    []string            // topo-order
	CommitterNames []string            // topo-order
	TreeHashes     []string            // topo-order
	ParentCounts   []int               // topo-order
	TagToMessage   map[string]string   // tag name -> subject of the commit it points to
}

// refGlobs limits rev-list/log walks to branches, tags, and remote tracking
// refs, excluding refs/stash, refs/notes, and other non-standard refs.
var refGlobs = []string{"--glob=refs/heads", "--glob=refs/tags", "--glob=refs/remotes"}

// captureSnapshot collects all verification data points from the current
// repository state using git plumbing commands. Each data type uses a
// separate git log call for straightforward parsing.
func captureSnapshot(ctx context.Context) (rewriteSnapshot, error) {
	var snap rewriteSnapshot

	// Commit count
	args := append([]string{"rev-list", "--count"}, refGlobs...)
	out, _, err := git.Run(ctx, args...)
	if err != nil {
		return snap, fmt.Errorf("counting commits: %w", err)
	}
	count := 0
	fmt.Sscanf(strings.TrimSpace(out), "%d", &count)
	snap.CommitCount = count

	// Tree hashes
	snap.TreeHashes, err = logLines(ctx, "%T")
	if err != nil {
		return snap, fmt.Errorf("reading tree hashes: %w", err)
	}

	// Author dates
	snap.AuthorDates, err = logLines(ctx, "%ai")
	if err != nil {
		return snap, fmt.Errorf("reading author dates: %w", err)
	}

	// Committer dates
	snap.CommitterDates, err = logLines(ctx, "%ci")
	if err != nil {
		return snap, fmt.Errorf("reading committer dates: %w", err)
	}

	// Author emails
	snap.AuthorEmails, err = logLines(ctx, "%ae")
	if err != nil {
		return snap, fmt.Errorf("reading author emails: %w", err)
	}

	// Author names
	snap.AuthorNames, err = logLines(ctx, "%an")
	if err != nil {
		return snap, fmt.Errorf("reading author names: %w", err)
	}

	// Committer names
	snap.CommitterNames, err = logLines(ctx, "%cn")
	if err != nil {
		return snap, fmt.Errorf("reading committer names: %w", err)
	}

	// Parent hashes (space-separated per line) -> parent counts.
	// Root commits produce empty lines (no parents), so we must NOT use
	// logLines (which drops empty lines via splitNonEmpty). Instead, split
	// raw output preserving empty entries.
	parentArgs := append([]string{"log", "--topo-order", "--format=%P"}, refGlobs...)
	out, _, err = git.Run(ctx, parentArgs...)
	if err != nil {
		return snap, fmt.Errorf("reading parent hashes: %w", err)
	}
	parentRaw := strings.TrimRight(out, "\n")
	if parentRaw == "" {
		snap.ParentCounts = nil
	} else {
		parentLines := strings.Split(parentRaw, "\n")
		snap.ParentCounts = make([]int, len(parentLines))
		for i, line := range parentLines {
			if line == "" {
				snap.ParentCounts[i] = 0
			} else {
				snap.ParentCounts[i] = len(strings.Fields(line))
			}
		}
	}

	// Full commit messages: use \x01 as a record separator before each
	// message body (%B can contain newlines).
	out, _, err = git.Run(ctx, "log", "--all", "--topo-order", "--format=%x01%B")
	if err != nil {
		return snap, fmt.Errorf("reading commit messages: %w", err)
	}
	snap.Messages = splitMessages(out)

	// Branch names
	out, _, err = git.Run(ctx, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return snap, fmt.Errorf("reading branch names: %w", err)
	}
	snap.BranchNames = splitSorted(out)

	// Tag names
	out, _, err = git.Run(ctx, "tag", "-l")
	if err != nil {
		return snap, fmt.Errorf("reading tag names: %w", err)
	}
	snap.TagNames = splitSorted(out)
	snap.TagCount = len(snap.TagNames)

	// Tag-to-message map
	snap.TagToMessage, err = buildTagToMessage(ctx, snap.TagNames)
	if err != nil {
		return snap, fmt.Errorf("building tag-to-message map: %w", err)
	}

	return snap, nil
}

// logLines runs git log --all --topo-order with the given format and
// returns non-empty output lines.
func logLines(ctx context.Context, format string) ([]string, error) {
	args := append([]string{"log", "--topo-order", "--format=" + format}, refGlobs...)
	out, _, err := git.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return splitNonEmpty(out), nil
}

// splitNonEmpty splits on newlines and drops empty entries.
func splitNonEmpty(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	result := make([]string, 0, len(lines))
	for _, l := range lines {
		if l != "" {
			result = append(result, l)
		}
	}
	return result
}

// splitSorted splits on newlines, drops empties, and sorts the result.
func splitSorted(s string) []string {
	lines := splitNonEmpty(s)
	sort.Strings(lines)
	return lines
}

// splitMessages parses the output of git log --format=%x01%B into
// individual commit messages. Each record starts with \x01, followed
// by the full message body (which may span multiple lines).
func splitMessages(raw string) []string {
	parts := strings.Split(raw, "\x01")
	var msgs []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		msgs = append(msgs, trimmed)
	}
	return msgs
}

// buildTagToMessage maps each tag name to the subject line of the
// commit it ultimately points to (dereferencing annotated tags).
func buildTagToMessage(ctx context.Context, tagNames []string) (map[string]string, error) {
	m := make(map[string]string, len(tagNames))
	for _, tag := range tagNames {
		// Dereference to commit in case of annotated tags.
		sha, _, err := git.Run(ctx, "rev-parse", tag+"^{commit}")
		if err != nil {
			// Tag might not point to a commit (e.g. a tag on a blob); skip.
			continue
		}
		sha = strings.TrimSpace(sha)
		subject, _, err := git.Run(ctx, "log", "-1", "--format=%s", sha)
		if err != nil {
			continue
		}
		m[tag] = strings.TrimSpace(subject)
	}
	return m, nil
}

// compareSnapshots compares a before and after snapshot to verify that a
// history rewrite preserved everything except author/committer names.
// oldName is the author name that should no longer appear after the
// rewrite. Returns a slice of failure descriptions; an empty slice
// means all checks passed.
func compareSnapshots(before, after rewriteSnapshot, oldName, newName, oldEmail string) []string {
	var failures []string

	// 1. Commit count
	if before.CommitCount != after.CommitCount {
		failures = append(failures, fmt.Sprintf(
			"commit count changed: before=%d, after=%d",
			before.CommitCount, after.CommitCount))
	}

	// 2. Tag count
	if before.TagCount != after.TagCount {
		failures = append(failures, fmt.Sprintf(
			"tag count changed: before=%d, after=%d",
			before.TagCount, after.TagCount))
	}

	// 3. Branch names
	if msg := compareStringSlices("branch names", before.BranchNames, after.BranchNames); msg != "" {
		failures = append(failures, msg)
	}

	// 4. Tag names
	if msg := compareStringSlices("tag names", before.TagNames, after.TagNames); msg != "" {
		failures = append(failures, msg)
	}

	// 5. Old name must not appear in author or committer names
	//    (skip when oldName is empty or old and new names are identical).
	//    When AND matching (oldEmail != ""), commits with oldName but a
	//    different email are intentionally preserved -- only check that no
	//    commit has BOTH the old name AND the old email.
	if oldName != "" && oldName != newName {
		if oldEmail != "" {
			// AND matching: only flag commits where both name and email match.
			for i, name := range after.AuthorNames {
				if name == oldName && i < len(after.AuthorEmails) && after.AuthorEmails[i] == oldEmail {
					failures = append(failures, fmt.Sprintf(
						"old author name %q with old email %q still present at commit index %d", oldName, oldEmail, i))
					break
				}
			}
			// CommitterNames don't have a parallel CommitterEmails slice in
			// the snapshot, so skip the AND check for committers.
		} else {
			// Name-only matching: oldName must not appear at all.
			for i, name := range after.AuthorNames {
				if name == oldName {
					failures = append(failures, fmt.Sprintf(
						"old author name %q still present in author at commit index %d", oldName, i))
					break
				}
			}
			for i, name := range after.CommitterNames {
				if name == oldName {
					failures = append(failures, fmt.Sprintf(
						"old author name %q still present in committer at commit index %d", oldName, i))
					break
				}
			}
		}
	}

	// 6. Messages
	if msg := compareStringSlices("messages", before.Messages, after.Messages); msg != "" {
		failures = append(failures, msg)
	}

	// 7. Author dates
	if msg := compareStringSlices("author dates", before.AuthorDates, after.AuthorDates); msg != "" {
		failures = append(failures, msg)
	}

	// 8. Committer dates
	if msg := compareStringSlices("committer dates", before.CommitterDates, after.CommitterDates); msg != "" {
		failures = append(failures, msg)
	}

	// 9. Author emails
	if oldEmail != "" {
		for i, email := range after.AuthorEmails {
			if email == oldEmail {
				failures = append(failures, fmt.Sprintf(
					"old email %q still present in author at commit index %d", oldEmail, i))
				break
			}
		}
	} else {
		if msg := compareStringSlices("author emails", before.AuthorEmails, after.AuthorEmails); msg != "" {
			failures = append(failures, msg)
		}
	}

	// 10. Tree hashes
	if msg := compareStringSlices("tree hashes", before.TreeHashes, after.TreeHashes); msg != "" {
		failures = append(failures, msg)
	}

	// 11. Parent counts
	if msg := compareIntSlices("parent counts", before.ParentCounts, after.ParentCounts); msg != "" {
		failures = append(failures, msg)
	}

	// 12. Tag-to-message mapping
	for tag, beforeMsg := range before.TagToMessage {
		afterMsg, ok := after.TagToMessage[tag]
		if !ok {
			failures = append(failures, fmt.Sprintf(
				"tag %q missing from after snapshot", tag))
			continue
		}
		if beforeMsg != afterMsg {
			failures = append(failures, fmt.Sprintf(
				"tag %q message changed: before=%q, after=%q", tag, beforeMsg, afterMsg))
		}
	}
	for tag := range after.TagToMessage {
		if _, ok := before.TagToMessage[tag]; !ok {
			failures = append(failures, fmt.Sprintf(
				"tag %q present in after snapshot but missing from before", tag))
		}
	}

	return failures
}

// compareStringSlices reports the first difference between two string slices.
// Returns an empty string if they are identical.
func compareStringSlices(label string, before, after []string) string {
	if len(before) != len(after) {
		return fmt.Sprintf("%s: length differs: before=%d, after=%d", label, len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			return fmt.Sprintf("%s: differ at index %d: before=%q, after=%q",
				label, i, before[i], after[i])
		}
	}
	return ""
}

// compareIntSlices reports the first difference between two int slices.
// Returns an empty string if they are identical.
func compareIntSlices(label string, before, after []int) string {
	if len(before) != len(after) {
		return fmt.Sprintf("%s: length differs: before=%d, after=%d", label, len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			return fmt.Sprintf("%s: differ at index %d: before=%d, after=%d",
				label, i, before[i], after[i])
		}
	}
	return ""
}

// rewriteCommits walks all commits in dependency order (parents before
// children) and creates new commit objects wherever the author/committer
// name matches oldName or a parent SHA was remapped by an earlier rewrite.
// Returns the old-to-new SHA mapping, the count of commits whose name was
// actually changed, and any error.
func rewriteCommits(ctx context.Context, oldName, newName, oldEmail, newEmail string, verbose bool) (map[string]string, int, error) {
	// Get all commits in topo-order with parents before children.
	args := append([]string{"rev-list", "--topo-order", "--reverse"}, refGlobs...)
	out, _, err := git.Run(ctx, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing commits: %w", err)
	}

	shas := splitNonEmpty(out)
	shaMap := make(map[string]string, len(shas))
	nameChanged := 0

	for _, sha := range shas {
		info, err := git.ParseCommit(ctx, sha)
		if err != nil {
			return nil, 0, fmt.Errorf("parsing commit %s: %w", sha, err)
		}

		// Remap parent SHAs through earlier rewrites.
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

		// Check if this commit matches the rewrite criteria.
		author := info.Author
		committer := info.Committer
		thisNameChanged := false

		nameMatch := oldName != "" && (author.Name == oldName || committer.Name == oldName)
		emailMatch := oldEmail != "" && (author.Email == oldEmail || committer.Email == oldEmail)

		var commitMatches bool
		if oldName != "" && oldEmail != "" {
			commitMatches = nameMatch && emailMatch // AND when both specified
		} else {
			commitMatches = nameMatch || emailMatch // OR when only one specified
		}

		if commitMatches {
			if oldName != "" && author.Name == oldName {
				author.Name = newName
				thisNameChanged = true
			}
			if oldName != "" && committer.Name == oldName {
				committer.Name = newName
				thisNameChanged = true
			}
			if oldEmail != "" && author.Email == oldEmail {
				author.Email = newEmail
				thisNameChanged = true
			}
			if oldEmail != "" && committer.Email == oldEmail {
				committer.Email = newEmail
				thisNameChanged = true
			}
		}

		if thisNameChanged {
			nameChanged++
		}

		// If nothing changed (no name match and no parent remapped),
		// this commit keeps its original SHA.
		if !thisNameChanged && !parentRemapped {
			shaMap[sha] = sha
			if verbose {
				fmt.Fprintf(os.Stderr, "  %s                   (unchanged)\n", sha[:12])
			}
			continue
		}

		// Create a new commit object with the (potentially updated)
		// author/committer and remapped parents. Tree, message, emails,
		// and dates are preserved exactly.
		newSHA, err := git.CommitTreeWithAuthor(ctx, info.Tree, remappedParents, info.Message, author, committer)
		if err != nil {
			return nil, 0, fmt.Errorf("creating rewritten commit for %s: %w", sha, err)
		}
		shaMap[sha] = newSHA
		if verbose {
			if thisNameChanged {
				fmt.Fprintf(os.Stderr, "  %s -> %s  (name changed)\n", sha[:12], newSHA[:12])
			} else {
				fmt.Fprintf(os.Stderr, "  %s -> %s  (inherited)\n", sha[:12], newSHA[:12])
			}
		}
	}

	return shaMap, nameChanged, nil
}

// updateRefs updates all branch and tag refs to point to rewritten commits.
// For annotated tags, the tag object itself is rewritten if its target commit
// changed or its tagger name matches oldName. Stash refs are skipped.
func updateRefs(ctx context.Context, shaMap map[string]string, oldName, newName, oldEmail, newEmail string, verbose bool) error {
	out, _, err := git.Run(ctx, "for-each-ref", "--format=%(refname) %(objecttype) %(objectname)", "refs/heads/", "refs/tags/", "refs/remotes/")
	if err != nil {
		return fmt.Errorf("listing refs: %w", err)
	}

	lines := splitNonEmpty(out)
	for _, line := range lines {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			continue
		}
		refname, objecttype, objectname := parts[0], parts[1], parts[2]

		// Skip stash refs.
		if strings.HasPrefix(refname, "refs/stash") {
			continue
		}

		// Skip symbolic refs like refs/remotes/origin/HEAD -- updating
		// them with git update-ref would convert them to regular refs.
		if strings.HasPrefix(refname, "refs/remotes/") && strings.HasSuffix(refname, "/HEAD") {
			continue
		}

		switch objecttype {
		case "commit":
			// Branch or lightweight tag pointing directly at a commit.
			newSHA, ok := shaMap[objectname]
			if !ok || newSHA == objectname {
				continue
			}
			if err := git.UpdateRef(ctx, refname, newSHA, objectname); err != nil {
				return fmt.Errorf("updating ref %s: %w", refname, err)
			}
			if verbose {
				fmt.Fprintf(os.Stderr, "  %-20s %s -> %s\n", refname, objectname[:12], newSHA[:12])
			}

		case "tag":
			// Annotated tag object -- rewrite if its target changed or
			// tagger name matches.
			newTagSHA, err := rewriteAnnotatedTag(ctx, objectname, shaMap, oldName, newName, oldEmail, newEmail)
			if err != nil {
				return fmt.Errorf("rewriting annotated tag %s: %w", refname, err)
			}
			if newTagSHA == objectname {
				continue
			}
			if err := git.UpdateRef(ctx, refname, newTagSHA, objectname); err != nil {
				return fmt.Errorf("updating annotated tag ref %s: %w", refname, err)
			}
			if verbose {
				fmt.Fprintf(os.Stderr, "  %-20s %s -> %s\n", refname, objectname[:12], newTagSHA[:12])
			}
		}
	}

	// Handle detached HEAD: if HEAD is not on a branch, update it directly.
	_, _, symErr := git.Run(ctx, "symbolic-ref", "HEAD")
	if symErr != nil {
		// HEAD is detached.
		headSHA, err := git.RevParse(ctx, "HEAD")
		if err != nil {
			return fmt.Errorf("reading detached HEAD: %w", err)
		}
		if newSHA, ok := shaMap[headSHA]; ok && newSHA != headSHA {
			if err := git.UpdateRef(ctx, "HEAD", newSHA, headSHA); err != nil {
				return fmt.Errorf("updating detached HEAD: %w", err)
			}
			if verbose {
				fmt.Fprintf(os.Stderr, "  %-20s %s -> %s\n", "HEAD (detached)", headSHA[:12], newSHA[:12])
			}
		}
	}

	return nil
}

// rewriteAnnotatedTag rewrites an annotated tag object if its target commit
// was remapped or its tagger name matches oldName. Returns the new tag object
// SHA, or the original SHA if nothing changed.
func rewriteAnnotatedTag(ctx context.Context, tagObjectSHA string, shaMap map[string]string, oldName, newName, oldEmail, newEmail string) (string, error) {
	out, _, err := git.Run(ctx, "cat-file", "-p", tagObjectSHA)
	if err != nil {
		return "", fmt.Errorf("reading tag object %s: %w", tagObjectSHA, err)
	}

	// Split into header and body at the first blank line.
	headerEnd := strings.Index(out, "\n\n")
	var headerSection, body string
	if headerEnd < 0 {
		headerSection = out
	} else {
		headerSection = out[:headerEnd]
		body = out[headerEnd+2:]
	}

	lines := strings.Split(headerSection, "\n")
	changed := false

	for i, line := range lines {
		spIdx := strings.IndexByte(line, ' ')
		if spIdx < 0 {
			continue
		}
		key := line[:spIdx]
		val := line[spIdx+1:]

		switch key {
		case "object":
			if newSHA, ok := shaMap[val]; ok && newSHA != val {
				lines[i] = "object " + newSHA
				changed = true
			}
		case "tagger":
			// Format: "Name <email> timestamp timezone"
			// Parse tagger name and email for AND/OR matching.
			ltIdx := strings.LastIndex(val, " <")
			if ltIdx >= 0 {
				taggerName := val[:ltIdx]
				rest := val[ltIdx:] // " <email> timestamp timezone"
				gtIdx := strings.IndexByte(rest, '>')
				var taggerEmail string
				if gtIdx >= 0 {
					taggerEmail = rest[2:gtIdx] // extract email between < and >
				}

				taggerNameMatch := oldName != "" && taggerName == oldName
				taggerEmailMatch := oldEmail != "" && taggerEmail == oldEmail

				var taggerMatches bool
				if oldName != "" && oldEmail != "" {
					taggerMatches = taggerNameMatch && taggerEmailMatch
				} else {
					taggerMatches = taggerNameMatch || taggerEmailMatch
				}

				if taggerMatches {
					newTaggerName := taggerName
					if taggerNameMatch && oldName != "" {
						newTaggerName = newName
					}
					newRest := rest
					if taggerEmailMatch && oldEmail != "" && gtIdx >= 0 {
						newRest = " <" + newEmail + rest[gtIdx:]
					}
					lines[i] = "tagger " + newTaggerName + newRest
					changed = true
				}
			}
		}
	}

	if !changed {
		return tagObjectSHA, nil
	}

	// Reconstruct the full tag object content.
	content := strings.Join(lines, "\n") + "\n\n" + body

	newSHA, _, err := git.RunWithEnvStdin(ctx, nil, []byte(content), "hash-object", "-t", "tag", "-w", "--stdin")
	if err != nil {
		return "", fmt.Errorf("writing rewritten tag object: %w", err)
	}
	return strings.TrimSpace(newSHA), nil
}
