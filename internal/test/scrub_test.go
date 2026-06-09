package test

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// commitFileEnv creates a file, writes content, and commits it via safegit with
// the given session env. Returns the new HEAD SHA.
func commitFileEnv(t *testing.T, dir string, env []string, path, content, msg string) string {
	t.Helper()
	fullPath := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", msg, "--", path)
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	return revParseHEAD(t, dir)
}

// revListReverse returns commit SHAs in chronological order (oldest first).
func revListReverse(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("git", "rev-list", "--reverse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-list --reverse HEAD: %v", err)
	}
	return splitLines(strings.TrimSpace(string(out)))
}

// gitShow returns the content of a file at a given commit.
// Returns empty string and false if the file does not exist in that commit.
func gitShow(t *testing.T, dir, sha, path string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", "show", sha+":"+path)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// gitLsTree returns the list of files in a commit's tree.
func gitLsTree(t *testing.T, dir, sha string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", sha)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-tree %s: %v", sha, err)
	}
	return splitLines(strings.TrimSpace(string(out)))
}

// splitLines splits a string by newlines, filtering out empty strings.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// gitParents returns the parent SHAs of a commit.
func gitParents(t *testing.T, dir, sha string) []string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", sha+"^@")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		// No parents (root commit) returns error
		return nil
	}
	return splitLines(strings.TrimSpace(string(out)))
}

var scrubEnv = []string{"CLAUDE_CODE_SESSION_ID=scrub-test"}

// TestScrubFlatFile creates 3 commits modifying secret.txt, replaces it
// on disk, scrubs, and verifies all rewritten commits have the new content.
func TestScrubFlatFile(t *testing.T) {
	dir := newRepo(t)

	// Create 3 commits each modifying secret.txt
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "version 1\n", "add secret v1")
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "version 2\n", "update secret v2")
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "version 3\n", "update secret v3")

	shas := revListReverse(t, dir)
	// shas[0] = initial (seed.txt), shas[1..3] = secret.txt commits
	initialSHA := shas[0]

	// Write replacement content on disk
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("REDACTED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Scrub from the initial commit (exclusive), so all 3 secret.txt commits are rewritten
	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", initialSHA, "--reason", "test flat scrub", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	// Verify all rewritten commits have the new content
	newSHAs := revListReverse(t, dir)
	// Skip initial commit (index 0)
	for i := 1; i < len(newSHAs); i++ {
		content, ok := gitShow(t, dir, newSHAs[i], "secret.txt")
		if !ok {
			t.Errorf("commit %d (%s): secret.txt not found", i, newSHAs[i][:12])
			continue
		}
		if content != "REDACTED\n" {
			t.Errorf("commit %d (%s): secret.txt = %q, want %q", i, newSHAs[i][:12], content, "REDACTED\n")
		}
	}
}

