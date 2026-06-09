package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var scrubMatchEnv = []string{"CLAUDE_CODE_SESSION_ID=scrub-match-test"}

// TestScrubMatchBlobReplace creates 3 files with "SECRET_ABC" across 5 commits.
// Runs scrub match and verifies all files in all commits have "REDACTED".
func TestScrubMatchBlobReplace(t *testing.T) {
	dir := newRepo(t)

	// Create 3 files with SECRET_ABC across 5 commits
	commitFileEnv(t, dir, scrubMatchEnv, "file1.txt", "data SECRET_ABC here\n", "add file1")
	commitFileEnv(t, dir, scrubMatchEnv, "file2.txt", "also SECRET_ABC inside\n", "add file2")
	commitFileEnv(t, dir, scrubMatchEnv, "file3.txt", "contains SECRET_ABC too\n", "add file3")
	commitFileEnv(t, dir, scrubMatchEnv, "file1.txt", "updated SECRET_ABC v2\n", "update file1")
	commitFileEnv(t, dir, scrubMatchEnv, "file2.txt", "updated SECRET_ABC v2\n", "update file2")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test blob replace",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify all files in all commits have REDACTED instead of SECRET_ABC
	shas := revListReverse(t, dir)
	for i, sha := range shas {
		for _, fname := range []string{"file1.txt", "file2.txt", "file3.txt"} {
			content, ok := gitShow(t, dir, sha, fname)
			if !ok {
				continue // file may not exist in early commits
			}
			if strings.Contains(content, "SECRET_ABC") {
				t.Errorf("commit %d (%s): %s still contains SECRET_ABC: %q", i, sha[:12], fname, content)
			}
			if !strings.Contains(content, "REDACTED") {
				t.Errorf("commit %d (%s): %s missing REDACTED: %q", i, sha[:12], fname, content)
			}
		}
	}
}

// TestScrubMatchCommitMessage verifies that scrub match replaces patterns
// in commit messages.
func TestScrubMatchCommitMessage(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "some SECRET_ABC data\n", "added SECRET_ABC key")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test commit message",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Check commit messages
	cmd := exec.Command("git", "log", "--format=%B")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	messages := string(out)
	if strings.Contains(messages, "SECRET_ABC") {
		t.Errorf("commit messages still contain SECRET_ABC: %s", messages)
	}
}

// TestScrubMatchTagAnnotation verifies that scrub match replaces patterns
// in annotated tag messages.
func TestScrubMatchTagAnnotation(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "some SECRET_ABC data\n", "add data")

	// Create annotated tag with SECRET_ABC in the annotation
	cmd := exec.Command("git", "tag", "-a", "v1.0", "-m", "release with SECRET_ABC")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag: %v\n%s", err, out)
	}

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test tag annotation",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Check tag annotation
	cmd = exec.Command("git", "tag", "-l", "-n99", "v1.0")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git tag -l -n99: %v", err)
	}
	tagOutput := string(out)
	if strings.Contains(tagOutput, "SECRET_ABC") {
		t.Errorf("tag annotation still contains SECRET_ABC: %s", tagOutput)
	}
	if !strings.Contains(tagOutput, "REDACTED") {
		t.Errorf("tag annotation does not contain REDACTED: %s", tagOutput)
	}
}

