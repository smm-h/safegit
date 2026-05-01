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

	// At least one must succeed; the other may fail with "reference already
	// exists" due to the bare remote's own ref locking under concurrency.
	successes := 0
	for i := 0; i < 2; i++ {
		if codes[i] == 0 {
			successes++
		}
	}
	if successes == 0 {
		t.Fatalf("both pushes failed: [0] code=%d stderr=%s [1] code=%d stderr=%s",
			codes[0], stderrs[0], codes[1], stderrs[1])
	}

	// If only one succeeded, push again to land the second
	if successes == 1 {
		_, _, code = runSafegit(t, dir, "push", "origin", "main")
		if code != 0 {
			t.Fatalf("retry push failed (code %d)", code)
		}
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
	hn1, _ := os.Hostname()
	lockContent := fmt.Sprintf("pid=%d\nts=%s\nop=commit\nhost=%s\n",
		os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano), hn1)
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

func TestCrossBranchCommit(t *testing.T) {
	dir := newRepo(t)

	// Create a feature branch
	cmd := exec.Command("git", "branch", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v\n%s", err, out)
	}

	// Write a file and commit to feature branch while on main
	if err := os.WriteFile(filepath.Join(dir, "cross.txt"), []byte("cross\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "cross-branch commit", "--branch", "feature", "--", "cross.txt")
	if code != 0 {
		t.Fatalf("cross-branch commit failed (code %d): %s", code, stderr)
	}

	// Verify we're still on main
	headCmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	headCmd.Dir = dir
	headOut, _ := headCmd.Output()
	if strings.TrimSpace(string(headOut)) != "main" {
		t.Errorf("HEAD moved to %s, expected to stay on main", strings.TrimSpace(string(headOut)))
	}

	// Verify the file is in feature's tree
	cmd = exec.Command("git", "ls-tree", "-r", "--name-only", "feature")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "cross.txt") {
		t.Error("cross.txt not found in feature branch tree")
	}

	// Verify feature has 2 commits (initial + cross-branch)
	count := gitLog(t, dir, "feature")
	if count != 2 {
		t.Errorf("feature has %d commits, expected 2", count)
	}
}

func TestBisectGuardAndHelp(t *testing.T) {
	dir := newRepo(t)

	// Bare bisect (no args) should print help, not be blocked by guard
	_, _, code := runSafegit(t, dir, "bisect")
	// git bisect with no args prints help and exits 1
	// The important thing is it doesn't exit 5 (coord guard refusal)
	if code == 5 {
		t.Error("bare 'safegit bisect' should not trigger coordination guard")
	}
}

func TestCommitMessageFromFile(t *testing.T) {
	dir := newRepo(t)

	// Write message to a file
	msgPath := filepath.Join(dir, "commit-msg.txt")
	if err := os.WriteFile(msgPath, []byte("message from file\n\ndetailed body"), 0644); err != nil {
		t.Fatal(err)
	}

	// Write a file to commit
	if err := os.WriteFile(filepath.Join(dir, "ftest.txt"), []byte("data\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runSafegit(t, dir, "commit", "-F", msgPath, "--", "ftest.txt")
	if code != 0 {
		t.Fatalf("commit -F failed (code %d): %s", code, stderr)
	}

	// Verify commit message
	cmd := exec.Command("git", "log", "-1", "--format=%s", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "message from file" {
		t.Errorf("subject = %q, want 'message from file'", strings.TrimSpace(string(out)))
	}
}

func TestConfigOverride(t *testing.T) {
	dir := newRepo(t)

	// Write a custom config with different casMaxAttempts
	customCfg := filepath.Join(dir, "custom-config.json")
	if err := os.WriteFile(customCfg, []byte(`{"schemaVersion":1,"commit":{"casMaxAttempts":99},"lock":{"acquireTimeoutSeconds":30},"hooks":{"preprepush":{"timeoutSeconds":1800}},"push":{"retryAttempts":3},"log":{"maxSizeMB":100}}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Read the config via --config flag
	stdout, _, code := runSafegit(t, dir, "--format", "json", "--config", customCfg, "config", "commit.casMaxAttempts")
	if code != 0 {
		t.Fatalf("config read failed (code %d)", code)
	}

	r := parseJSON(t, stdout)
	if !r.OK {
		t.Fatalf("config returned ok=false: %s", stdout)
	}

	var data map[string]interface{}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatal(err)
	}
	if val, ok := data["commit.casMaxAttempts"].(float64); !ok || val != 99 {
		t.Errorf("casMaxAttempts = %v, want 99", data["commit.casMaxAttempts"])
	}
}

func TestCoordinationBypassedOplog(t *testing.T) {
	dir := newRepo(t)

	// Dirty the working tree
	seedPath := filepath.Join(dir, "seed.txt")
	if err := os.WriteFile(seedPath, []byte("dirty\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a branch to checkout to
	cmd := exec.Command("git", "branch", "other")
	cmd.Dir = dir
	cmd.CombinedOutput()

	// Checkout with --force (should bypass coord guard and log it)
	_, _, code := runSafegit(t, dir, "--force", "checkout", "other")
	if code != 0 {
		t.Fatalf("forced checkout failed (code %d)", code)
	}

	// Read oplog and find coordination_bypassed entry
	logPath := filepath.Join(dir, ".git", "safegit", "log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "coordination_bypassed") {
		t.Error("oplog missing coordination_bypassed entry after forced checkout")
	}
}

func TestLockRecoveredOplog(t *testing.T) {
	dir := newRepo(t)

	// Write a file to commit
	if err := os.WriteFile(filepath.Join(dir, "recover.txt"), []byte("data\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Start+kill a process to get a dead PID
	helper := exec.Command("sleep", "60")
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	deadPID := helper.Process.Pid
	helper.Process.Kill()
	helper.Wait()

	// Plant a stale lock
	lockDir := filepath.Join(dir, ".git", "safegit", "locks", "refs", "heads")
	os.MkdirAll(lockDir, 0755)
	hn2, _ := os.Hostname()
	lockContent2 := fmt.Sprintf("pid=%d\nts=%s\nop=commit\nhost=%s\n",
		deadPID, time.Now().Add(-1*time.Hour).UTC().Format(time.RFC3339Nano), hn2)
	os.WriteFile(filepath.Join(lockDir, "main.lock"), []byte(lockContent2), 0644)

	// Commit should recover the stale lock
	_, _, code := runSafegit(t, dir, "commit", "-m", "after recovery", "--", "recover.txt")
	if code != 0 {
		t.Fatalf("commit after stale lock failed (code %d)", code)
	}

	// Read oplog and find lock_recovered entry
	logPath := filepath.Join(dir, ".git", "safegit", "log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "lock_recovered") {
		t.Error("oplog missing lock_recovered entry after stale lock recovery")
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

func TestStatusJSON(t *testing.T) {
	dir := newRepo(t)

	// Modify a tracked file and create an untracked file
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("modified\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("new\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stdout, _, code := runSafegit(t, dir, "--format", "json", "status")
	if code != 0 {
		t.Fatalf("status failed (code %d)", code)
	}

	r := parseJSON(t, stdout)
	if !r.OK {
		t.Fatalf("status returned ok=false: %s", stdout)
	}

	var data struct {
		Branch  string `json:"branch"`
		SHA     string `json:"sha"`
		Entries []struct {
			Path   string `json:"path"`
			Status string `json:"status"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("parsing status data: %v", err)
	}

	if data.Branch != "main" {
		t.Errorf("branch = %q, want 'main'", data.Branch)
	}
	if len(data.SHA) != 40 {
		t.Errorf("sha = %q, want 40-char hex", data.SHA)
	}

	// Should have at least 2 entries (modified seed.txt + untracked.txt)
	if len(data.Entries) < 2 {
		t.Errorf("expected at least 2 entries, got %d", len(data.Entries))
	}

	// Check that seed.txt is reported as modified
	found := false
	for _, e := range data.Entries {
		if e.Path == "seed.txt" && e.Status == "modified" {
			found = true
		}
	}
	if !found {
		t.Error("seed.txt not found as 'modified' in status entries")
	}

	// Check that untracked.txt is reported
	found = false
	for _, e := range data.Entries {
		if e.Path == "untracked.txt" && e.Status == "untracked" {
			found = true
		}
	}
	if !found {
		t.Error("untracked.txt not found as 'untracked' in status entries")
	}
}

func TestDiffJSON(t *testing.T) {
	dir := newRepo(t)

	// Modify seed.txt
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("modified content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stdout, _, code := runSafegit(t, dir, "--format", "json", "diff")
	if code != 0 {
		t.Fatalf("diff failed (code %d)", code)
	}

	r := parseJSON(t, stdout)
	if !r.OK {
		t.Fatalf("diff returned ok=false: %s", stdout)
	}

	var data struct {
		Files []struct {
			Path  string `json:"path"`
			Hunks []struct {
				Header string   `json:"header"`
				Lines  []string `json:"lines"`
			} `json:"hunks"`
		} `json:"files"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("parsing diff data: %v", err)
	}

	if len(data.Files) != 1 {
		t.Fatalf("expected 1 file in diff, got %d", len(data.Files))
	}
	if data.Files[0].Path != "seed.txt" {
		t.Errorf("file path = %q, want 'seed.txt'", data.Files[0].Path)
	}
	if len(data.Files[0].Hunks) == 0 {
		t.Error("expected at least 1 hunk")
	}
}

func TestLogJSON(t *testing.T) {
	dir := newRepo(t)

	// Make a safegit commit so we have 2 commits (initial + this one)
	if err := os.WriteFile(filepath.Join(dir, "log.txt"), []byte("log\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runSafegit(t, dir, "commit", "-m", "test log entry", "--", "log.txt")

	stdout, _, code := runSafegit(t, dir, "--format", "json", "log", "-2")
	if code != 0 {
		t.Fatalf("log failed (code %d)", code)
	}

	r := parseJSON(t, stdout)
	if !r.OK {
		t.Fatalf("log returned ok=false: %s", stdout)
	}

	var data struct {
		Entries []struct {
			SHA     string `json:"sha"`
			Author  string `json:"author"`
			Date    string `json:"date"`
			Message string `json:"message"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("parsing log data: %v", err)
	}

	if len(data.Entries) != 2 {
		t.Fatalf("expected 2 log entries, got %d", len(data.Entries))
	}

	// First entry (most recent) should be our commit
	if data.Entries[0].Message != "test log entry" {
		t.Errorf("first entry message = %q, want 'test log entry'", data.Entries[0].Message)
	}
	if len(data.Entries[0].SHA) != 40 {
		t.Errorf("SHA = %q, want 40-char hex", data.Entries[0].SHA)
	}
	if data.Entries[0].Author == "" {
		t.Error("author is empty")
	}
	if data.Entries[0].Date == "" {
		t.Error("date is empty")
	}
}

func TestShowJSON(t *testing.T) {
	dir := newRepo(t)

	// Make a commit with a known file
	if err := os.WriteFile(filepath.Join(dir, "show.txt"), []byte("show\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runSafegit(t, dir, "commit", "-m", "show test commit", "--", "show.txt")

	stdout, _, code := runSafegit(t, dir, "--format", "json", "show", "HEAD")
	if code != 0 {
		t.Fatalf("show failed (code %d)", code)
	}

	r := parseJSON(t, stdout)
	if !r.OK {
		t.Fatalf("show returned ok=false: %s", stdout)
	}

	var data struct {
		SHA     string `json:"sha"`
		Author  string `json:"author"`
		Date    string `json:"date"`
		Message string `json:"message"`
		Files   []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("parsing show data: %v", err)
	}

	if data.Message != "show test commit" {
		t.Errorf("message = %q, want 'show test commit'", data.Message)
	}
	if len(data.SHA) != 40 {
		t.Errorf("SHA = %q, want 40-char hex", data.SHA)
	}
	if len(data.Files) == 0 {
		t.Error("expected at least 1 file in show output")
	}
}

// TestUndo verifies that safegit undo rolls back the last commit, and that
// a second undo with nothing undoable fails.
func TestUndo(t *testing.T) {
	dir := newRepo(t)

	// Record HEAD before the commit
	preCmd := exec.Command("git", "rev-parse", "HEAD")
	preCmd.Dir = dir
	preOut, err := preCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	preSHA := strings.TrimSpace(string(preOut))

	// Create a file and commit it
	if err := os.WriteFile(filepath.Join(dir, "undo.txt"), []byte("undo me\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "commit to undo", "--", "undo.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	// Record HEAD after the commit
	postCmd := exec.Command("git", "rev-parse", "HEAD")
	postCmd.Dir = dir
	postOut, err := postCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	postSHA := strings.TrimSpace(string(postOut))

	if preSHA == postSHA {
		t.Fatal("HEAD did not advance after commit")
	}

	// Run safegit undo
	_, stderr, code = runSafegit(t, dir, "undo")
	if code != 0 {
		t.Fatalf("undo failed (code %d): %s", code, stderr)
	}

	// Verify HEAD rolled back to the pre-commit SHA
	undoneCmd := exec.Command("git", "rev-parse", "HEAD")
	undoneCmd.Dir = dir
	undoneOut, err := undoneCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	undoneSHA := strings.TrimSpace(string(undoneOut))
	if undoneSHA != preSHA {
		t.Errorf("after undo HEAD = %s, want %s (pre-commit)", undoneSHA, preSHA)
	}

	// Verify the file is no longer in HEAD's tree
	treeCmd := exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	treeCmd.Dir = dir
	treeOut, err := treeCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(treeOut), "undo.txt") {
		t.Error("undo.txt still in HEAD tree after undo")
	}

	// Run undo again -- should fail (the last oplog entry is now an undo, not undoable)
	_, _, code = runSafegit(t, dir, "undo")
	if code == 0 {
		t.Error("second undo should have failed, but exited 0")
	}
}

// TestGuardedPassthrough_StashAndTag verifies:
// - tag (unguarded passthrough) works on a clean tree
// - tag -l shows the created tag
// - stash (guarded passthrough) is refused with exit 5 on a dirty tree
func TestGuardedPassthrough_StashAndTag(t *testing.T) {
	dir := newRepo(t)

	// Tag on a clean tree should succeed (tag is not guarded)
	_, stderr, code := runSafegit(t, dir, "tag", "v-test")
	if code != 0 {
		t.Fatalf("tag creation failed (code %d): %s", code, stderr)
	}

	// tag -l should show the tag
	stdout, _, code := runSafegit(t, dir, "tag", "-l")
	if code != 0 {
		t.Fatalf("tag -l failed (code %d)", code)
	}
	if !strings.Contains(stdout, "v-test") {
		t.Errorf("tag -l output %q does not contain 'v-test'", stdout)
	}

	// Dirty the working tree
	seedPath := filepath.Join(dir, "seed.txt")
	if err := os.WriteFile(seedPath, []byte("dirty for stash\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// stash is guarded -- should refuse with exit code 5
	_, _, code = runSafegit(t, dir, "stash")
	if code != 5 {
		t.Errorf("expected exit code 5 (coord guard refusal) for stash on dirty tree, got %d", code)
	}
}

// TestDetachedHEAD_CommitRefused verifies that committing on a detached HEAD
// fails, and that --branch <name> works around the detached state.
func TestDetachedHEAD_CommitRefused(t *testing.T) {
	dir := newRepo(t)

	// Detach HEAD
	cmd := exec.Command("git", "checkout", "--detach", "HEAD")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout --detach: %v\n%s", err, out)
	}

	// Create a file to commit
	if err := os.WriteFile(filepath.Join(dir, "detached.txt"), []byte("detached\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Commit without --branch should fail (detached HEAD)
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "detached commit", "--", "detached.txt")
	if code == 0 {
		t.Fatal("commit on detached HEAD should have failed, but exited 0")
	}
	if !strings.Contains(strings.ToLower(stderr), "detached") {
		// Also check stdout (JSON mode puts errors in stdout envelope)
		// But we ran in human mode, so stderr is the right place.
		t.Errorf("stderr %q does not mention 'detached'", stderr)
	}

	// Commit with --branch main should succeed
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "detached but targeted", "--branch", "main", "--", "detached.txt")
	if code != 0 {
		t.Fatalf("commit with --branch main on detached HEAD failed (code %d): %s", code, stderr)
	}

	// Verify the file landed on main
	treeCmd := exec.Command("git", "ls-tree", "-r", "--name-only", "refs/heads/main")
	treeCmd.Dir = dir
	treeOut, err := treeCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(treeOut), "detached.txt") {
		t.Error("detached.txt not found in main branch tree after --branch commit")
	}
}

// TestUndoAmend verifies that safegit undo after an amend restores the
// pre-amend commit: original file present, amend-added file absent.
func TestUndoAmend(t *testing.T) {
	dir := newRepo(t)

	// Create file.txt and commit it
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "original commit", "--", "file.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Record the original commit SHA (this is what undo should restore)
	origCmd := exec.Command("git", "rev-parse", "HEAD")
	origCmd.Dir = dir
	origOut, err := origCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	origSHA := strings.TrimSpace(string(origOut))

	// Create extra.txt and amend
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("extra\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSafegit(t, dir, "--format", "json", "amend", "--", "extra.txt")
	if code != 0 {
		t.Fatalf("amend failed (code %d): %s", code, stderr)
	}

	// Verify amend JSON has oldSha pointing to original commit
	r := parseJSON(t, stdout)
	if !r.OK {
		t.Fatalf("amend returned ok=false: %s", stdout)
	}
	var amendData struct {
		OldSHA string `json:"oldSha"`
	}
	if err := json.Unmarshal(r.Data, &amendData); err != nil {
		t.Fatalf("parsing amend data: %v", err)
	}
	if amendData.OldSHA != origSHA {
		t.Errorf("amend oldSha = %s, want %s (original commit)", amendData.OldSHA, origSHA)
	}

	// Verify HEAD moved (amend creates a new commit object)
	amendedCmd := exec.Command("git", "rev-parse", "HEAD")
	amendedCmd.Dir = dir
	amendedOut, err := amendedCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	amendedSHA := strings.TrimSpace(string(amendedOut))
	if amendedSHA == origSHA {
		t.Fatal("HEAD did not change after amend")
	}

	// Run safegit undo
	_, stderr, code = runSafegit(t, dir, "undo")
	if code != 0 {
		t.Fatalf("undo failed (code %d): %s", code, stderr)
	}

	// Verify HEAD rolled back to the original commit SHA
	undoneCmd := exec.Command("git", "rev-parse", "HEAD")
	undoneCmd.Dir = dir
	undoneOut, err := undoneCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	undoneSHA := strings.TrimSpace(string(undoneOut))
	if undoneSHA != origSHA {
		t.Errorf("after undo HEAD = %s, want %s (original commit)", undoneSHA, origSHA)
	}

	// Verify file.txt is in tree but extra.txt is not
	treeCmd := exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	treeCmd.Dir = dir
	treeOut, err := treeCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	tree := string(treeOut)
	if !strings.Contains(tree, "file.txt") {
		t.Error("file.txt missing from HEAD tree after undo")
	}
	if strings.Contains(tree, "extra.txt") {
		t.Error("extra.txt still in HEAD tree after undo (should have been removed)")
	}
}