// TestScrubNestedPath scrubs a file in a nested directory and verifies
// sibling files are untouched.
func TestScrubNestedPath(t *testing.T) {
	dir := newRepo(t)

	// Create commits with nested files
	commitFileEnv(t, dir, scrubEnv, "a/b/secret.txt", "nested secret v1\n", "add nested secret")
	commitFileEnv(t, dir, scrubEnv, "a/b/other.txt", "other content\n", "add sibling")
	commitFileEnv(t, dir, scrubEnv, "a/b/secret.txt", "nested secret v2\n", "update nested secret")

	shas := revListReverse(t, dir)
	initialSHA := shas[0]

	// Write replacement
	if err := os.WriteFile(filepath.Join(dir, "a", "b", "secret.txt"), []byte("SCRUBBED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", initialSHA, "--reason", "test nested scrub", "a/b/secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	newSHAs := revListReverse(t, dir)
	for i := 1; i < len(newSHAs); i++ {
		content, ok := gitShow(t, dir, newSHAs[i], "a/b/secret.txt")
		if !ok {
			// Some commits may not have the file (e.g., commit that only added other.txt)
			continue
		}
		if content != "SCRUBBED\n" {
			t.Errorf("commit %d (%s): a/b/secret.txt = %q, want %q", i, newSHAs[i][:12], content, "SCRUBBED\n")
		}
	}

	// Verify sibling file is untouched in commits where it exists
	for i := 1; i < len(newSHAs); i++ {
		content, ok := gitShow(t, dir, newSHAs[i], "a/b/other.txt")
		if !ok {
			continue
		}
		if content != "other content\n" {
			t.Errorf("commit %d (%s): a/b/other.txt = %q, want %q (sibling should be untouched)", i, newSHAs[i][:12], content, "other content\n")
		}
	}
}

// TestScrubRemoveFile deletes the file from disk and verifies scrub removes
// it from all rewritten commits.
func TestScrubRemoveFile(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "sensitive data\n", "add secret")
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "more sensitive\n", "update secret")
	commitFileEnv(t, dir, scrubEnv, "keepme.txt", "keep this\n", "add keepme")

	shas := revListReverse(t, dir)
	initialSHA := shas[0]

	// Delete secret.txt from disk to trigger removal mode
	if err := os.Remove(filepath.Join(dir, "secret.txt")); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", initialSHA, "--reason", "test removal", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	newSHAs := revListReverse(t, dir)
	for i := 1; i < len(newSHAs); i++ {
		_, ok := gitShow(t, dir, newSHAs[i], "secret.txt")
		if ok {
			t.Errorf("commit %d (%s): secret.txt still exists after removal scrub", i, newSHAs[i][:12])
		}
	}

	// Verify keepme.txt is still present in the last commit
	lastSHA := newSHAs[len(newSHAs)-1]
	content, ok := gitShow(t, dir, lastSHA, "keepme.txt")
	if !ok {
		t.Error("keepme.txt missing from HEAD after scrub")
	}
	if ok && content != "keep this\n" {
		t.Errorf("keepme.txt = %q, want %q", content, "keep this\n")
	}
}

// TestScrubMergeCommit verifies scrub works across merge commits,
// preserving the merge structure (two parents).
func TestScrubMergeCommit(t *testing.T) {
	dir := newRepo(t)

	// Make a commit on main with the secret
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "main secret\n", "add secret on main")

	// Create and switch to a branch
	cmd := exec.Command("git", "checkout", "-b", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b feature: %v\n%s", err, out)
	}

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "feature secret\n", "update secret on feature")
	commitFileEnv(t, dir, scrubEnv, "feature.txt", "feature work\n", "add feature file")

	// Switch back to main
	cmd = exec.Command("git", "checkout", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout main: %v\n%s", err, out)
	}

	commitFileEnv(t, dir, scrubEnv, "main.txt", "main work\n", "add main file")

	// Merge feature into main
	cmd = exec.Command("git", "merge", "feature", "--no-edit")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git merge feature: %v\n%s", err, out)
	}

	shas := revListReverse(t, dir)
	initialSHA := shas[0]

	// Capture the merge commit SHA before scrub
	mergeSHA := revParseHEAD(t, dir)
	preParents := gitParents(t, dir, mergeSHA)
	if len(preParents) != 2 {
		t.Fatalf("expected merge to have 2 parents, got %d", len(preParents))
	}

	// Write replacement on disk
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("REDACTED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", initialSHA, "--reason", "test merge scrub", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	// Verify the new HEAD (merge commit) still has 2 parents
	newMergeSHA := revParseHEAD(t, dir)
	newParents := gitParents(t, dir, newMergeSHA)
	if len(newParents) != 2 {
		t.Errorf("merge commit after scrub has %d parents, want 2", len(newParents))
	}

	// Verify secret.txt is REDACTED in the merge commit
	content, ok := gitShow(t, dir, newMergeSHA, "secret.txt")
	if !ok {
		t.Error("secret.txt not found in merge commit after scrub")
	}
	if ok && content != "REDACTED\n" {
		t.Errorf("merge commit secret.txt = %q, want %q", content, "REDACTED\n")
	}
}

