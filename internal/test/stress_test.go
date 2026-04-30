package test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// verifyAllFilesInTree checks that every file in the list is present in HEAD's tree.
func verifyAllFilesInTree(t *testing.T, dir string, n int, prefix string) {
	t.Helper()
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	fileSet := make(map[string]bool)
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fileSet[f] = true
	}
	missing := 0
	for i := 0; i < n; i++ {
		fname := fmt.Sprintf("%s%03d.txt", prefix, i)
		if !fileSet[fname] {
			t.Errorf("file %s not found in HEAD tree", fname)
			missing++
		}
	}
	if missing > 0 {
		t.Errorf("%d/%d files missing from HEAD tree", missing, n)
	}
}

// verifyLinearHistory checks that the last n commits each have exactly 1 parent.
func verifyLinearHistory(t *testing.T, dir, ref string, n int) {
	t.Helper()
	cmd := exec.Command("git", "log", "--format=%H %P", fmt.Sprintf("-%d", n), ref)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			t.Errorf("commit %s has %d parents (expected 1): %q", parts[0][:8], len(parts)-1, line)
		}
	}
}

// runParallelCommits creates n files and commits them in parallel, returning results.
func runParallelCommits(t *testing.T, dir string, n int, prefix string) (codes []int) {
	t.Helper()

	for i := 0; i < n; i++ {
		fname := fmt.Sprintf("%s%03d.txt", prefix, i)
		content := fmt.Sprintf("content %s %d\n", prefix, i)
		if err := os.WriteFile(filepath.Join(dir, fname), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	codes = make([]int, n)
	stdouts := make([]string, n)
	stderrs := make([]string, n)

	parallel(n, func(i int) {
		fname := fmt.Sprintf("%s%03d.txt", prefix, i)
		msg := fmt.Sprintf("commit %s %d", prefix, i)
		stdout, stderr, code := runSafegit(t, dir, "commit", "-m", msg, "--", fname)
		stdouts[i] = stdout
		stderrs[i] = stderr
		codes[i] = code
	})

	failCount := 0
	for i := 0; i < n; i++ {
		if codes[i] != 0 {
			failCount++
			t.Errorf("commit %d failed (code %d): stdout=%s stderr=%s", i, codes[i], stdouts[i], stderrs[i])
		}
	}
	if failCount > 0 {
		t.Fatalf("%d/%d commits failed", failCount, n)
	}

	return codes
}

// Stress50: 50 parallel commits to main, each with its own file.
// Verifies all 50 land, all files present, linear history.
func TestStress50(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}
	dir := newRepo(t)

	const N = 50
	beforeCount := gitLog(t, dir, "main")

	runParallelCommits(t, dir, N, "s50_")

	afterCount := gitLog(t, dir, "main")
	if got := afterCount - beforeCount; got != N {
		t.Errorf("expected %d new commits, got %d", N, got)
	}

	verifyAllFilesInTree(t, dir, N, "s50_")
	verifyLinearHistory(t, dir, "main", N)
}

// Stress100: 100 parallel commits to main.
func TestStress100(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}
	dir := newRepo(t)

	const N = 100
	beforeCount := gitLog(t, dir, "main")

	runParallelCommits(t, dir, N, "s100_")

	afterCount := gitLog(t, dir, "main")
	if got := afterCount - beforeCount; got != N {
		t.Errorf("expected %d new commits, got %d", N, got)
	}

	verifyAllFilesInTree(t, dir, N, "s100_")
	verifyLinearHistory(t, dir, "main", N)
}

