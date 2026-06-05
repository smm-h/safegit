package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRedo verifies the basic redo flow: commit -> undo -> redo restores the commit.
func TestRedo(t *testing.T) {
	dir := newRepo(t)
	env := []string{"CLAUDE_CODE_SESSION_ID=redo-test"}

	// Make a commit
	if err := os.WriteFile(filepath.Join(dir, "redo.txt"), []byte("redo content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "commit for redo", "--", "redo.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	commitSHA := revParseHEAD(t, dir)

	// Undo the commit
	_, stderr, code = runSafegitEnv(t, dir, env, "undo")
	if code != 0 {
		t.Fatalf("undo failed (code %d): %s", code, stderr)
	}
	undoSHA := revParseHEAD(t, dir)
	if undoSHA == commitSHA {
		t.Fatal("HEAD did not change after undo")
	}

	// Redo should restore the commit
	_, stderr, code = runSafegitEnv(t, dir, env, "redo")
	if code != 0 {
		t.Fatalf("redo failed (code %d): %s", code, stderr)
	}
	redoSHA := revParseHEAD(t, dir)
	if redoSHA != commitSHA {
		t.Errorf("after redo, HEAD = %s, want %s (original commit)", redoSHA, commitSHA)
	}

	// redo.txt should be back in the tree
	treeCmd := exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	treeCmd.Dir = dir
	treeOut, err := treeCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(treeOut), "redo.txt") {
		t.Error("redo.txt missing from HEAD tree after redo")
	}
}

// TestRedoWhenLastOpNotUndo verifies that redo errors when the last operation is not undo.
func TestRedoWhenLastOpNotUndo(t *testing.T) {
	dir := newRepo(t)
	env := []string{"CLAUDE_CODE_SESSION_ID=redo-test"}

	// Make a commit (no undo)
	if err := os.WriteFile(filepath.Join(dir, "noundo.txt"), []byte("no undo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "commit without undo", "--", "noundo.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	// Redo should fail
	_, stderr, code = runSafegitEnv(t, dir, env, "redo")
	if code == 0 {
		t.Fatal("redo should have failed when last op is not undo, but exited 0")
	}
	if !strings.Contains(stderr, "commit") || !strings.Contains(stderr, "not \"undo\"") {
		t.Errorf("expected error mentioning last op was commit not undo, got: %s", stderr)
	}
}

// TestRedoAfterRedo verifies that redo -> redo fails (last op is "redo", not "undo").
func TestRedoAfterRedo(t *testing.T) {
	dir := newRepo(t)
	env := []string{"CLAUDE_CODE_SESSION_ID=redo-test"}

	// commit -> undo -> redo
	if err := os.WriteFile(filepath.Join(dir, "double.txt"), []byte("double redo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "commit for double redo", "--", "double.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	_, stderr, code = runSafegitEnv(t, dir, env, "undo")
	if code != 0 {
		t.Fatalf("undo failed (code %d): %s", code, stderr)
	}

	_, stderr, code = runSafegitEnv(t, dir, env, "redo")
	if code != 0 {
		t.Fatalf("first redo failed (code %d): %s", code, stderr)
	}

	// Second redo should fail
	_, stderr, code = runSafegitEnv(t, dir, env, "redo")
	if code == 0 {
		t.Fatal("second redo should have failed, but exited 0")
	}
	if !strings.Contains(stderr, "redo") || !strings.Contains(stderr, "not \"undo\"") {
		t.Errorf("expected error mentioning last op was redo not undo, got: %s", stderr)
	}
}

// TestUndoAfterRedo verifies that undo after redo fails with "cannot undo redo".
func TestUndoAfterRedo(t *testing.T) {
	dir := newRepo(t)
	env := []string{"CLAUDE_CODE_SESSION_ID=redo-test"}

	// commit -> undo -> redo
	if err := os.WriteFile(filepath.Join(dir, "undoredo.txt"), []byte("undo after redo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "commit for undo-after-redo", "--", "undoredo.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	_, stderr, code = runSafegitEnv(t, dir, env, "undo")
	if code != 0 {
		t.Fatalf("undo failed (code %d): %s", code, stderr)
	}

	_, stderr, code = runSafegitEnv(t, dir, env, "redo")
	if code != 0 {
		t.Fatalf("redo failed (code %d): %s", code, stderr)
	}

	// Undo should fail because the last op is "redo" which is not in undoableOps
	_, stderr, code = runSafegitEnv(t, dir, env, "undo")
	if code == 0 {
		t.Fatal("undo after redo should have failed, but exited 0")
	}
	if !strings.Contains(stderr, "cannot undo") && !strings.Contains(stderr, "redo") {
		t.Errorf("expected error about cannot undo redo, got: %s", stderr)
	}
}

// TestRedoDryRun verifies that redo --dry-run does not change HEAD.
func TestRedoDryRun(t *testing.T) {
	dir := newRepo(t)
	env := []string{"CLAUDE_CODE_SESSION_ID=redo-test"}

	// commit -> undo
	if err := os.WriteFile(filepath.Join(dir, "dryrun.txt"), []byte("dry run\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, env, "commit", "-m", "commit for dry-run redo", "--", "dryrun.txt")
	if code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}

	_, stderr, code = runSafegitEnv(t, dir, env, "undo")
	if code != 0 {
		t.Fatalf("undo failed (code %d): %s", code, stderr)
	}
	shaAfterUndo := revParseHEAD(t, dir)

	// redo --dry-run should not change HEAD
	stdout, stderr, code := runSafegitEnv(t, dir, env, "--dry-run", "redo")
	if code != 0 {
		t.Fatalf("redo --dry-run failed (code %d): %s", code, stderr)
	}

	shaAfterDryRun := revParseHEAD(t, dir)
	if shaAfterDryRun != shaAfterUndo {
		t.Errorf("HEAD changed after dry-run: %s -> %s", shaAfterUndo, shaAfterDryRun)
	}

	// Output should mention "would redo"
	if !strings.Contains(stdout, "would redo") {
		t.Errorf("dry-run output should contain 'would redo', got: %s", stdout)
	}
}

// TestRedoSessionScoped verifies that redo finds the correct session's undo,
// not another session's.
func TestRedoSessionScoped(t *testing.T) {
	dir := newRepo(t)
	envA := []string{"CLAUDE_CODE_SESSION_ID=session-A"}
	envB := []string{"CLAUDE_CODE_SESSION_ID=session-B"}

	// Session A commits
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("from A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, dir, envA, "commit", "-m", "commit from A", "--", "a.txt")
	if code != 0 {
		t.Fatalf("session A commit failed (code %d): %s", code, stderr)
	}
	shaAfterA := revParseHEAD(t, dir)

	// Session A undoes
	_, stderr, code = runSafegitEnv(t, dir, envA, "undo")
	if code != 0 {
		t.Fatalf("session A undo failed (code %d): %s", code, stderr)
	}
	shaAfterAUndo := revParseHEAD(t, dir)
	if shaAfterAUndo == shaAfterA {
		t.Fatal("HEAD did not change after session A undo")
	}

	// Session B commits (on top of the undone state)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("from B\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegitEnv(t, dir, envB, "commit", "-m", "commit from B", "--", "b.txt")
	if code != 0 {
		t.Fatalf("session B commit failed (code %d): %s", code, stderr)
	}

	// Session B undoes
	_, stderr, code = runSafegitEnv(t, dir, envB, "undo")
	if code != 0 {
		t.Fatalf("session B undo failed (code %d): %s", code, stderr)
	}

	// Session A redo should find A's undo (restoring A's commit), not B's undo.
	// However, the CAS will fail because B's commit+undo moved the ref.
	// Session A's undo entry has sha=<initial> (what A rolled back TO) and oldSha=<A's commit>.
	// The current HEAD is <initial> (after B's undo), which matches A's undo entry sha.
	// So the CAS: update ref from sha (initial) to oldSha (A's commit) should succeed.
	_, stderr, code = runSafegitEnv(t, dir, envA, "redo")
	if code != 0 {
		t.Fatalf("session A redo failed (code %d): %s", code, stderr)
	}

	// HEAD should be at session A's commit SHA
	redoSHA := revParseHEAD(t, dir)
	if redoSHA != shaAfterA {
		t.Errorf("after session A redo, HEAD = %s, want %s (session A's commit)", redoSHA, shaAfterA)
	}
}
