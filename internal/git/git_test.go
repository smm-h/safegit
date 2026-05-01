package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

func TestRun(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	stdout, _, err := Run(context.Background(), "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(stdout)
	// Resolve symlinks for comparison (t.TempDir may use /tmp which is a symlink)
	wantResolved, _ := filepath.EvalSymlinks(dir)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("RepoRoot = %q, want %q", gotResolved, wantResolved)
	}
}

func TestRunWithEnv(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	// GIT_AUTHOR_NAME should be visible in commit output
	stdout, _, err := RunWithEnv(context.Background(),
		[]string{"GIT_AUTHOR_NAME=EnvTest"},
		"log", "--format=%an", "-1")
	if err != nil {
		t.Fatal(err)
	}
	// The initial commit was made with "Test", not "EnvTest",
	// so this just checks the command runs without error.
	if strings.TrimSpace(stdout) == "" {
		t.Error("expected non-empty output from git log")
	}
}

func TestRepoRoot(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	root, err := RepoRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantResolved, _ := filepath.EvalSymlinks(dir)
	gotResolved, _ := filepath.EvalSymlinks(root)
	if gotResolved != wantResolved {
		t.Errorf("RepoRoot = %q, want %q", gotResolved, wantResolved)
	}
}

func TestHeadRef(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	ref, err := HeadRef(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ref != "refs/heads/main" {
		t.Errorf("HeadRef = %q, want refs/heads/main", ref)
	}
}

func TestRevParse(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	sha, err := RevParse(context.Background(), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) != 40 {
		t.Errorf("RevParse(HEAD) returned %q, want 40-char SHA", sha)
	}
}

func TestReadTreeAndWriteTree(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	// Create a file, add it, commit it so HEAD has a non-empty tree
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0644)
	exec.Command("git", "-C", dir, "add", "hello.txt").Run()
	exec.Command("git", "-C", dir, "commit", "-m", "add hello").Run()

	indexPath := filepath.Join(dir, "test-index")
	err := ReadTree(context.Background(), indexPath, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		t.Fatal("ReadTree did not create index file")
	}

	treeSHA, err := WriteTree(context.Background(), indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(treeSHA) != 40 {
		t.Errorf("WriteTree returned %q, want 40-char SHA", treeSHA)
	}
}

func TestCommitTree(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	// Get the tree SHA from HEAD
	headSHA, _ := RevParse(context.Background(), "HEAD")
	treeSHA, _, _ := Run(context.Background(), "rev-parse", "HEAD^{tree}")
	treeSHA = strings.TrimSpace(treeSHA)

	commitSHA, err := CommitTree(context.Background(), treeSHA, headSHA, "test commit")
	if err != nil {
		t.Fatal(err)
	}
	if len(commitSHA) != 40 {
		t.Errorf("CommitTree returned %q, want 40-char SHA", commitSHA)
	}
}

func TestUpdateRef(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	ctx := context.Background()
	headSHA, _ := RevParse(ctx, "HEAD")
	treeSHA, _, _ := Run(ctx, "rev-parse", "HEAD^{tree}")
	treeSHA = strings.TrimSpace(treeSHA)

	newCommit, _ := CommitTree(ctx, treeSHA, headSHA, "new commit")

	// CAS update: expect headSHA, set to newCommit
	err := UpdateRef(ctx, "refs/heads/main", newCommit, headSHA)
	if err != nil {
		t.Fatal(err)
	}

	// Verify
	currentSHA, _ := RevParse(ctx, "refs/heads/main")
	if currentSHA != newCommit {
		t.Errorf("after UpdateRef: ref = %q, want %q", currentSHA, newCommit)
	}

	// CAS failure: wrong old value
	err = UpdateRef(ctx, "refs/heads/main", headSHA, "0000000000000000000000000000000000000000")
	if err == nil {
		t.Error("UpdateRef should fail with wrong old SHA")
	}
}

func TestGitDir(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	gitDir, err := GitDir(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gitDir, ".git") {
		t.Errorf("GitDir = %q, expected to end with .git", gitDir)
	}
}