// TestScrubMatchBinarySkipped creates a binary file containing SECRET_ABC
// and verifies it is NOT modified by scrub match (binary blobs are skipped).
func TestScrubMatchBinarySkipped(t *testing.T) {
	dir := newRepo(t)

	// Create a binary file: NUL bytes + SECRET_ABC
	binaryContent := []byte("header\x00\x00\x00binary SECRET_ABC content\x00end")
	binaryPath := filepath.Join(dir, "binary.dat")
	if err := os.WriteFile(binaryPath, binaryContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Also create a text file with the secret so scrub has something to match
	commitFileEnv(t, dir, scrubMatchEnv, "text.txt", "SECRET_ABC\n", "add text file")

	// Commit the binary file using git directly (safegit commit needs it tracked)
	cmd := exec.Command("git", "add", "binary.dat")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "add binary file")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), scrubMatchEnv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	// Record the binary blob SHA before scrub
	cmd = exec.Command("git", "rev-parse", "HEAD:binary.dat")
	cmd.Dir = dir
	blobSHAOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD:binary.dat: %v", err)
	}
	blobSHABefore := strings.TrimSpace(string(blobSHAOut))

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test binary skip",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// The binary file should be unchanged in HEAD
	headSHA := revParseHEAD(t, dir)
	cmd = exec.Command("git", "cat-file", "blob", headSHA+":binary.dat")
	cmd.Dir = dir
	blobContent, err := cmd.Output()
	if err != nil {
		t.Fatalf("reading binary blob after scrub: %v", err)
	}
	if string(blobContent) != string(binaryContent) {
		t.Errorf("binary file was modified by scrub; got %d bytes, want %d bytes", len(blobContent), len(binaryContent))
	}

	// Verify the binary blob's hash is the same (unchanged)
	cmd = exec.Command("git", "rev-parse", headSHA+":binary.dat")
	cmd.Dir = dir
	blobSHAOut, err = cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse after scrub: %v", err)
	}
	blobSHAAfter := strings.TrimSpace(string(blobSHAOut))
	if blobSHAAfter != blobSHABefore {
		t.Errorf("binary blob SHA changed: %s -> %s (should be unchanged)", blobSHABefore, blobSHAAfter)
	}
}

// TestScrubMatchUnreachablePruned creates a commit with SECRET_ABC, amends it
// (creating an unreachable object), runs scrub match, and verifies the old
// unreachable commit is gone from the object store.
func TestScrubMatchUnreachablePruned(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "secret.txt", "SECRET_ABC data\n", "add secret")
	oldSHA := revParseHEAD(t, dir)

	// Amend the commit (creating an unreachable old commit)
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("SECRET_ABC amended\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "secret.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "--amend", "-m", "amended secret")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit --amend: %v\n%s", err, out)
	}

	// Verify the old SHA is still reachable (via reflog) before scrub
	cmd = exec.Command("git", "cat-file", "-e", oldSHA)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("old commit %s should still exist before scrub", oldSHA[:12])
	}

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test unreachable prune",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// The old commit should be gone from the object store
	cmd = exec.Command("git", "cat-file", "-e", oldSHA)
	cmd.Dir = dir
	if err := cmd.Run(); err == nil {
		t.Errorf("old unreachable commit %s still exists after scrub cleanup", oldSHA[:12])
	}
}

// TestScrubMatchSurgicalReflog creates a repo with a secret on main,
// captures pre-rewrite SHAs, runs scrub match, and verifies the old SHAs
// no longer appear in the reflog (cleanup expired them).
func TestScrubMatchSurgicalReflog(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "secret.txt", "SECRET_ABC\n", "add secret on main")
	commitFileEnv(t, dir, scrubMatchEnv, "secret.txt", "SECRET_ABC v2\n", "update secret on main")

	// Capture pre-rewrite SHAs (the secret-containing commits)
	allSHAsBefore := revListReverse(t, dir)
	preScrubSHAs := make(map[string]bool)
	for _, sha := range allSHAsBefore[1:] { // skip seed commit
		preScrubSHAs[sha] = true
	}

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test surgical reflog",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify old (pre-rewrite) SHAs no longer appear in any reflog
	cmd := exec.Command("git", "reflog", "show", "--format=%H", "--all")
	cmd.Dir = dir
	out, _ := cmd.Output()

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

	// Verify the committed history is clean
	allSHAsAfter := revListReverse(t, dir)
	for i, sha := range allSHAsAfter {
		content, ok := gitShow(t, dir, sha, "secret.txt")
		if !ok {
			continue
		}
		if strings.Contains(content, "SECRET_ABC") {
			t.Errorf("commit %d (%s): secret.txt still contains SECRET_ABC", i, sha[:12])
		}
	}
}

