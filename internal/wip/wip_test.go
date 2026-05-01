package wip

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/testutil"
)

func TestCreateAndList(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Write a file to wip
	if err := os.WriteFile(filepath.Join(dir, "wip-file.txt"), []byte("work in progress\n"), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := Create(context.Background(), sgDir, []string{"wip-file.txt"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(info.ID) != 8 {
		t.Errorf("ID = %q, want 8-char hex", info.ID)
	}
	if len(info.Files) != 1 || info.Files[0] != "wip-file.txt" {
		t.Errorf("Files = %v, want [wip-file.txt]", info.Files)
	}
	if info.Ref != "refs/safegit/wip/"+info.ID {
		t.Errorf("Ref = %q, want refs/safegit/wip/%s", info.Ref, info.ID)
	}

	// File should still exist after wip (no longer reverted to avoid clobbering other agents)
	if _, err := os.Stat(filepath.Join(dir, "wip-file.txt")); os.IsNotExist(err) {
		t.Error("wip-file.txt should still exist after Create")
	}

	// Verify it appears in list
	wips, err := List(context.Background(), sgDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(wips) != 1 {
		t.Fatalf("List returned %d wips, want 1", len(wips))
	}
	if wips[0].ID != info.ID {
		t.Errorf("List[0].ID = %q, want %q", wips[0].ID, info.ID)
	}
	if len(wips[0].Files) != 1 || wips[0].Files[0] != "wip-file.txt" {
		t.Errorf("List[0].Files = %v, want [wip-file.txt]", wips[0].Files)
	}
}

func TestRestore(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Modify the existing tracked file (seed.txt)
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("modified seed\n"), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := Create(context.Background(), sgDir, []string{"seed.txt"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// After create, seed.txt should still have the modified content (no revert)
	content, err := os.ReadFile(filepath.Join(dir, "seed.txt"))
	if err != nil {
		t.Fatalf("reading seed.txt after create: %v", err)
	}
	if string(content) != "modified seed\n" {
		t.Errorf("seed.txt after create = %q, want %q", string(content), "modified seed\n")
	}

	// Restore the wip
	restored, err := Restore(context.Background(), sgDir, info.ID)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(restored) != 1 || restored[0] != "seed.txt" {
		t.Errorf("restored = %v, want [seed.txt]", restored)
	}

	// seed.txt should have the wip'd content back
	content, err = os.ReadFile(filepath.Join(dir, "seed.txt"))
	if err != nil {
		t.Fatalf("reading seed.txt after restore: %v", err)
	}
	if string(content) != "modified seed\n" {
		t.Errorf("seed.txt after restore = %q, want %q", string(content), "modified seed\n")
	}

	// Wip ref should be gone
	wips, err := List(context.Background(), sgDir)
	if err != nil {
		t.Fatalf("List after restore: %v", err)
	}
	if len(wips) != 0 {
		t.Errorf("List after restore returned %d wips, want 0", len(wips))
	}

	// Lock file should be gone
	locked, _, _ := IsLocked(sgDir, "seed.txt")
	if locked {
		t.Error("seed.txt still locked after restore")
	}
}

func TestLockConflict(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Modify seed.txt and create a wip
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("wip content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Create(context.Background(), sgDir, []string{"seed.txt"})
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}

	// Try to create another wip for the same file
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("second wip\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err = Create(context.Background(), sgDir, []string{"seed.txt"})
	if err == nil {
		t.Fatal("expected error for lock conflict, got nil")
	}
	if !strings.Contains(err.Error(), "already locked") {
		t.Errorf("error = %q, want to contain 'already locked'", err.Error())
	}
}

func TestRestoreAfterModification(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Modify seed.txt and create a wip
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("wip content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := Create(context.Background(), sgDir, []string{"seed.txt"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Modify seed.txt again after wip
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("different changes\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Restore should succeed and overwrite with wip content
	restored, err := Restore(context.Background(), sgDir, info.ID)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(restored) != 1 {
		t.Errorf("restored = %v, want 1 file", restored)
	}

	// seed.txt should have the wip content
	content, err := os.ReadFile(filepath.Join(dir, "seed.txt"))
	if err != nil {
		t.Fatalf("reading seed.txt: %v", err)
	}
	if string(content) != "wip content\n" {
		t.Errorf("seed.txt = %q, want %q", string(content), "wip content\n")
	}
}

func TestOrphanLocks(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Create a lock file without a corresponding wip ref (orphan)
	if err := writeLockFile(sgDir, "ghost.txt", "deadbeef"); err != nil {
		t.Fatal(err)
	}

	orphans, err := OrphanLocks(context.Background(), sgDir)
	if err != nil {
		t.Fatalf("OrphanLocks: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("OrphanLocks returned %d, want 1", len(orphans))
	}

	// Clean them
	cleaned, err := CleanOrphanLocks(context.Background(), sgDir)
	if err != nil {
		t.Fatalf("CleanOrphanLocks: %v", err)
	}
	if cleaned != 1 {
		t.Errorf("cleaned = %d, want 1", cleaned)
	}

	// Verify no more orphans
	orphans, err = OrphanLocks(context.Background(), sgDir)
	if err != nil {
		t.Fatalf("OrphanLocks after clean: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("orphans after clean = %d, want 0", len(orphans))
	}
}

func TestListEmpty(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	wips, err := List(context.Background(), sgDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(wips) != 0 {
		t.Errorf("List returned %d wips on fresh repo, want 0", len(wips))
	}
}

func TestMultipleFiles(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Create and track two extra files via regular git
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("original\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("git", "add", "a.txt", "b.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "add a and b")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	// Modify both
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("modified\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	info, err := Create(context.Background(), sgDir, []string{"a.txt", "b.txt"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(info.Files) != 2 {
		t.Errorf("Files = %v, want 2 files", info.Files)
	}

	// Both should still have modified content (no revert)
	for _, name := range []string{"a.txt", "b.txt"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if string(content) != "modified\n" {
			t.Errorf("%s = %q, want %q", name, string(content), "modified\n")
		}
	}

	// Restore
	restored, err := Restore(context.Background(), sgDir, info.ID)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(restored) != 2 {
		t.Errorf("restored = %v, want 2 files", restored)
	}

	// Both should have wip content
	for _, name := range []string{"a.txt", "b.txt"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("reading %s after restore: %v", name, err)
		}
		if string(content) != "modified\n" {
			t.Errorf("%s after restore = %q, want %q", name, string(content), "modified\n")
		}
	}
}

// TestRestoreNonexistent verifies that restoring a nonexistent wip-id returns an error.
func TestRestoreNonexistent(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	_, err := Restore(context.Background(), sgDir, "deadbeef")
	if err == nil {
		t.Fatal("expected error for nonexistent wip, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want to contain 'not found'", err.Error())
	}
}

// TestIsLockedAfterCreate verifies lock state matches expectations.
func TestIsLockedAfterCreate(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Modify seed.txt
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("locked content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := Create(context.Background(), sgDir, []string{"seed.txt"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	locked, wipID, err := IsLocked(sgDir, "seed.txt")
	if err != nil {
		t.Fatalf("IsLocked: %v", err)
	}
	if !locked {
		t.Error("seed.txt not locked after Create")
	}
	if wipID != info.ID {
		t.Errorf("lock wipID = %q, want %q", wipID, info.ID)
	}

	// Unrelated file should not be locked
	locked, _, err = IsLocked(sgDir, "other.txt")
	if err != nil {
		t.Fatalf("IsLocked other: %v", err)
	}
	if locked {
		t.Error("other.txt locked unexpectedly")
	}
}