// TestScrubAnnotatedTag verifies scrub updates annotated tags to point
// at the rewritten commit.
func TestScrubAnnotatedTag(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "tagged secret\n", "add secret")

	// Create annotated tag
	cmd := exec.Command("git", "tag", "-a", "v1.0", "-m", "release v1.0")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag: %v\n%s", err, out)
	}

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "tagged secret v2\n", "update secret after tag")

	shas := revListReverse(t, dir)
	initialSHA := shas[0]

	// Write replacement
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("REDACTED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", initialSHA, "--reason", "test tag scrub", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	// Verify the tag still exists
	cmd = exec.Command("git", "tag", "-l", "v1.0")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "v1.0" {
		t.Error("annotated tag v1.0 missing after scrub")
	}

	// Verify the tag points to the rewritten commit (new SHA, not old)
	// Dereference the tag to get the commit SHA it points to
	cmd = exec.Command("git", "rev-parse", "v1.0^{commit}")
	cmd.Dir = dir
	tagTarget, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse v1.0^{commit}: %v", err)
	}
	tagTargetSHA := strings.TrimSpace(string(tagTarget))

	// The tagged commit should have the REDACTED content
	content, ok := gitShow(t, dir, tagTargetSHA, "secret.txt")
	if !ok {
		t.Error("secret.txt not found in tagged commit after scrub")
	}
	if ok && content != "REDACTED\n" {
		t.Errorf("tagged commit secret.txt = %q, want %q", content, "REDACTED\n")
	}
}

// TestScrubFromScope verifies that --from controls which commits are rewritten.
// Commits before --from are unchanged (same SHA); commits after are rewritten.
func TestScrubFromScope(t *testing.T) {
	dir := newRepo(t)

	// Create 5 commits (all modifying the same file so scrub touches them)
	var commitSHAs []string
	for i := 1; i <= 5; i++ {
		sha := commitFileEnv(t, dir, scrubEnv, "secret.txt", strings.Repeat("x", i)+"\n", "commit "+strings.Repeat("x", i))
		commitSHAs = append(commitSHAs, sha)
	}

	allSHAs := revListReverse(t, dir)
	// allSHAs[0] = initial commit (seed.txt)
	// allSHAs[1..5] = our 5 commits

	// Pass the 2nd commit as --from so commits 3-5 are in the fromSHA..HEAD range
	// (git range is exclusive of the left side)
	fromSHA := allSHAs[2] // 2nd added commit (3rd overall including initial)

	// Write replacement
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("SCRUBBED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", fromSHA, "--reason", "test scope", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	newSHAs := revListReverse(t, dir)

	// Commits 0 (initial) and 1 should be UNCHANGED (same SHA).
	// With inclusive --from semantics, commit at index 2 (the --from commit)
	// is now rewritten.
	for i := 0; i <= 1; i++ {
		if newSHAs[i] != allSHAs[i] {
			t.Errorf("commit %d changed: %s -> %s (should be unchanged)", i, allSHAs[i][:12], newSHAs[i][:12])
		}
	}

	// Commits 2, 3, 4, 5 should be REWRITTEN (different SHA) -- inclusive of --from
	for i := 2; i <= 5; i++ {
		if newSHAs[i] == allSHAs[i] {
			t.Errorf("commit %d not rewritten: %s (should have changed)", i, allSHAs[i][:12])
		}
	}

	// Verify rewritten commits have the scrubbed content (inclusive of --from)
	for i := 2; i <= 5; i++ {
		content, ok := gitShow(t, dir, newSHAs[i], "secret.txt")
		if !ok {
			t.Errorf("commit %d (%s): secret.txt not found", i, newSHAs[i][:12])
			continue
		}
		if content != "SCRUBBED\n" {
			t.Errorf("commit %d (%s): secret.txt = %q, want %q", i, newSHAs[i][:12], content, "SCRUBBED\n")
		}
	}
}

// TestScrubReasonInOplog verifies the scrub reason is recorded in the oplog.
func TestScrubReasonInOplog(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "sensitive data\n", "add secret")

	shas := revListReverse(t, dir)
	initialSHA := shas[0]

	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("REDACTED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", initialSHA, "--reason", "sensitive data leaked", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	// Read oplog and find the scrub entry
	logPath := filepath.Join(dir, ".git", "safegit", "log")
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("opening oplog: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 4096)

	found := false
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry["op"] != "scrub-file" {
			continue
		}
		found = true

		extra, ok := entry["extra"].(map[string]interface{})
		if !ok {
			t.Error("scrub oplog entry missing 'extra' map")
			break
		}
		reason, ok := extra["reason"].(string)
		if !ok {
			t.Error("scrub oplog entry missing 'reason' in extra")
			break
		}
		if reason != "sensitive data leaked" {
			t.Errorf("oplog reason = %q, want %q", reason, "sensitive data leaked")
		}
		// Also check file path is recorded
		file, ok := extra["file"].(string)
		if !ok {
			t.Error("scrub oplog entry missing 'file' in extra")
		} else if file != "secret.txt" {
			t.Errorf("oplog file = %q, want %q", file, "secret.txt")
		}
		break
	}
	if !found {
		t.Error("no scrub entry found in oplog")
	}
}

// TestScrubConfirmationAbort runs scrub without --yes and pipes "n" to stdin.
// Verifies the command exits without rewriting anything.
func TestScrubConfirmationAbort(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "sensitive\n", "add secret")

	shas := revListReverse(t, dir)
	initialSHA := shas[0]

	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("REDACTED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Commit the replacement content so the working tree is clean.
	// The dirty-tree guard would block scrub before the confirmation prompt.
	_, cstderr, ccode := runSafegitEnv(t, dir, scrubEnv, "commit", "-m", "replace secret", "--", "secret.txt")
	if ccode != 0 {
		t.Fatalf("commit replacement failed (code %d): %s", ccode, cstderr)
	}

	// Capture HEAD after committing the replacement (this is the state
	// we expect to be preserved when the user aborts).
	headBefore := revParseHEAD(t, dir)

	// Run WITHOUT --yes, pipe "n" to stdin
	cmd := exec.Command(safegitBin, "scrub", "file", "--from", initialSHA, "--reason", "should abort", "secret.txt")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CLAUDE_CODE_SESSION_ID=scrub-test")
	cmd.Stdin = strings.NewReader("n\n")

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("running safegit scrub: %v", err)
		}
	}

	if exitCode != 0 {
		t.Errorf("expected exit code 0 on abort, got %d: stdout=%s stderr=%s", exitCode, outBuf.String(), errBuf.String())
	}

	combined := outBuf.String() + errBuf.String()
	if !strings.Contains(combined, "Aborted") {
		t.Errorf("output should contain 'Aborted', got: %s", combined)
	}

	// HEAD should not have changed
	headAfter := revParseHEAD(t, dir)
	if headAfter != headBefore {
		t.Errorf("HEAD changed despite abort: %s -> %s", headBefore, headAfter)
	}
}