// TestScrubMatchStashWarning creates a stash containing SECRET_ABC,
// runs scrub match, and verifies that the verification scan detects the
// secret in stash objects and exits with code 1 (CRITICAL warning).
// Stash blobs are not rewritten because stash commits are on refs/stash,
// not on any branch's ancestry.
func TestScrubMatchStashWarning(t *testing.T) {
	dir := newRepo(t)

	// Create a commit with the secret
	commitFileEnv(t, dir, scrubMatchEnv, "secret.txt", "SECRET_ABC\n", "add secret")

	// Modify the file and stash it
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("SECRET_ABC modified for stash\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "stash")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git stash: %v\n%s", err, out)
	}

	// Verify stash exists
	cmd = exec.Command("git", "stash", "list")
	cmd.Dir = dir
	stashOut, _ := cmd.Output()
	if !strings.Contains(string(stashOut), "stash@{0}") {
		t.Fatal("stash entry not created")
	}

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test stash warning",
		"--entire-history",
	)

	// The command exits 1 because the verification scan detects SECRET_ABC
	// in stash blobs that were not rewritten.
	if code != 1 {
		t.Fatalf("expected exit code 1 (verification failure due to stash), got %d: stdout=%s stderr=%s", code, stdout, stderr)
	}

	// stderr should contain CRITICAL warning about secret still present
	if !strings.Contains(stderr, "CRITICAL") {
		t.Errorf("stderr should contain CRITICAL warning, got: %s", stderr)
	}

	// Verify the committed history is clean despite the stash issue
	shas := revListReverse(t, dir)
	for i, sha := range shas {
		content, ok := gitShow(t, dir, sha, "secret.txt")
		if !ok {
			continue
		}
		if strings.Contains(content, "SECRET_ABC") {
			t.Errorf("commit %d (%s): secret.txt still contains SECRET_ABC", i, sha[:12])
		}
	}
}

// TestScrubMatchEntireHistory creates 5 commits and verifies that
// --entire-history rewrites all of them (all SHAs changed).
func TestScrubMatchEntireHistory(t *testing.T) {
	dir := newRepo(t)

	var originalSHAs []string
	for i := 1; i <= 5; i++ {
		sha := commitFileEnv(t, dir, scrubMatchEnv, "data.txt",
			strings.Repeat("SECRET_ABC ", i)+"\n",
			"commit "+string(rune('0'+i)))
		originalSHAs = append(originalSHAs, sha)
	}

	allSHAsBefore := revListReverse(t, dir)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test entire history",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	allSHAsAfter := revListReverse(t, dir)

	if len(allSHAsAfter) != len(allSHAsBefore) {
		t.Fatalf("commit count changed: %d -> %d", len(allSHAsBefore), len(allSHAsAfter))
	}

	// The seed commit (index 0) doesn't contain SECRET_ABC, but it may still
	// be rewritten because --entire-history includes all commits and descendant
	// commits get new parent SHAs. Skip the seed commit and verify commits 1-5.
	for i := 1; i < len(allSHAsBefore); i++ {
		if allSHAsAfter[i] == allSHAsBefore[i] {
			t.Errorf("commit %d was not rewritten: SHA still %s", i, allSHAsBefore[i][:12])
		}
	}
}

