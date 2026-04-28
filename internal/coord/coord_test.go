package coord

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/wip"
)

// initTestRepo creates a temp git repo with an initial commit, runs safegit init,
// and returns (repoDir, gitDir, safegitDir).
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

	// Seed file so the initial commit has a non-empty tree
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

func TestCleanRepo(t *testing.T) {
	dir, _, sgDir := initTestRepo(t)
	chdir(t, dir)

	ds, err := Check(sgDir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ds != nil {
		t.Errorf("expected nil DirtyState on clean repo, got %+v", ds)
	}
}

func TestDirtyModified(t *testing.T) {
	dir, _, sgDir := initTestRepo(t)
	chdir(t, dir)

	// Modify the tracked file
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("modified\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ds, err := Check(sgDir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ds == nil {
		t.Fatal("expected non-nil DirtyState for modified file")
	}
	if len(ds.ModifiedFiles) == 0 {
		t.Fatal("expected at least one modified file")
	}

	// Verify seed.txt appears in the output
	found := false
	for _, f := range ds.ModifiedFiles {
		if strings.Contains(f, "seed.txt") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("seed.txt not found in ModifiedFiles: %v", ds.ModifiedFiles)
	}
}

func TestDirtyUntracked(t *testing.T) {
	dir, _, sgDir := initTestRepo(t)
	chdir(t, dir)

	// Create an untracked file
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("scratch\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ds, err := Check(sgDir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ds == nil {
		t.Fatal("expected non-nil DirtyState for untracked file")
	}

	found := false
	for _, f := range ds.ModifiedFiles {
		if strings.Contains(f, "scratch.txt") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("scratch.txt not found in ModifiedFiles: %v", ds.ModifiedFiles)
	}
}

func TestDirtyWipLock(t *testing.T) {
	dir, _, sgDir := initTestRepo(t)
	chdir(t, dir)

	// Modify seed.txt and create a wip to get a lock file
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("wip content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := wip.Create(sgDir, []string{"seed.txt"})
	if err != nil {
		t.Fatalf("wip.Create: %v", err)
	}

	// After wip.Create, working tree is clean, but wip-lock exists
	ds, err := Check(sgDir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ds == nil {
		t.Fatal("expected non-nil DirtyState for wip lock")
	}
	if len(ds.WipLocks) == 0 {
		t.Fatal("expected at least one wip lock")
	}

	// Verify the wip ref appears
	found := false
	for _, w := range ds.WipLocks {
		if strings.Contains(w, info.ID) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("wip ID %s not found in WipLocks: %v", info.ID, ds.WipLocks)
	}
}

func TestRefuseMessage(t *testing.T) {
	ds := &DirtyState{
		ModifiedFiles: []string{" M src/foo.go", "?? scratch.txt"},
		WipLocks:      []string{"refs/safegit/wip/a3f9e1c2  (src/baz.go)"},
	}

	msg := ds.Refuse("checkout")

	// Check key parts of the message
	checks := []string{
		"refusing checkout",
		"Modified files:",
		" M src/foo.go",
		"?? scratch.txt",
		"Active wips:",
		"refs/safegit/wip/a3f9e1c2",
		"Suggestion:",
		"safegit commit",
		"safegit wip",
		"--force",
	}
	for _, want := range checks {
		if !strings.Contains(msg, want) {
			t.Errorf("Refuse message missing %q.\nGot:\n%s", want, msg)
		}
	}
}
