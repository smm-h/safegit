package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitDiffTreeRename runs git diff-tree with rename detection on HEAD and
// returns the combined output.
func gitDiffTreeRename(t *testing.T, repoDir string) string {
	t.Helper()
	cmd := exec.Command("git", "diff-tree", "--no-commit-id", "-r", "-M", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git diff-tree failed: %v\n%s", err, out)
	}
	return string(out)
}

// gitStatusPorcelain runs git status --porcelain and returns trimmed output.
func gitStatusPorcelain(t *testing.T, repoDir string) string {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status failed: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeFile is a test helper that writes content to a file in the repo.
func writeFile(t *testing.T, repoDir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoDir, name), []byte(content), 0644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func TestMoveDetection_BasicRename(t *testing.T) {
	dir := newRepo(t)

	// Write foo.txt and commit it
	writeFile(t, dir, "foo.txt", "hello world")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add foo", "--", "foo.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Rename foo.txt -> bar.txt
	if err := os.Rename(filepath.Join(dir, "foo.txt"), filepath.Join(dir, "bar.txt")); err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	// Commit only the new path
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "rename foo to bar", "--", "bar.txt")
	if code != 0 {
		t.Fatalf("rename commit failed (code %d): %s", code, stderr)
	}

	// stderr should mention auto-staged deletion with rename detected
	if !strings.Contains(stderr, "auto-staged deletion: foo.txt (rename detected)") {
		t.Fatalf("expected stderr to contain auto-staged deletion message, got: %s", stderr)
	}

	// git diff-tree should show a rename
	diffTree := gitDiffTreeRename(t, dir)
	if !strings.Contains(diffTree, "foo.txt") || !strings.Contains(diffTree, "bar.txt") {
		t.Fatalf("expected diff-tree to mention both foo.txt and bar.txt, got: %s", diffTree)
	}
	if !strings.Contains(diffTree, "R") {
		t.Fatalf("expected diff-tree to show rename (R), got: %s", diffTree)
	}

	// Working tree should be clean
	status := gitStatusPorcelain(t, dir)
	if status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}
}

func TestMoveDetection_MoveAndEdit(t *testing.T) {
	dir := newRepo(t)

	// Write foo.txt and commit
	writeFile(t, dir, "foo.txt", "hello world")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add foo", "--", "foo.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Rename foo.txt -> bar.txt, then modify content
	if err := os.Rename(filepath.Join(dir, "foo.txt"), filepath.Join(dir, "bar.txt")); err != nil {
		t.Fatalf("rename failed: %v", err)
	}
	writeFile(t, dir, "bar.txt", "goodbye world")

	// Commit only the new path
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "move and edit", "--", "bar.txt")
	if code != 0 {
		t.Fatalf("move-and-edit commit failed (code %d): %s", code, stderr)
	}

	// No rename detection when content differs
	if strings.Contains(stderr, "rename detected") {
		t.Fatalf("expected no rename detection when content changed, got: %s", stderr)
	}

	// foo.txt deletion should NOT be auto-staged
	status := gitStatusPorcelain(t, dir)
	if !strings.Contains(status, "D foo.txt") {
		t.Fatalf("expected 'D foo.txt' in status, got: %s", status)
	}
}

func TestMoveDetection_ExplicitBothPaths(t *testing.T) {
	dir := newRepo(t)

	// Write foo.txt and commit
	writeFile(t, dir, "foo.txt", "hello world")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add foo", "--", "foo.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Rename foo.txt -> bar.txt
	if err := os.Rename(filepath.Join(dir, "foo.txt"), filepath.Join(dir, "bar.txt")); err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	// Commit both paths explicitly
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "rename", "--", "bar.txt", "foo.txt")
	if code != 0 {
		t.Fatalf("explicit-both-paths commit failed (code %d): %s", code, stderr)
	}

	// No auto-staging needed when user lists both paths
	if strings.Contains(stderr, "rename detected") {
		t.Fatalf("expected no rename detection when both paths explicit, got: %s", stderr)
	}

	// Working tree should be clean
	status := gitStatusPorcelain(t, dir)
	if status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}
}

