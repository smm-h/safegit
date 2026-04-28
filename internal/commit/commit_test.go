package commit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/wip"
)

// initTestRepo creates a temp git repo with an initial commit containing one
// file, runs safegit init, and returns (repoDir, gitDir, safegitDir).
func initTestRepo(t *testing.T) (string, string, string) {
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

	// Create a seed file so the initial commit has a non-empty tree
	seedPath := filepath.Join(dir, "seed.txt")
	if err := os.WriteFile(seedPath, []byte("seed\n"), 0644); err != nil {
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
	return dir, gitDir, sgDir
}

// chdir changes into dir for the duration of the test, restoring on cleanup.
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}

// newPipeline creates a Pipeline with default config for the given safegitDir.
func newPipeline(sgDir string) *Pipeline {
	cfg := repo.DefaultConfig()
	return &Pipeline{SafegitDir: sgDir, Config: cfg}
}

// commitLandsOnBranch verifies that ref points to sha.
func commitLandsOnBranch(t *testing.T, ref, sha string) {
	t.Helper()
	got, err := git.RevParse(ref)
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	if got != sha {
		t.Errorf("ref %s = %s, want %s", ref, got, sha)
	}
}

// treeHasFile checks that a commit's tree contains the given file path.
func treeHasFile(t *testing.T, commitSHA, path string) {
	t.Helper()
	ctx := context.Background()
	out, _, err := git.Run(ctx, "ls-tree", "-r", "--name-only", commitSHA)
	if err != nil {
		t.Fatalf("ls-tree: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == path {
			return
		}
	}
	t.Errorf("file %q not found in tree of %s; tree contents:\n%s", path, commitSHA, out)
}

// treeLacksFile checks that a commit's tree does NOT contain the given file.
func treeLacksFile(t *testing.T, commitSHA, path string) {
	t.Helper()
	ctx := context.Background()
	out, _, err := git.Run(ctx, "ls-tree", "-r", "--name-only", commitSHA)
	if err != nil {
		t.Fatalf("ls-tree: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == path {
			t.Errorf("file %q should NOT be in tree of %s", path, commitSHA)
			return
		}
	}
}