// T12: Raw-git bypass detection. Commit via raw git, then verify doctor
// reports the bypass in its JSON output.
func TestRawGitBypassDetection(t *testing.T) {
	dir := newRepo(t)

	// First, make a safegit commit so the oplog has an entry for this ref
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("tracked\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "safegit commit", "--", "tracked.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	// Now commit via raw git (bypassing safegit)
	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "raw bypass")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("raw git commit failed: %v\n%s", err, out)
	}

	// Run safegit doctor
	stdout, stderr, code := runSafegit(t, dir, "--format", "json", "doctor")
	if code != 0 {
		t.Fatalf("doctor failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	r := parseJSON(t, stdout)
	if !r.OK {
		t.Fatalf("doctor returned ok=false: %s", stdout)
	}

	// Parse the checks array and find bypass_detect
	var checks []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(r.Data, &checks); err != nil {
		t.Fatalf("parsing doctor data: %v", err)
	}

	found := false
	for _, c := range checks {
		if c.Name == "bypass_detect" {
			found = true
			if c.Status != "warn" {
				t.Errorf("bypass_detect status = %q, want 'warn'", c.Status)
			}
			if !strings.Contains(c.Detail, "diverged") {
				t.Errorf("bypass_detect detail = %q, want to contain 'diverged'", c.Detail)
			}
		}
	}
	if !found {
		t.Error("bypass_detect check not found in doctor output")
	}
}

// T16: Pull refused when wip locks exist.
func TestPullRefusedWithWip(t *testing.T) {
	dir := newRepo(t)

	// Modify seed.txt and create a wip
	seedPath := filepath.Join(dir, "seed.txt")
	if err := os.WriteFile(seedPath, []byte("wip content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegit(t, dir, "wip", "seed.txt")
	if code != 0 {
		t.Fatalf("wip failed (code %d): %s", code, stderr)
	}

	// safegit pull should refuse with code 5 (wip locks count as dirty)
	_, _, code = runSafegit(t, dir, "pull")
	if code != 5 {
		t.Errorf("expected exit code 5 (dirty/wip), got %d", code)
	}
}

// T17: Checkout succeeds when working tree is clean and no wips.
func TestCheckoutCleanProceeds(t *testing.T) {
	dir := newRepo(t)

	// Create another branch
	cmd := exec.Command("git", "branch", "other")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v\n%s", err, out)
	}

	// Working tree is clean, no wips -- checkout should succeed
	_, stderr, code := runSafegit(t, dir, "checkout", "other")
	if code != 0 {
		t.Fatalf("checkout failed (code %d): %s", code, stderr)
	}

	// Verify HEAD moved
	headCmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	headCmd.Dir = dir
	headOut, _ := headCmd.Output()
	if strings.TrimSpace(string(headOut)) != "other" {
		t.Errorf("HEAD = %q, want 'other'", strings.TrimSpace(string(headOut)))
	}
}

// T11: Hook timeout. Install a sleeping hook, set short timeout, verify push aborts.
func TestHookTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}
	dir := newRepo(t)

	// Install a hook that sleeps forever
	hookDir := filepath.Join(dir, ".git", "hooks")
	hookPath := filepath.Join(hookDir, "pre-pre-push")
	hookContent := "#!/bin/sh\nsleep 60\n"
	if err := os.WriteFile(hookPath, []byte(hookContent), 0755); err != nil {
		t.Fatal(err)
	}

	// Set a very short timeout (2 seconds)
	runSafegit(t, dir, "config", "hooks.preprepush.timeoutSeconds", "2")

	// Set up a bare remote so push has something to target
	remoteDir := t.TempDir()
	cmd := exec.Command("git", "init", "--bare", remoteDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "remote", "add", "origin", remoteDir)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	// Push should abort with hook timeout (exit code 21)
	_, _, code := runSafegit(t, dir, "push", "origin", "main")
	if code != 21 {
		t.Errorf("expected exit code 21 (hook timeout), got %d", code)
	}
}

// StressDifferentBranches: 50 parallel commits each to their own branch.
// No lock contention expected (different refs -> different locks).
func TestStressDifferentBranches(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}
	dir := newRepo(t)

	const N = 50

	// Create N branches and N files
	for i := 0; i < N; i++ {
		branchName := fmt.Sprintf("br%03d", i)
		cmd := exec.Command("git", "branch", branchName)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git branch %s: %v\n%s", branchName, err, out)
		}

		fname := fmt.Sprintf("br%03d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, fname), []byte(fmt.Sprintf("branch %d\n", i)), 0644); err != nil {
			t.Fatal(err)
		}
	}

	codes := make([]int, N)
	parallel(N, func(i int) {
		branchName := fmt.Sprintf("br%03d", i)
		fname := fmt.Sprintf("br%03d.txt", i)
		msg := fmt.Sprintf("commit to %s", branchName)
		_, _, code := runSafegit(t, dir, "commit", "-m", msg, "--branch", branchName, "--", fname)
		codes[i] = code
	})

	for i := 0; i < N; i++ {
		if codes[i] != 0 {
			t.Errorf("commit %d to br%03d failed (code %d)", i, i, codes[i])
		}
	}

	// Each branch should have exactly 2 commits (initial + new)
	for i := 0; i < N; i++ {
		branchName := fmt.Sprintf("br%03d", i)
		count := gitLog(t, dir, "refs/heads/"+branchName)
		if count != 2 {
			t.Errorf("branch %s has %d commits, expected 2", branchName, count)
		}
	}
}

