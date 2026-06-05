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
	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--force", "scrub", "--from", initialSHA, "--reason", "test flat scrub", "secret.txt")
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

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--force", "scrub", "--from", initialSHA, "--reason", "test nested scrub", "a/b/secret.txt")
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

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--force", "scrub", "--from", initialSHA, "--reason", "test removal", "secret.txt")
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

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--force", "scrub", "--from", initialSHA, "--reason", "test merge scrub", "secret.txt")
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

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--force", "scrub", "--from", initialSHA, "--reason", "test tag scrub", "secret.txt")
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

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--force", "scrub", "--from", fromSHA, "--reason", "test scope", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	newSHAs := revListReverse(t, dir)

	// Commits 0 (initial), 1, 2 should be UNCHANGED (same SHA)
	for i := 0; i <= 2; i++ {
		if newSHAs[i] != allSHAs[i] {
			t.Errorf("commit %d changed: %s -> %s (should be unchanged)", i, allSHAs[i][:12], newSHAs[i][:12])
		}
	}

	// Commits 3, 4, 5 should be REWRITTEN (different SHA)
	for i := 3; i <= 5; i++ {
		if newSHAs[i] == allSHAs[i] {
			t.Errorf("commit %d not rewritten: %s (should have changed)", i, allSHAs[i][:12])
		}
	}

	// Verify rewritten commits have the scrubbed content
	for i := 3; i <= 5; i++ {
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

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--force", "scrub", "--from", initialSHA, "--reason", "sensitive data leaked", "secret.txt")
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
		if entry["op"] != "scrub" {
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

// TestScrubConfirmationAbort runs scrub without --force and pipes "n" to stdin.
// Verifies the command exits without rewriting anything.
func TestScrubConfirmationAbort(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubEnv, "secret.txt", "sensitive\n", "add secret")

	shas := revListReverse(t, dir)
	initialSHA := shas[0]
	headBefore := revParseHEAD(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("REDACTED\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Run WITHOUT --force, pipe "n" to stdin
	cmd := exec.Command(safegitBin, "scrub", "--from", initialSHA, "--reason", "should abort", "secret.txt")
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

	stdout, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--force", "--dry-run", "scrub", "--from", initialSHA, "--reason", "dry run test", "secret.txt")
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

	_, stderr, code := runSafegitEnv(t, dir, scrubEnv, "--force", "scrub", "--from", initialSHA, "--reason", "test undo reject", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	// Now try to undo -- should fail
	_, stderr, code = runSafegitEnv(t, dir, scrubEnv, "undo")
	if code == 0 {
		t.Fatal("undo after scrub should have failed, but exited 0")
	}
	if !strings.Contains(stderr, "cannot undo scrub") {
		t.Errorf("undo error should contain 'cannot undo scrub', got: %s", stderr)
	}
}