// TestScrubMatchFromScope creates 5 commits where the secret only appears in
// commits 3-5, and uses --from on commit 3 to verify only commits 3-5 are
// rewritten while 1-2 remain unchanged.
func TestScrubMatchFromScope(t *testing.T) {
	dir := newRepo(t)

	// Commits 1-2: no secret (innocent content)
	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "clean content v1\n", "commit 1")
	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "clean content v2\n", "commit 2")

	// Commits 3-5: contain the secret
	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC v3\n", "commit 3")
	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC v4\n", "commit 4")
	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC v5\n", "commit 5")

	allSHAs := revListReverse(t, dir)
	// allSHAs[0] = seed commit, allSHAs[1..5] = our 5 commits

	// Use --from on the 3rd added commit (index 3), so commits 3-5 are rewritten
	fromSHA := allSHAs[3]

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test from scope",
		"--from", fromSHA,
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	newSHAs := revListReverse(t, dir)

	// Commits 0, 1, 2 should be unchanged
	for i := 0; i <= 2; i++ {
		if newSHAs[i] != allSHAs[i] {
			t.Errorf("commit %d changed: %s -> %s (should be unchanged)", i, allSHAs[i][:12], newSHAs[i][:12])
		}
	}

	// Commits 3, 4, 5 should be rewritten
	for i := 3; i <= 5; i++ {
		if newSHAs[i] == allSHAs[i] {
			t.Errorf("commit %d not rewritten: SHA still %s", i, allSHAs[i][:12])
		}
	}

	// Verify rewritten commits have REDACTED instead of SECRET_ABC
	for i := 3; i <= 5; i++ {
		content, ok := gitShow(t, dir, newSHAs[i], "data.txt")
		if !ok {
			t.Errorf("commit %d (%s): data.txt not found", i, newSHAs[i][:12])
			continue
		}
		if strings.Contains(content, "SECRET_ABC") {
			t.Errorf("commit %d (%s): data.txt still contains SECRET_ABC", i, newSHAs[i][:12])
		}
		if !strings.Contains(content, "REDACTED") {
			t.Errorf("commit %d (%s): data.txt missing REDACTED", i, newSHAs[i][:12])
		}
	}
}

// TestScrubMatchIdempotent runs scrub match twice and verifies the second
// run finds no matches and exits cleanly.
func TestScrubMatchIdempotent(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC data\n", "add data")

	// First run
	stdout1, stderr1, code1 := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "idempotent test 1",
		"--entire-history",
	)
	if code1 != 0 {
		t.Fatalf("first scrub match failed (code %d): stdout=%s stderr=%s", code1, stdout1, stderr1)
	}

	headAfterFirst := revParseHEAD(t, dir)

	// Second run
	stdout2, stderr2, code2 := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "idempotent test 2",
		"--entire-history",
	)
	if code2 != 0 {
		t.Fatalf("second scrub match failed (code %d): stdout=%s stderr=%s", code2, stdout2, stderr2)
	}

	combined := stdout2 + stderr2
	if !strings.Contains(combined, "No matches found") {
		t.Errorf("second run should report no matches, got: %s", combined)
	}

	headAfterSecond := revParseHEAD(t, dir)
	if headAfterSecond != headAfterFirst {
		t.Errorf("HEAD changed on second run: %s -> %s (should be unchanged)", headAfterFirst[:12], headAfterSecond[:12])
	}
}

// TestScrubMatchDirtyTreeRejected verifies that scrub match rejects
// a dirty working tree when --force is not used.
func TestScrubMatchDirtyTreeRejected(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC\n", "add data")

	// Make the working tree dirty
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("modified\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Without --force, should fail
	_, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test dirty guard",
		"--entire-history",
	)
	if code == 0 {
		t.Fatal("scrub match without --force on dirty tree should have failed, but exited 0")
	}
	if !strings.Contains(stderr, "working tree is dirty") {
		t.Errorf("error should contain 'working tree is dirty', got: %s", stderr)
	}
}

// TestScrubMatchDryRun verifies --dry-run previews matches without rewriting.
func TestScrubMatchDryRun(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC\n", "add data")
	headBefore := revParseHEAD(t, dir)

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "--dry-run", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test dry run",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match --dry-run failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	combined := stdout + stderr

	// Dry-run should report match information
	if !strings.Contains(combined, "Found") && !strings.Contains(combined, "match") {
		t.Errorf("dry-run output should contain match info, got: %s", combined)
	}

	// HEAD should be unchanged
	headAfter := revParseHEAD(t, dir)
	if headAfter != headBefore {
		t.Errorf("HEAD changed during dry run: %s -> %s", headBefore[:12], headAfter[:12])
	}
}