// T6: Two agents commit sequentially to main, then both push to a bare remote
// concurrently. Both should succeed (same branch tip, linear history).
func TestConcurrentPush(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping push test in short mode")
	}
	dir := newRepo(t)

	// Create bare remote
	remoteDir := t.TempDir()
	cmd := exec.Command("git", "init", "--bare", remoteDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "remote", "add", "origin", remoteDir)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	// Agent A commits file_a.txt
	if err := os.WriteFile(filepath.Join(dir, "file_a.txt"), []byte("agent A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "agent A commit", "--", "file_a.txt")
	if code != 0 {
		t.Fatalf("agent A commit failed (code %d): %s", code, stderr)
	}

	// Agent B commits file_b.txt (sequentially, so both on main)
	if err := os.WriteFile(filepath.Join(dir, "file_b.txt"), []byte("agent B\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "agent B commit", "--", "file_b.txt")
	if code != 0 {
		t.Fatalf("agent B commit failed (code %d): %s", code, stderr)
	}

	// Both push concurrently
	codes := make([]int, 2)
	stderrs := make([]string, 2)
	parallel(2, func(i int) {
		_, se, c := runSafegit(t, dir, "push", "origin", "main")
		codes[i] = c
		stderrs[i] = se
	})

	// At least one must succeed; both pushing the same tip should both succeed
	successes := 0
	for i := 0; i < 2; i++ {
		if codes[i] == 0 {
			successes++
		}
	}
	if successes == 0 {
		t.Fatalf("both pushes failed: agent0 code=%d stderr=%s | agent1 code=%d stderr=%s",
			codes[0], stderrs[0], codes[1], stderrs[1])
	}

	// Verify remote has both commits (file_a.txt and file_b.txt in HEAD tree)
	cmd = exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	cmd.Dir = remoteDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-tree on remote: %v", err)
	}
	tree := string(out)
	for _, f := range []string{"file_a.txt", "file_b.txt"} {
		if !strings.Contains(tree, f) {
			t.Errorf("remote missing %s in HEAD tree", f)
		}
	}
}

// T7: Two agents push different branches concurrently to the same bare remote.
// No contention (different refs), both should succeed.
func TestConcurrentDifferentBranchPush(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping push test in short mode")
	}
	dir := newRepo(t)

	// Create bare remote and add as origin
	remoteDir := t.TempDir()
	cmd := exec.Command("git", "init", "--bare", remoteDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "remote", "add", "origin", remoteDir)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	// Create branch_a and branch_b
	for _, br := range []string{"branch_a", "branch_b"} {
		cmd = exec.Command("git", "branch", br)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git branch %s: %v\n%s", br, err, out)
		}
	}

	// Commit a file to each branch via safegit
	if err := os.WriteFile(filepath.Join(dir, "br_a.txt"), []byte("branch A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "branch_a commit", "--branch", "branch_a", "--", "br_a.txt")
	if code != 0 {
		t.Fatalf("branch_a commit failed (code %d): %s", code, stderr)
	}

	if err := os.WriteFile(filepath.Join(dir, "br_b.txt"), []byte("branch B\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "branch_b commit", "--branch", "branch_b", "--", "br_b.txt")
	if code != 0 {
		t.Fatalf("branch_b commit failed (code %d): %s", code, stderr)
	}

	// Push both branches concurrently
	branches := []string{"branch_a", "branch_b"}
	codes := make([]int, 2)
	stderrs := make([]string, 2)
	parallel(2, func(i int) {
		_, se, c := runSafegit(t, dir, "push", "origin", branches[i])
		codes[i] = c
		stderrs[i] = se
	})

	// Both should succeed (different refs, no contention)
	for i, br := range branches {
		if codes[i] != 0 {
			t.Errorf("push %s failed (code %d): %s", br, codes[i], stderrs[i])
		}
	}

	// Verify remote has both branches
	cmd = exec.Command("git", "branch")
	cmd.Dir = remoteDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch on remote: %v", err)
	}
	remoteBranches := string(out)
	for _, br := range branches {
		if !strings.Contains(remoteBranches, br) {
			t.Errorf("remote missing branch %s", br)
		}
	}
}

// T9: One agent holds a lock for 500ms (live PID), another tries to commit.
// The commit should poll and succeed once the lock is released.
func TestPollingWakeupLatency(t *testing.T) {
	dir := newRepo(t)

	// Create a file to commit
	if err := os.WriteFile(filepath.Join(dir, "poll.txt"), []byte("poll test\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a lock file with the CURRENT process PID (alive, so not stale)
	lockDir := filepath.Join(dir, ".git", "safegit", "locks", "refs", "heads")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatal(err)
	}
	lockFile := filepath.Join(lockDir, "main.lock")
	lockContent := fmt.Sprintf("pid=%d\nts=%s\nop=commit\nhost=test\n",
		os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(lockFile, []byte(lockContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Start a goroutine that removes the lock after 500ms
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Remove(lockFile)
	}()

	// Run safegit commit -- it will poll waiting for the lock
	start := time.Now()
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "after poll", "--", "poll.txt")
	elapsed := time.Since(start)

	if code != 0 {
		t.Fatalf("commit failed (code %d) after polling: %s", code, stderr)
	}

	// Should complete within 2s (500ms lock hold + polling overhead)
	if elapsed > 2*time.Second {
		t.Errorf("commit took %v, expected < 2s (lock held for 500ms)", elapsed)
	}

	// Should have taken at least ~500ms (lock was held that long)
	if elapsed < 400*time.Millisecond {
		t.Errorf("commit took %v, suspiciously fast (lock should have been held 500ms)", elapsed)
	}
}

// T13: Make .git/objects read-only, verify commit fails, restore and verify recovery.
func TestDiskFullSimulated(t *testing.T) {
	dir := newRepo(t)

	objectsDir := filepath.Join(dir, ".git", "objects")

	// Always restore permissions, even if the test panics
	t.Cleanup(func() {
		// Restore write permissions recursively (objects dir + subdirs)
		filepath.Walk(objectsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				os.Chmod(path, 0755)
			}
			return nil
		})
	})

	// Create a file to commit
	if err := os.WriteFile(filepath.Join(dir, "diskfull.txt"), []byte("test content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Make .git/objects read-only (prevents git from writing new objects)
	if err := os.Chmod(objectsDir, 0555); err != nil {
		t.Fatalf("chmod objects dir: %v", err)
	}

	// Commit should fail (can't write objects)
	_, _, code := runSafegit(t, dir, "commit", "-m", "should fail", "--", "diskfull.txt")
	if code == 0 {
		t.Error("commit succeeded with read-only .git/objects, expected failure")
	}

	// Restore permissions
	if err := os.Chmod(objectsDir, 0755); err != nil {
		t.Fatalf("restoring objects dir permissions: %v", err)
	}

	// Now commit should succeed
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "after recovery", "--", "diskfull.txt")
	if code != 0 {
		t.Errorf("commit failed after restoring permissions (code %d): %s", code, stderr)
	}

	// Verify the file is in HEAD tree
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "diskfull.txt") {
		t.Error("diskfull.txt not found in HEAD tree after recovery")
	}
}

// StressOpLog: 50 parallel commits, verify oplog has exactly 50 commit entries,
// all valid JSON, no partial or interleaved lines.
func TestStressOpLog(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}
	dir := newRepo(t)

	const N = 50
	runParallelCommits(t, dir, N, "oplog_")

	// Read and verify the op log
	logPath := filepath.Join(dir, ".git", "safegit", "log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	commitEntries := 0
	for i, line := range lines {
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i+1, err)
			continue
		}
		if entry["op"] == "commit" {
			commitEntries++
		}
	}
	if commitEntries != N {
		t.Errorf("expected %d commit entries in oplog, got %d", N, commitEntries)
	}
}