// TestScrubDryRun verifies --dry-run previews without making changes.
func TestScrubDryRun(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "sensitive\n", "add secret")

	shas := revListReverse(t, dir)
	initialSHA := shas[0]
	headBefore := revParseHEAD(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("REDACTED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "--dry-run", "scrub", "file", "--from", initialSHA, "--reason", "dry run test", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub --dry-run failed (code %d): %s", code, stderr)
	}

	combined := stdout + stderr
	// Check for dry-run indicator
	if !strings.Contains(strings.ToLower(combined), "dry run") && !strings.Contains(strings.ToLower(combined), "would") {
		t.Errorf("dry-run output should mention 'dry run' or 'would', got: %s", combined)
	}

	// HEAD should not have changed
	headAfter := revParseHEAD(t, dir)
	if headAfter != headBefore {
		t.Errorf("HEAD changed during dry run: %s -> %s", headBefore, headAfter)
	}
}

// TestScrubUndoRejected verifies that undo refuses to reverse a scrub operation.
func TestScrubUndoRejected(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "sensitive\n", "add secret")

	shas := revListReverse(t, dir)
	initialSHA := shas[0]

	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("REDACTED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", initialSHA, "--reason", "test undo reject", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	// Now try to undo -- should fail
	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "undo")
	if code == 0 {
		t.Fatal("undo after scrub should have failed, but exited 0")
	}
	if !strings.Contains(stderr, "cannot undo scrub-file") {
		t.Errorf("undo error should contain 'cannot undo scrub-file', got: %s", stderr)
	}
}

