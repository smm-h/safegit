package test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
