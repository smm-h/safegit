package coord

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/testutil"
	"github.com/smm-h/safegit/internal/wip"
)

func TestCleanRepo(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	ds, err := Check(context.Background(), sgDir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ds != nil {
		t.Errorf("expected nil DirtyState on clean repo, got %+v", ds)
	}
}

func TestDirtyModified(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Modify the tracked file
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("modified\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ds, err := Check(context.Background(), sgDir)
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
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Create an untracked file
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("scratch\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ds, err := Check(context.Background(), sgDir)
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
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Modify seed.txt and create a wip to get a lock file
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("wip content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := wip.Create(context.Background(), sgDir, []string{"seed.txt"})
	if err != nil {
		t.Fatalf("wip.Create: %v", err)
	}

	// After wip.Create, working tree is clean, but wip-lock exists
	ds, err := Check(context.Background(), sgDir)
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