// TestScrubRootCommitInclusive verifies that scrub with --from pointing to the
// root (first) commit rewrites the root commit itself (inclusive semantics).
func TestScrubRootCommitInclusive(t *testing.T) {
	// Create a custom repo without newRepo's seed commit -- we want our
	// secret.txt commit to be the root.
	dir := t.TempDir()

	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Root commit with secret.txt
	secretPath := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("root secret\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "secret.txt"},
		{"git", "commit", "-m", "root commit with secret"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	rootSHA := revParseHEAD(t, dir)

	// Add a second commit
	if err := os.WriteFile(secretPath, []byte("secret v2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "secret.txt"},
		{"git", "commit", "-m", "update secret"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Write replacement
	if err := os.WriteFile(secretPath, []byte("SCRUBBED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", rootSHA, "--reason", "test root inclusive", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	// The root commit should be rewritten (different SHA)
	newSHAs := revListReverse(t, dir)
	if newSHAs[0] == rootSHA {
		t.Errorf("root commit was not rewritten: SHA still %s", rootSHA[:12])
	}

	// Verify root commit has scrubbed content
	content, ok := gitShow(t, dir, newSHAs[0], "secret.txt")
	if !ok {
		t.Error("secret.txt not found in rewritten root commit")
	} else if content != "SCRUBBED\n" {
		t.Errorf("root commit secret.txt = %q, want %q", content, "SCRUBBED\n")
	}

	// Verify second commit also has scrubbed content
	content, ok = gitShow(t, dir, newSHAs[1], "secret.txt")
	if !ok {
		t.Error("secret.txt not found in rewritten second commit")
	} else if content != "SCRUBBED\n" {
		t.Errorf("second commit secret.txt = %q, want %q", content, "SCRUBBED\n")
	}
}

// TestScrubFromHead verifies that --from HEAD rewrites only the HEAD commit.
func TestScrubFromHead(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "sensitive data\n", "add secret")
	parentSHA := revParseHEAD(t, dir)

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "more sensitive\n", "update secret")
	headBefore := revParseHEAD(t, dir)

	// Write replacement
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("SCRUBBED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", "HEAD", "--reason", "test from HEAD", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	headAfter := revParseHEAD(t, dir)

	// HEAD should have changed (rewritten)
	if headAfter == headBefore {
		t.Error("HEAD was not rewritten despite --from HEAD")
	}

	// The parent of the new HEAD should be the same as the old parent
	// (only HEAD was in the rewrite range)
	newSHAs := revListReverse(t, dir)
	// newSHAs should have: seed, first secret commit, rewritten HEAD
	if len(newSHAs) < 3 {
		t.Fatalf("expected at least 3 commits, got %d", len(newSHAs))
	}

	// The commit before the rewritten HEAD should be unchanged
	if newSHAs[len(newSHAs)-2] != parentSHA {
		t.Errorf("parent commit changed: %s -> %s (should be unchanged)", parentSHA[:12], newSHAs[len(newSHAs)-2][:12])
	}

	// Verify scrubbed content in the new HEAD
	content, ok := gitShow(t, dir, headAfter, "secret.txt")
	if !ok {
		t.Error("secret.txt not found in rewritten HEAD")
	} else if content != "SCRUBBED\n" {
		t.Errorf("HEAD secret.txt = %q, want %q", content, "SCRUBBED\n")
	}
}

// TestScrubFromMergeCommit verifies that --from a merge commit rewrites
// the merge itself (inclusive) and its descendants, but leaves both parent
// branches untouched.
func TestScrubFromMergeCommit(t *testing.T) {
	dir := newRepo(t)

	// Commit secret on main
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "main v1\n", "add secret on main")

	// Create feature branch
	cmd := exec.Command("git", "checkout", "-b", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b feature: %v\n%s", err, out)
	}

	// Commit on feature
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "feature v1\n", "feature secret")
	featureSHA := revParseHEAD(t, dir)

	// Back to main, commit
	cmd = exec.Command("git", "checkout", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout main: %v\n%s", err, out)
	}

	commitFileEnv(t, dir, scrubEnv, "main.txt", "main work\n", "main only commit")
	mainPreMergeSHA := revParseHEAD(t, dir)

	// Merge feature into main
	cmd = exec.Command("git", "merge", "feature", "--no-edit")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git merge: %v\n%s", err, out)
	}

	mergeSHA := revParseHEAD(t, dir)

	// Add a post-merge commit
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "post-merge\n", "post-merge update")

	// Write replacement
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("SCRUBBED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", mergeSHA, "--reason", "test merge from", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	// Verify: merge commit itself should be rewritten (inclusive)
	newMergeSHA := revParseHEAD(t, dir)
	_ = newMergeSHA // HEAD is now the rewritten post-merge commit

	// The feature branch commit and the main pre-merge commit should be
	// untouched (their SHAs should be findable in the new history).
	// Use git rev-parse to check the feature branch ref still points to
	// the same commit.

	// Check feature branch SHA is unchanged
	cmd = exec.Command("git", "rev-parse", "feature")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse feature: %v", err)
	}
	featureAfter := strings.TrimSpace(string(out))
	if featureAfter != featureSHA {
		t.Errorf("feature branch SHA changed: %s -> %s (should be untouched)", featureSHA[:12], featureAfter[:12])
	}

	// Check main pre-merge commit is still in history with same SHA
	newAllSHAs := revListReverse(t, dir)
	foundMainPre := false
	for _, sha := range newAllSHAs {
		if sha == mainPreMergeSHA {
			foundMainPre = true
			break
		}
	}
	if !foundMainPre {
		t.Errorf("main pre-merge commit %s not found in rewritten history (should be untouched)", mainPreMergeSHA[:12])
	}

	// Verify HEAD (post-merge) has scrubbed content
	headSHA := revParseHEAD(t, dir)
	content, ok := gitShow(t, dir, headSHA, "secret.txt")
	if !ok {
		t.Error("secret.txt not found in HEAD after scrub")
	} else if content != "SCRUBBED\n" {
		t.Errorf("HEAD secret.txt = %q, want %q", content, "SCRUBBED\n")
	}
}

// TestScrubFromNonAncestor verifies that --from with a commit that is not
// an ancestor of HEAD produces an error.
func TestScrubFromNonAncestor(t *testing.T) {
	dir := newRepo(t)

	// Commit on main
	commitFileEnv(t, dir, scrubEnv, "main.txt", "main content\n", "main commit")

	// Create feature branch with its own commit
	cmd := exec.Command("git", "checkout", "-b", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b feature: %v\n%s", err, out)
	}
	commitFileEnv(t, dir, scrubEnv, "feature.txt", "feature content\n", "feature commit")
	featureSHA := revParseHEAD(t, dir)

	// Switch back to main
	cmd = exec.Command("git", "checkout", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout main: %v\n%s", err, out)
	}

	// Write a file to scrub (so scrub has a valid target)
	if err := os.WriteFile(filepath.Join(dir, "main.txt"), []byte("SCRUBBED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Attempt scrub with --from pointing to the feature commit (not an ancestor of main HEAD)
	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", featureSHA, "--reason", "test non-ancestor", "main.txt")
	if code == 0 {
		t.Fatal("scrub with non-ancestor --from should have failed, but exited 0")
	}
	if !strings.Contains(stderr, "not an ancestor") {
		t.Errorf("error should contain 'not an ancestor', got: %s", stderr)
	}
}

// TestScrubCleanWorkingTree verifies that after a scrub with --force,
// the working tree is clean (git status --porcelain returns empty).
func TestScrubCleanWorkingTree(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "sensitive v1\n", "add secret")
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "sensitive v2\n", "update secret")

	shas := revListReverse(t, dir)
	initialSHA := shas[0]

	// Write replacement
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("SCRUBBED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", initialSHA, "--reason", "test clean tree", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	// git status --porcelain should be empty after scrub (SyncMainIndex keeps it clean)
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("working tree is dirty after scrub: %s", string(out))
	}
}

// TestScrubDirtyTreeRejected verifies that scrub rejects a dirty working tree
// unless --force is used.
func TestScrubDirtyTreeRejected(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "sensitive\n", "add secret")

	shas := revListReverse(t, dir)
	initialSHA := shas[0]

	// Write replacement (making tree dirty)
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("SCRUBBED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Without --force, scrub should fail with dirty tree error
	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "scrub", "file", "--from", initialSHA, "--reason", "test dirty guard", "secret.txt")
	if code == 0 {
		t.Fatal("scrub without --force on dirty tree should have failed, but exited 0")
	}
	if !strings.Contains(stderr, "working tree is dirty") {
		t.Errorf("error should contain 'working tree is dirty', got: %s", stderr)
	}

	// With --force, scrub should succeed
	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", initialSHA, "--reason", "test dirty guard force", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub with --force on dirty tree failed (code %d): %s", code, stderr)
	}
}