// TestScrubMatchNoMatches verifies clean exit when pattern matches nothing.
func TestScrubMatchNoMatches(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "normal content\n", "add data")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "NONEXISTENT_PATTERN_XYZ",
		"--replace", "REDACTED",
		"--reason", "test no matches",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match with no matches failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	combined := stdout + stderr
	if !strings.Contains(combined, "No matches found") {
		t.Errorf("should report 'No matches found', got: %s", combined)
	}
}

// TestScrubMatchCoordinationGuard verifies the lock file is created during
// scrub and cleaned up after.
func TestScrubMatchCoordinationGuard(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, scrubMatchEnv, "data.txt", "SECRET_ABC\n", "add data")

	// Run scrub match and check that it succeeds (lock is acquired and released)
	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test coordination",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify the lock file does NOT exist after completion (clean release)
	lockDir := filepath.Join(dir, ".git", "safegit", "locks")
	if _, err := os.Stat(lockDir); err == nil {
		entries, _ := os.ReadDir(lockDir)
		for _, e := range entries {
			if strings.Contains(e.Name(), "rewrite") {
				t.Errorf("lock file %s still exists after scrub completed", e.Name())
			}
		}
	}

	// Verify we can run a second scrub (lock was released properly)
	_, stderr2, code2 := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test coordination 2",
		"--entire-history",
	)
	if code2 != 0 {
		t.Fatalf("second scrub match failed (lock not released?): code %d, stderr=%s", code2, stderr2)
	}
}

// TestScrubMatchScope creates a repo with secret.txt and config/secret.env
// both containing "SECRET_ABC". Runs scrub match with --scope '*.env' and
// verifies only the .env file is scrubbed while secret.txt is left untouched.
func TestScrubMatchScope(t *testing.T) {
	dir := newRepo(t)

	// Create secret.txt and config/secret.env, both containing SECRET_ABC
	commitFileEnv(t, dir, scrubMatchEnv, "secret.txt", "data SECRET_ABC here\n", "add secret.txt")
	commitFileEnv(t, dir, scrubMatchEnv, "config/secret.env", "KEY=SECRET_ABC\n", "add config/secret.env")

	stdout, stderr, code := runSafegitEnv(t, dir, scrubMatchEnv,
		"--yes", "scrub", "match",
		"--pattern", "SECRET_ABC",
		"--replace", "REDACTED",
		"--reason", "test scope",
		"--entire-history",
		"--scope", "*.env",
	)
	if code != 0 {
		t.Fatalf("scrub match --scope failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Verify all commits: config/secret.env should have REDACTED, secret.txt should still have SECRET_ABC
	shas := revListReverse(t, dir)
	for i, sha := range shas {
		// Check config/secret.env is scrubbed
		content, ok := gitShow(t, dir, sha, "config/secret.env")
		if ok {
			if strings.Contains(content, "SECRET_ABC") {
				t.Errorf("commit %d (%s): config/secret.env still contains SECRET_ABC: %q", i, sha[:12], content)
			}
			if !strings.Contains(content, "REDACTED") {
				t.Errorf("commit %d (%s): config/secret.env missing REDACTED: %q", i, sha[:12], content)
			}
		}

		// Check secret.txt is NOT scrubbed (outside scope)
		content, ok = gitShow(t, dir, sha, "secret.txt")
		if ok {
			if !strings.Contains(content, "SECRET_ABC") {
				t.Errorf("commit %d (%s): secret.txt should still contain SECRET_ABC (outside scope): %q", i, sha[:12], content)
			}
			if strings.Contains(content, "REDACTED") {
				t.Errorf("commit %d (%s): secret.txt should NOT contain REDACTED (outside scope): %q", i, sha[:12], content)
			}
		}
	}
}