func TestBasicCommit(t *testing.T) {
	dir, _, sgDir := initTestRepo(t)
	chdir(t, dir)

	// Write a new file
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	p := newPipeline(sgDir)
	result, err := p.Execute(context.Background(), CommitRequest{
		Message: "add hello",
		Files:   []string{"hello.txt"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(result.SHA) != 40 {
		t.Errorf("SHA = %q, want 40-char hex", result.SHA)
	}
	if result.Ref != "refs/heads/main" {
		t.Errorf("Ref = %q, want refs/heads/main", result.Ref)
	}
	if result.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", result.Attempts)
	}

	commitLandsOnBranch(t, "refs/heads/main", result.SHA)
	treeHasFile(t, result.SHA, "hello.txt")
}

func TestMultipleFiles(t *testing.T) {
	dir, _, sgDir := initTestRepo(t)
	chdir(t, dir)

	files := []string{"a.txt", "b.txt", "c.txt"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(f+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	p := newPipeline(sgDir)
	result, err := p.Execute(context.Background(), CommitRequest{
		Message: "add three files",
		Files:   files,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, f := range files {
		treeHasFile(t, result.SHA, f)
	}
}

func TestDeletedFile(t *testing.T) {
	dir, _, sgDir := initTestRepo(t)
	chdir(t, dir)

	// The seed.txt file was created by initTestRepo; delete it
	if err := os.Remove(filepath.Join(dir, "seed.txt")); err != nil {
		t.Fatal(err)
	}

	p := newPipeline(sgDir)
	result, err := p.Execute(context.Background(), CommitRequest{
		Message: "delete seed",
		Files:   []string{"seed.txt"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	treeLacksFile(t, result.SHA, "seed.txt")
}

func TestDeleteSafegitCommittedFile(t *testing.T) {
	// Regression test: a file committed via safegit (not regular git) must be
	// deletable via safegit. IsTracked must check HEAD, not the main index.
	dir, _, sgDir := initTestRepo(t)
	chdir(t, dir)

	// Step 1: Create and commit a file via safegit
	filePath := filepath.Join(dir, "ephemeral.txt")
	if err := os.WriteFile(filePath, []byte("here today\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p := newPipeline(sgDir)
	addResult, err := p.Execute(context.Background(), CommitRequest{
		Message: "add ephemeral",
		Files:   []string{"ephemeral.txt"},
	})
	if err != nil {
		t.Fatalf("add commit: %v", err)
	}
	treeHasFile(t, addResult.SHA, "ephemeral.txt")

	// Step 2: Delete the file from disk and commit the deletion via safegit
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}
	delResult, err := p.Execute(context.Background(), CommitRequest{
		Message: "delete ephemeral",
		Files:   []string{"ephemeral.txt"},
	})
	if err != nil {
		t.Fatalf("delete commit: %v", err)
	}
	treeLacksFile(t, delResult.SHA, "ephemeral.txt")
}

func TestCASRetry(t *testing.T) {
	dir, _, sgDir := initTestRepo(t)
	chdir(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "ours.txt"), []byte("ours\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Use a sync.Once so the hook only fires once (first attempt).
	// The hook advances the branch tip with a direct git commit,
	// causing a CAS miss on the first try.
	var once sync.Once
	p := newPipeline(sgDir)
	p.PhaseADone = func() {
		once.Do(func() {
			// Advance branch with a racing commit
			raceFile := filepath.Join(dir, "race.txt")
			os.WriteFile(raceFile, []byte("race\n"), 0644)
			cmd := exec.Command("git", "add", "race.txt")
			cmd.Dir = dir
			cmd.Run()
			cmd = exec.Command("git", "commit", "-m", "racing commit")
			cmd.Dir = dir
			cmd.Run()
		})
	}

	result, err := p.Execute(context.Background(), CommitRequest{
		Message: "our commit",
		Files:   []string{"ours.txt"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.Attempts < 2 {
		t.Errorf("Attempts = %d, want >= 2 (CAS retry should have happened)", result.Attempts)
	}

	commitLandsOnBranch(t, "refs/heads/main", result.SHA)
	treeHasFile(t, result.SHA, "ours.txt")
	// The racing commit's file should also be in the tree since we rebased on top of it
	treeHasFile(t, result.SHA, "race.txt")
}

func TestCASExhaustion(t *testing.T) {
	dir, _, sgDir := initTestRepo(t)
	chdir(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data\n"), 0644); err != nil {
		t.Fatal(err)
	}

	raceCounter := 0
	p := newPipeline(sgDir)
	// Force max attempts to 3 for faster test
	p.Config.Commit.CASMaxAttempts = 3
	p.PhaseADone = func() {
		// Every attempt gets a racing commit, so CAS always misses
		raceCounter++
		raceFile := filepath.Join(dir, "race"+string(rune('0'+raceCounter))+".txt")
		os.WriteFile(raceFile, []byte("race\n"), 0644)
		cmd := exec.Command("git", "add", raceFile)
		cmd.Dir = dir
		cmd.Run()
		cmd = exec.Command("git", "commit", "-m", "racing "+string(rune('0'+raceCounter)))
		cmd.Dir = dir
		cmd.Run()
	}

	_, err := p.Execute(context.Background(), CommitRequest{
		Message: "doomed commit",
		Files:   []string{"file.txt"},
	})

	if err == nil {
		t.Fatal("expected CAS exhaustion error, got nil")
	}
	ce, ok := err.(*CommitError)
	if !ok {
		t.Fatalf("expected *CommitError, got %T: %v", err, err)
	}
	if ce.Code != ExitCASExhausted {
		t.Errorf("error code = %d, want %d", ce.Code, ExitCASExhausted)
	}
}

func TestNewUntrackedFile(t *testing.T) {
	dir, _, sgDir := initTestRepo(t)
	chdir(t, dir)

	// Create a brand-new file that has never been tracked
	if err := os.WriteFile(filepath.Join(dir, "brand-new.txt"), []byte("new\n"), 0644); err != nil {
		t.Fatal(err)
	}

	p := newPipeline(sgDir)
	result, err := p.Execute(context.Background(), CommitRequest{
		Message: "add brand-new file",
		Files:   []string{"brand-new.txt"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	treeHasFile(t, result.SHA, "brand-new.txt")
	commitLandsOnBranch(t, "refs/heads/main", result.SHA)
}

func TestDryRun(t *testing.T) {
	dir, _, sgDir := initTestRepo(t)
	chdir(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "dry.txt"), []byte("dry\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Capture the branch tip before the dry run
	beforeSHA, err := git.RevParse("refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}

	p := newPipeline(sgDir)
	result, err := p.Execute(context.Background(), CommitRequest{
		Message: "dry run commit",
		Files:   []string{"dry.txt"},
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Commit object should exist but ref should NOT have moved
	if len(result.SHA) != 40 {
		t.Errorf("SHA = %q, want 40-char hex", result.SHA)
	}

	afterSHA, err := git.RevParse("refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if afterSHA != beforeSHA {
		t.Errorf("ref moved during dry run: before=%s, after=%s", beforeSHA, afterSHA)
	}
}

func TestCommitRefusesWipLocked(t *testing.T) {
	dir, _, sgDir := initTestRepo(t)
	chdir(t, dir)

	// Modify seed.txt and create a wip to lock the file
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("wip content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := wip.Create(sgDir, []string{"seed.txt"})
	if err != nil {
		t.Fatalf("wip.Create: %v", err)
	}

	// Modify seed.txt again and try to commit via the pipeline
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("new content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	p := newPipeline(sgDir)
	_, err = p.Execute(context.Background(), CommitRequest{
		Message: "should fail",
		Files:   []string{"seed.txt"},
	})

	if err == nil {
		t.Fatal("expected commit to fail for wip-locked file, got nil")
	}
	ce, ok := err.(*CommitError)
	if !ok {
		t.Fatalf("expected *CommitError, got %T: %v", err, err)
	}
	if ce.Code != ExitWipLocked {
		t.Errorf("error code = %d, want %d", ce.Code, ExitWipLocked)
	}
	if !strings.Contains(ce.Message, "wip-locked") {
		t.Errorf("error message = %q, want to contain 'wip-locked'", ce.Message)
	}
}