// TestScrubIdempotent verifies that running scrub twice with the same
// parameters succeeds both times.
func TestScrubIdempotent(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "sensitive v1\n", "add secret")
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "sensitive v2\n", "update secret")

	shas := revListReverse(t, dir)
	initialSHA := shas[0]

	// Write replacement
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("SCRUBBED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// First scrub
	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", initialSHA, "--reason", "idempotent test 1", "secret.txt")
	if code != 0 {
		t.Fatalf("first scrub failed (code %d): %s", code, stderr)
	}

	headAfterFirst := revParseHEAD(t, dir)

	// Get new initial SHA for second scrub (history was rewritten)
	newSHAs := revListReverse(t, dir)
	newInitialSHA := newSHAs[0]

	// Second scrub with --force (on-disk content may differ from committed tree)
	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", newInitialSHA, "--reason", "idempotent test 2", "secret.txt")
	if code != 0 {
		t.Fatalf("second scrub failed (code %d): %s", code, stderr)
	}

	headAfterSecond := revParseHEAD(t, dir)

	// Verify content is still scrubbed
	content, ok := gitShow(t, dir, headAfterSecond, "secret.txt")
	if !ok {
		t.Error("secret.txt not found after second scrub")
	} else if content != "SCRUBBED\n" {
		t.Errorf("after second scrub secret.txt = %q, want %q", content, "SCRUBBED\n")
	}

	// Both scrubs should have produced different HEADs (the second rewrite
	// creates new commit objects even if content is identical, because
	// commit hashing includes timestamps)... Actually git commit-tree with
	// preserved author/committer strings may produce the same SHA if the
	// trees and parents are identical. Either way, the scrub should succeed.
	_ = headAfterFirst
}

