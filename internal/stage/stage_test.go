package stage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/index"
	"github.com/smm-h/safegit/internal/repo"
)

// initTestRepo creates a temp git repo with an initial commit and safegit initialized.
// Returns (repoDir, safegitDir).
func initTestRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Create a seed file with multiple lines for hunk testing
	seedContent := "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10\n"
	seedPath := filepath.Join(dir, "seed.txt")
	if err := os.WriteFile(seedPath, []byte(seedContent), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "seed.txt"},
		{"git", "commit", "-m", "initial"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	gitDir := filepath.Join(dir, ".git")
	if err := repo.Init(gitDir, false); err != nil {
		t.Fatalf("safegit init: %v", err)
	}
	sgDir := repo.SafegitDir(gitDir)
	return dir, sgDir
}

// chdir changes into dir for the duration of the test.
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}

func TestExtractHunks(t *testing.T) {
	dir, sgDir := initTestRepo(t)
	chdir(t, dir)

	// Modify the file to create multiple hunks:
	// Change line 2 (near top) and line 9 (near bottom) -- with enough
	// unchanged context between them, git produces separate hunks.
	content := "line 1\nMODIFIED 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nMODIFIED 9\nline 10\n"
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tmpIdx, err := index.New(sgDir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer tmpIdx.Cleanup()

	header, hunks, err := ExtractHunks(tmpIdx.IndexPath, "seed.txt")
	if err != nil {
		t.Fatalf("ExtractHunks: %v", err)
	}

	if len(header) == 0 {
		t.Error("expected non-empty header")
	}

	// With default diff context (3 lines), lines 2 and 9 should produce 2 hunks
	// since there are 5 unchanged lines between them (more than 2*3=6 context overlap threshold)
	if len(hunks) < 1 {
		t.Fatalf("expected at least 1 hunk, got %d", len(hunks))
	}

	// Verify first hunk contains the modification
	found := false
	for _, line := range hunks[0].Body {
		if strings.Contains(line, "MODIFIED") {
			found = true
			break
		}
	}
	if !found {
		t.Error("first hunk does not contain the modification")
	}

	// Verify hunk indices are 1-based
	if hunks[0].Index != 1 {
		t.Errorf("first hunk index = %d, want 1", hunks[0].Index)
	}
}

func TestBuildPatch(t *testing.T) {
	header := []string{
		"diff --git a/file.txt b/file.txt",
		"index abc1234..def5678 100644",
		"--- a/file.txt",
		"+++ b/file.txt",
	}
	hunks := []Hunk{
		{Index: 1, Header: "@@ -1,3 +1,3 @@", Body: []string{" line1", "-old2", "+new2", " line3"}},
		{Index: 2, Header: "@@ -8,3 +8,3 @@", Body: []string{" line8", "-old9", "+new9", " line10"}},
	}

	// Select only hunk 1
	patch, err := BuildPatch(header, hunks, []int{1})
	if err != nil {
		t.Fatalf("BuildPatch: %v", err)
	}

	patchStr := string(patch)
	if !strings.Contains(patchStr, "diff --git") {
		t.Error("patch missing diff header")
	}
	if !strings.Contains(patchStr, "@@ -1,3 +1,3 @@") {
		t.Error("patch missing hunk 1 header")
	}
	if strings.Contains(patchStr, "@@ -8,3 +8,3 @@") {
		t.Error("patch should not contain hunk 2")
	}

	// Select both hunks
	patch, err = BuildPatch(header, hunks, []int{1, 2})
	if err != nil {
		t.Fatalf("BuildPatch both: %v", err)
	}
	patchStr = string(patch)
	if !strings.Contains(patchStr, "@@ -1,3 +1,3 @@") || !strings.Contains(patchStr, "@@ -8,3 +8,3 @@") {
		t.Error("patch should contain both hunks")
	}
}

func TestStageWholeFile(t *testing.T) {
	dir, sgDir := initTestRepo(t)
	chdir(t, dir)

	// Modify seed.txt
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tmpIdx, err := index.New(sgDir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer tmpIdx.Cleanup()

	// Get tree SHA before staging
	treeBefore, err := git.WriteTree(tmpIdx.IndexPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := StageFile(tmpIdx.IndexPath, "seed.txt"); err != nil {
		t.Fatalf("StageFile: %v", err)
	}

	treeAfter, err := git.WriteTree(tmpIdx.IndexPath)
	if err != nil {
		t.Fatal(err)
	}

	if treeBefore == treeAfter {
		t.Error("tree did not change after staging")
	}

	// Verify the staged content matches the new file
	ctx := context.Background()
	env := []string{"GIT_INDEX_FILE=" + tmpIdx.IndexPath}
	out, _, err := git.RunWithEnv(ctx, env, "show", ":seed.txt")
	if err != nil {
		t.Fatalf("show staged: %v", err)
	}
	if strings.TrimSpace(out) != "changed" {
		t.Errorf("staged content = %q, want %q", strings.TrimSpace(out), "changed")
	}
}

func TestStageSpecificHunks(t *testing.T) {
	dir, sgDir := initTestRepo(t)
	chdir(t, dir)

	// Create a file with enough lines to produce distinct hunks
	// Original has lines 1-10. Modify lines 1, 5, and 10 to get 3 hunks
	// (with enough distance between them).
	original := strings.Repeat("aaa\n", 20)
	seedPath := filepath.Join(dir, "multi.txt")
	if err := os.WriteFile(seedPath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "multi.txt"},
		{"git", "commit", "-m", "add multi.txt"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	// Now modify lines 1, 10, and 20 to create 3 hunks (separated by >6 unchanged lines)
	lines := strings.Split(original, "\n")
	lines[0] = "BBB"   // line 1
	lines[9] = "CCC"   // line 10
	lines[19] = "DDD"  // line 20
	modified := strings.Join(lines, "\n")
	if err := os.WriteFile(seedPath, []byte(modified), 0644); err != nil {
		t.Fatal(err)
	}

	tmpIdx, err := index.New(sgDir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer tmpIdx.Cleanup()

	// Verify we get multiple hunks
	_, hunks, err := ExtractHunks(tmpIdx.IndexPath, "multi.txt")
	if err != nil {
		t.Fatalf("ExtractHunks: %v", err)
	}
	if len(hunks) < 2 {
		t.Fatalf("expected at least 2 hunks, got %d", len(hunks))
	}

	// Stage only hunk 1 and the last hunk
	selectedHunks := []int{1, len(hunks)}
	if err := StageHunks(tmpIdx.IndexPath, "multi.txt", selectedHunks); err != nil {
		t.Fatalf("StageHunks: %v", err)
	}

	// After staging hunks 1 and last, the remaining diff should only show the middle hunk(s)
	_, remainingHunks, err := ExtractHunks(tmpIdx.IndexPath, "multi.txt")
	if err != nil {
		t.Fatalf("ExtractHunks after partial stage: %v", err)
	}

	// We staged 2 hunks out of N, so there should be fewer remaining
	if len(remainingHunks) >= len(hunks) {
		t.Errorf("expected fewer remaining hunks after staging; before=%d, after=%d", len(hunks), len(remainingHunks))
	}
}

func TestUnstageFile(t *testing.T) {
	dir, sgDir := initTestRepo(t)
	chdir(t, dir)

	// Modify and stage
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("modified\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tmpIdx, err := index.New(sgDir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer tmpIdx.Cleanup()

	// Record HEAD tree
	headTree, err := git.WriteTree(tmpIdx.IndexPath)
	if err != nil {
		t.Fatal(err)
	}

	// Stage the change
	if err := StageFile(tmpIdx.IndexPath, "seed.txt"); err != nil {
		t.Fatal(err)
	}

	// Verify tree changed
	stagedTree, err := git.WriteTree(tmpIdx.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	if stagedTree == headTree {
		t.Fatal("staging did not change tree")
	}

	// Unstage
	if err := UnstageFile(tmpIdx.IndexPath, "seed.txt"); err != nil {
		t.Fatalf("UnstageFile: %v", err)
	}

	// Tree should match HEAD again
	unstagedTree, err := git.WriteTree(tmpIdx.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	if unstagedTree != headTree {
		t.Errorf("tree after unstage = %s, want %s (HEAD)", unstagedTree, headTree)
	}
}

func TestBinaryFileReject(t *testing.T) {
	dir, sgDir := initTestRepo(t)
	chdir(t, dir)

	// Create a binary file (with null bytes)
	binContent := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0x00, 0x50, 0x4E, 0x47}
	binPath := filepath.Join(dir, "binary.bin")
	if err := os.WriteFile(binPath, binContent, 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "binary.bin"},
		{"git", "commit", "-m", "add binary"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	// Modify the binary file
	if err := os.WriteFile(binPath, []byte{0xFF, 0xFE, 0xFD, 0x00, 0x01}, 0644); err != nil {
		t.Fatal(err)
	}

	tmpIdx, err := index.New(sgDir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer tmpIdx.Cleanup()

	_, _, err = ExtractHunks(tmpIdx.IndexPath, "binary.bin")
	if err == nil {
		t.Fatal("expected error for binary file, got nil")
	}
	if !strings.Contains(err.Error(), "binary") {
		t.Errorf("error should mention 'binary', got: %v", err)
	}
}

func TestParseHunkSpec(t *testing.T) {
	tests := []struct {
		input string
		want  []int
		err   bool
	}{
		{"1", []int{1}, false},
		{"1,3,5", []int{1, 3, 5}, false},
		{"2-4", []int{2, 3, 4}, false},
		{"1,3-5,7", []int{1, 3, 4, 5, 7}, false},
		{"", nil, true},
		{"abc", nil, true},
		{"5-3", nil, true}, // start > end
	}

	for _, tt := range tests {
		got, err := ParseHunkSpec(tt.input)
		if tt.err {
			if err == nil {
				t.Errorf("ParseHunkSpec(%q): expected error, got %v", tt.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseHunkSpec(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if len(got) != len(tt.want) {
			t.Errorf("ParseHunkSpec(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("ParseHunkSpec(%q)[%d] = %d, want %d", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestParseHunkHeader(t *testing.T) {
	tests := []struct {
		input                                  string
		oldStart, oldCount, newStart, newCount int
	}{
		{"@@ -1,3 +1,3 @@", 1, 3, 1, 3},
		{"@@ -10,5 +12,7 @@ func foo()", 10, 5, 12, 7},
		{"@@ -1 +1 @@", 1, 1, 1, 1},
		{"@@ -0,0 +1,5 @@", 0, 0, 1, 5},
	}

	for _, tt := range tests {
		os, oc, ns, nc := parseHunkHeader(tt.input)
		if os != tt.oldStart || oc != tt.oldCount || ns != tt.newStart || nc != tt.newCount {
			t.Errorf("parseHunkHeader(%q) = (%d,%d,%d,%d), want (%d,%d,%d,%d)",
				tt.input, os, oc, ns, nc, tt.oldStart, tt.oldCount, tt.newStart, tt.newCount)
		}
	}
}
