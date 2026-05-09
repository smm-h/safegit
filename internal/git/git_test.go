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

func TestParseCommit(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Make a second commit so HEAD has a parent
	Run(ctx, "commit", "--allow-empty", "-m", "second")
	sha, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	info, err := ParseCommit(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	if len(info.Tree) != 40 {
		t.Errorf("Tree = %q, want 40-char SHA", info.Tree)
	}
	if len(info.Parents) != 1 {
		t.Errorf("Parents = %v, want exactly 1 parent", info.Parents)
	}
	if info.Author.Name != "Test" {
		t.Errorf("Author.Name = %q, want %q", info.Author.Name, "Test")
	}
	if info.Author.Email != "test@test.com" {
		t.Errorf("Author.Email = %q, want %q", info.Author.Email, "test@test.com")
	}
	if info.Committer.Name != "Test" {
		t.Errorf("Committer.Name = %q, want %q", info.Committer.Name, "Test")
	}
	if info.Message != "second" {
		t.Errorf("Message = %q, want %q", info.Message, "second")
	}
}

func TestParseCommitRootCommit(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Find the root commit (the initial --allow-empty commit)
	out, _, err := Run(ctx, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	rootSHA := strings.TrimSpace(out)

	info, err := ParseCommit(ctx, rootSHA)
	if err != nil {
		t.Fatal(err)
	}

	if len(info.Parents) != 0 {
		t.Errorf("Root commit Parents = %v, want empty", info.Parents)
	}
}

func TestParseCommitMerge(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Create a branch from the initial commit, make commits on each, merge
	Run(ctx, "checkout", "-b", "feature")
	Run(ctx, "commit", "--allow-empty", "-m", "feature commit")
	Run(ctx, "checkout", "main")
	Run(ctx, "commit", "--allow-empty", "-m", "main commit")
	Run(ctx, "merge", "feature", "--no-ff", "-m", "merge")

	sha, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	info, err := ParseCommit(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	if len(info.Parents) != 2 {
		t.Errorf("Merge commit Parents = %v (len %d), want 2 parents", info.Parents, len(info.Parents))
	}
}

func TestParseCommitMultiLineMessage(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Create a commit with a multi-line message including an internal blank line
	wantMsg := "line1\n\nline3"
	Run(ctx, "commit", "--allow-empty", "-m", wantMsg)

	sha, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	info, err := ParseCommit(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	if info.Message != wantMsg {
		t.Errorf("Message = %q, want %q", info.Message, wantMsg)
	}
}

func TestCommitTreeWithAuthor(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	treeSHA, _, err := Run(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	treeSHA = strings.TrimSpace(treeSHA)

	headSHA, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	author := AuthorInfo{Name: "Alice Author", Email: "alice@example.com", Date: "1700000000 +0000"}
	committer := AuthorInfo{Name: "Bob Committer", Email: "bob@example.com", Date: "1700000001 +0100"}

	newSHA, err := CommitTreeWithAuthor(ctx, treeSHA, []string{headSHA}, "custom commit", author, committer)
	if err != nil {
		t.Fatal(err)
	}
	if len(newSHA) != 40 {
		t.Fatalf("CommitTreeWithAuthor returned %q, want 40-char SHA", newSHA)
	}

	info, err := ParseCommit(ctx, newSHA)
	if err != nil {
		t.Fatal(err)
	}

	if info.Author.Name != author.Name {
		t.Errorf("Author.Name = %q, want %q", info.Author.Name, author.Name)
	}
	if info.Author.Email != author.Email {
		t.Errorf("Author.Email = %q, want %q", info.Author.Email, author.Email)
	}
	if info.Author.Date != author.Date {
		t.Errorf("Author.Date = %q, want %q", info.Author.Date, author.Date)
	}
	if info.Committer.Name != committer.Name {
		t.Errorf("Committer.Name = %q, want %q", info.Committer.Name, committer.Name)
	}
	if info.Committer.Email != committer.Email {
		t.Errorf("Committer.Email = %q, want %q", info.Committer.Email, committer.Email)
	}
	if info.Committer.Date != committer.Date {
		t.Errorf("Committer.Date = %q, want %q", info.Committer.Date, committer.Date)
	}
	if info.Message != "custom commit" {
		t.Errorf("Message = %q, want %q", info.Message, "custom commit")
	}
}

func TestCommitTreeWithAuthorMultipleParents(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Create two commits to use as parents
	Run(ctx, "commit", "--allow-empty", "-m", "commit A")
	shaA, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	Run(ctx, "commit", "--allow-empty", "-m", "commit B")
	shaB, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	treeSHA, _, err := Run(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	treeSHA = strings.TrimSpace(treeSHA)

	author := AuthorInfo{Name: "Test", Email: "test@test.com", Date: "1700000000 +0000"}
	committer := author

	newSHA, err := CommitTreeWithAuthor(ctx, treeSHA, []string{shaA, shaB}, "multi-parent", author, committer)
	if err != nil {
		t.Fatal(err)
	}

	info, err := ParseCommit(ctx, newSHA)
	if err != nil {
		t.Fatal(err)
	}

	if len(info.Parents) != 2 {
		t.Fatalf("Parents = %v (len %d), want 2 parents", info.Parents, len(info.Parents))
	}

	parentSet := map[string]bool{info.Parents[0]: true, info.Parents[1]: true}
	if !parentSet[shaA] {
		t.Errorf("Parents %v does not contain shaA %s", info.Parents, shaA)
	}
	if !parentSet[shaB] {
		t.Errorf("Parents %v does not contain shaB %s", info.Parents, shaB)
	}
}