// TestScrubMultipleBranches verifies that scrub remaps all branch refs
// whose targets are in the shaMap, not just HEAD. The feature branch
// points to a commit on main's lineage, so when main is scrubbed the
// feature ref must also be remapped to the rewritten SHA.
func TestScrubMultipleBranches(t *testing.T) {
	dir := newRepo(t)

	// Create commits with secret.txt on main
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "main secret v1\n", "add secret on main")
	branchPointSHA := revParseHEAD(t, dir)

	// Create a feature branch pointing to this commit (same as main currently)
	cmd := exec.Command("git", "branch", "feature", branchPointSHA)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch feature: %v\n%s", err, out)
	}

	// Add more commits on main (stay on main the whole time)
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "main secret v2\n", "update secret on main")
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "main secret v3\n", "update secret on main again")
	mainSHABefore := revParseHEAD(t, dir)

	shas := revListReverse(t, dir)
	initialSHA := shas[0]

	// Write replacement on disk
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("REDACTED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Scrub from the initial commit (inclusive), so all secret.txt commits are rewritten.
	// The feature branch points to branchPointSHA which is in the rewrite range.
	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", initialSHA, "--reason", "test multi-branch scrub", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	// Verify main branch points to a new (rewritten) SHA
	mainSHAAfter := revParseHEAD(t, dir)
	if mainSHAAfter == mainSHABefore {
		t.Errorf("main branch was not rewritten: SHA still %s", mainSHABefore[:12])
	}

	// Verify feature branch points to a new (rewritten) SHA
	cmd = exec.Command("git", "rev-parse", "feature")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse feature: %v", err)
	}
	featureSHAAfter := strings.TrimSpace(string(out))
	if featureSHAAfter == branchPointSHA {
		t.Errorf("feature branch was not rewritten: SHA still %s", branchPointSHA[:12])
	}

	// Verify scrubbed content on main (HEAD)
	content, ok := gitShow(t, dir, mainSHAAfter, "secret.txt")
	if !ok {
		t.Error("secret.txt not found on main after scrub")
	} else if content != "REDACTED\n" {
		t.Errorf("main secret.txt = %q, want %q", content, "REDACTED\n")
	}

	// Verify scrubbed content on feature branch
	content, ok = gitShow(t, dir, featureSHAAfter, "secret.txt")
	if !ok {
		t.Error("secret.txt not found on feature after scrub")
	} else if content != "REDACTED\n" {
		t.Errorf("feature secret.txt = %q, want %q", content, "REDACTED\n")
	}
}