func TestMoveDetection_UnrelatedDeletion(t *testing.T) {
	dir := newRepo(t)

	// Write a.txt and b.txt, commit both
	writeFile(t, dir, "a.txt", "content A")
	writeFile(t, dir, "b.txt", "content B")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add a and b", "--", "a.txt", "b.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Delete a.txt (unrelated) and rename b.txt -> c.txt
	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatalf("remove a.txt failed: %v", err)
	}
	if err := os.Rename(filepath.Join(dir, "b.txt"), filepath.Join(dir, "c.txt")); err != nil {
		t.Fatalf("rename b.txt failed: %v", err)
	}

	// Commit only the new path of b
	_, stderr, code = runSafegit(t, dir, "commit", "-m", "rename b", "--", "c.txt")
	if code != 0 {
		t.Fatalf("rename commit failed (code %d): %s", code, stderr)
	}

	// Should auto-stage b.txt deletion
	if !strings.Contains(stderr, "auto-staged deletion: b.txt (rename detected)") {
		t.Fatalf("expected auto-staged deletion of b.txt, got: %s", stderr)
	}

	// Should NOT mention a.txt (unrelated deletion)
	if strings.Contains(stderr, "a.txt") {
		t.Fatalf("expected no mention of a.txt in stderr, got: %s", stderr)
	}

	// a.txt should still show as deleted in status
	status := gitStatusPorcelain(t, dir)
	if !strings.Contains(status, "D a.txt") {
		t.Fatalf("expected 'D a.txt' in status, got: %s", status)
	}
}

func TestMoveDetection_Amend(t *testing.T) {
	dir := newRepo(t)

	// Write foo.txt and commit
	writeFile(t, dir, "foo.txt", "hello world")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add foo", "--", "foo.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Rename foo.txt -> bar.txt
	if err := os.Rename(filepath.Join(dir, "foo.txt"), filepath.Join(dir, "bar.txt")); err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	// Amend with only the new path
	_, stderr, code = runSafegit(t, dir, "commit", "--amend", "-m", "amend with rename", "--", "bar.txt")
	if code != 0 {
		t.Fatalf("amend commit failed (code %d): %s", code, stderr)
	}

	// Should detect the rename
	if !strings.Contains(stderr, "auto-staged deletion: foo.txt (rename detected)") {
		t.Fatalf("expected auto-staged deletion message for amend, got: %s", stderr)
	}

	// Working tree should be clean
	status := gitStatusPorcelain(t, dir)
	if status != "" {
		t.Fatalf("expected clean working tree, got: %s", status)
	}
}

func TestMoveDetection_QuietSuppresses(t *testing.T) {
	dir := newRepo(t)

	// Write foo.txt and commit
	writeFile(t, dir, "foo.txt", "hello world")
	_, stderr, code := runSafegit(t, dir, "commit", "-m", "add foo", "--", "foo.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Rename foo.txt -> bar.txt
	if err := os.Rename(filepath.Join(dir, "foo.txt"), filepath.Join(dir, "bar.txt")); err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	// Commit with --quiet
	_, stderr, code = runSafegit(t, dir, "--quiet", "commit", "-m", "rename", "--", "bar.txt")
	if code != 0 {
		t.Fatalf("quiet commit failed (code %d): %s", code, stderr)
	}

	// Should NOT mention rename in output (suppressed by --quiet)
	if strings.Contains(stderr, "rename detected") {
		t.Fatalf("expected no rename message with --quiet, got: %s", stderr)
	}

	// But move detection should still have run -- working tree should be clean
	status := gitStatusPorcelain(t, dir)
	if status != "" {
		t.Fatalf("expected clean working tree (move detection should still run), got: %s", status)
	}
}