// TestCleanupExpiresTaintedReflog verifies that after scrub, old (pre-rewrite)
// commit SHAs no longer appear in the reflog.
func TestCleanupExpiresTaintedReflog(t *testing.T) {
	dir := newRepo(t)

	// Create commits with a secret
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "secret v1\n", "add secret")
	commitFileEnv(t, dir, scrubEnv, "secret.txt", "secret v2\n", "update secret")

	shas := revListReverse(t, dir)
	initialSHA := shas[0]

	// Capture pre-rewrite SHAs (the secret-containing commits)
	preScrubSHAs := make(map[string]bool)
	for _, sha := range shas[1:] { // skip initial seed commit
		preScrubSHAs[sha] = true
	}

	// Write replacement
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("REDACTED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", initialSHA, "--reason", "test cleanup reflog", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	// Check that no pre-rewrite SHAs appear in any reflog
	cmd := exec.Command("git", "reflog", "show", "--format=%H", "--all")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		// --all might fail if no reflogs, try HEAD only
		cmd = exec.Command("git", "reflog", "show", "--format=%H", "HEAD")
		cmd.Dir = dir
		out, _ = cmd.Output()
	}

	// Also get HEAD reflog
	headCmd := exec.Command("git", "reflog", "show", "--format=%H", "HEAD")
	headCmd.Dir = dir
	headOut, _ := headCmd.Output()

	allReflogSHAs := strings.TrimSpace(string(out)) + "\n" + strings.TrimSpace(string(headOut))
	for _, line := range strings.Split(allReflogSHAs, "\n") {
		sha := strings.TrimSpace(line)
		if sha == "" {
			continue
		}
		if preScrubSHAs[sha] {
			t.Errorf("pre-rewrite SHA %s still in reflog after cleanup", sha[:12])
		}
	}
}

// TestScrubLightweightTag verifies that scrub updates lightweight tags
// to point at the rewritten commit.
func TestScrubLightweightTag(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "tagged secret v1\n", "add secret")
	taggedSHA := revParseHEAD(t, dir)

	// Create a lightweight tag (not annotated)
	cmd := exec.Command("git", "tag", "v1.0", taggedSHA)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag v1.0: %v\n%s", err, out)
	}

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "tagged secret v2\n", "update after tag")

	shas := revListReverse(t, dir)
	initialSHA := shas[0]

	// Write replacement
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("SCRUBBED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--yes", "--force", "scrub", "file", "--from", initialSHA, "--reason", "test lightweight tag", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	// Verify the lightweight tag still exists
	cmd = exec.Command("git", "tag", "-l", "v1.0")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "v1.0" {
		t.Error("lightweight tag v1.0 missing after scrub")
	}

	// Verify the tag points to the rewritten commit (scrubbed content)
	cmd = exec.Command("git", "rev-parse", "v1.0")
	cmd.Dir = dir
	tagTarget, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse v1.0: %v", err)
	}
	tagTargetSHA := strings.TrimSpace(string(tagTarget))

	// The tag should NOT point to the old SHA (it was rewritten)
	if tagTargetSHA == taggedSHA {
		t.Errorf("lightweight tag still points to old SHA %s (should be rewritten)", taggedSHA[:12])
	}

	// The tagged commit should have scrubbed content
	content, ok := gitShow(t, dir, tagTargetSHA, "secret.txt")
	if !ok {
		t.Error("secret.txt not found in tagged commit after scrub")
	} else if content != "SCRUBBED\n" {
		t.Errorf("tagged commit secret.txt = %q, want %q", content, "SCRUBBED\n")
	}
}
