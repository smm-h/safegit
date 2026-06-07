package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newRepoWithSubmodule creates a parent repo with one submodule at "mysub".
// It returns the parent repo dir and the submodule origin dir.
// The parent has 2 commits: "initial" (seed.txt) and "add submodule" (.gitmodules + mysub).
func newRepoWithSubmodule(t *testing.T) (parentDir, subOriginDir string) {
	t.Helper()
	base := t.TempDir()

	// Create the repo that will become the submodule origin
	subOriginDir = filepath.Join(base, "sub-origin")
	if err := os.MkdirAll(subOriginDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subOriginDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(subOriginDir, "sub-file.txt"), []byte("sub content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "sub-file.txt"},
		{"git", "commit", "-m", "sub initial"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subOriginDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Create the parent repo
	parentDir = filepath.Join(base, "parent")
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(parentDir, "seed.txt"), []byte("seed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "seed.txt"},
		{"git", "commit", "-m", "initial"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parentDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Allow local file:// transport and add the submodule
	gitCfg := exec.Command("git", "config", "protocol.file.allow", "always")
	gitCfg.Dir = parentDir
	if out, err := gitCfg.CombinedOutput(); err != nil {
		t.Fatalf("git config protocol.file.allow: %v\n%s", err, out)
	}
	subAdd := exec.Command("git", "submodule", "add", "-q", subOriginDir, "mysub")
	subAdd.Dir = parentDir
	if out, err := subAdd.CombinedOutput(); err != nil {
		t.Fatalf("git submodule add: %v\n%s", err, out)
	}
	subCommit := exec.Command("git", "commit", "-q", "-m", "add submodule")
	subCommit.Dir = parentDir
	if out, err := subCommit.CombinedOutput(); err != nil {
		t.Fatalf("git commit submodule: %v\n%s", err, out)
	}

	return parentDir, subOriginDir
}

// lsTreeEntry returns the raw ls-tree line for a given path at HEAD.
// Returns empty string if the path is not in the tree.
func lsTreeEntry(t *testing.T, dir, path string) string {
	t.Helper()
	cmd := exec.Command("git", "ls-tree", "HEAD", path)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-tree HEAD %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}

// lsTreeSHA extracts the object SHA from a ls-tree line (the 3rd field).
func lsTreeSHA(t *testing.T, entry string) string {
	t.Helper()
	fields := strings.Fields(entry)
	if len(fields) < 3 {
		t.Fatalf("unexpected ls-tree output: %q", entry)
	}
	return fields[2]
}

// lsTreeNames returns all entry names in the HEAD tree.
func lsTreeNames(t *testing.T, dir string) map[string]bool {
	t.Helper()
	cmd := exec.Command("git", "ls-tree", "--name-only", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-tree --name-only HEAD: %v", err)
	}
	names := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			names[line] = true
		}
	}
	return names
}

// Phase 0.4: Commit a new file in the parent while a submodule exists.
// Verify the file appears in HEAD tree and the submodule gitlink entry SHA is unchanged.
func TestSubmoduleParentFiles(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)

	// Record the submodule entry before safegit touches anything
	subEntryBefore := lsTreeEntry(t, parentDir, "mysub")
	if subEntryBefore == "" {
		t.Fatal("submodule entry not found in HEAD tree before test")
	}

	// Create and commit a new file in the parent via safegit
	if err := os.WriteFile(filepath.Join(parentDir, "newfile.txt"), []byte("parent data\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, parentDir, "commit", "-m", "add newfile", "--", "newfile.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	// Check 1: new file is in HEAD tree
	names := lsTreeNames(t, parentDir)
	if !names["newfile.txt"] {
		t.Error("newfile.txt not found in HEAD tree")
	}

	// Check 2: submodule entry is unchanged
	subEntryAfter := lsTreeEntry(t, parentDir, "mysub")
	if subEntryBefore != subEntryAfter {
		t.Errorf("submodule entry changed:\n  before: %s\n  after:  %s", subEntryBefore, subEntryAfter)
	}
}

// Phase 0.5: Make a commit inside the submodule, then use safegit to commit
// the submodule pointer update in the parent.
// Verify git ls-tree HEAD mysub shows the new SHA.
func TestSubmodulePointerBump(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)

	// Record the old submodule SHA
	oldEntry := lsTreeEntry(t, parentDir, "mysub")
	oldSHA := lsTreeSHA(t, oldEntry)

	// Make a new commit inside the submodule (using regular git)
	subDir := filepath.Join(parentDir, "mysub")
	for _, args := range [][]string{
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(subDir, "new-sub-file.txt"), []byte("new sub content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "new-sub-file.txt"},
		{"git", "commit", "-q", "-m", "sub update"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Record the new submodule HEAD SHA
	revParse := exec.Command("git", "rev-parse", "HEAD")
	revParse.Dir = subDir
	newSHABytes, err := revParse.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD in submodule: %v", err)
	}
	newSHA := strings.TrimSpace(string(newSHABytes))

	// Sanity: SHAs should differ
	if oldSHA == newSHA {
		t.Fatal("setup failed: submodule SHA did not change")
	}

	// Commit the submodule pointer bump via safegit
	_, stderr, code := runSafegit(t, parentDir, "commit", "-m", "bump submodule", "--", "mysub")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	// Verify the pointer was updated
	treeSHA := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if treeSHA != newSHA {
		t.Errorf("submodule pointer not updated:\n  expected: %s\n  got:      %s", newSHA, treeSHA)
	}
}

// Phase 0.6: One concurrent safegit bumps the submodule pointer while another
// commits a parent file. Verify both land, pointer is correct, total commits = 4.
func TestSubmoduleConcurrentBump(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)

	// Set CAS max attempts high for concurrent test
	runSafegit(t, parentDir, "config", "set", "commit.casMaxAttempts", "50")

	// Make a new commit inside the submodule
	subDir := filepath.Join(parentDir, "mysub")
	for _, args := range [][]string{
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(subDir, "updated.txt"), []byte("updated sub content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "updated.txt"},
		{"git", "commit", "-q", "-m", "sub update"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Record the new submodule HEAD SHA
	revParse := exec.Command("git", "rev-parse", "HEAD")
	revParse.Dir = subDir
	newSHABytes, err := revParse.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD in submodule: %v", err)
	}
	newSubSHA := strings.TrimSpace(string(newSHABytes))

	// Create the parent file for the second concurrent commit
	if err := os.WriteFile(filepath.Join(parentDir, "newfile.txt"), []byte("new parent data\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Launch both concurrent safegit commits
	stdouts := make([]string, 2)
	stderrs := make([]string, 2)
	codes := make([]int, 2)

	parallel(2, func(i int) {
		switch i {
		case 0:
			stdouts[0], stderrs[0], codes[0] = runSafegit(t, parentDir, "commit", "-m", "bump sub", "--", "mysub")
		case 1:
			stdouts[1], stderrs[1], codes[1] = runSafegit(t, parentDir, "commit", "-m", "add file", "--", "newfile.txt")
		}
	})

	if codes[0] != 0 {
		t.Fatalf("submodule bump failed (code %d): %s", codes[0], stderrs[0])
	}
	if codes[1] != 0 {
		t.Fatalf("file commit failed (code %d): %s", codes[1], stderrs[1])
	}

	// Check 1: final tree has updated submodule SHA
	finalSubSHA := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if finalSubSHA != newSubSHA {
		t.Errorf("submodule pointer not updated:\n  expected: %s\n  got:      %s", newSubSHA, finalSubSHA)
	}

	// Check 2: newfile.txt exists in the final tree
	names := lsTreeNames(t, parentDir)
	if !names["newfile.txt"] {
		t.Error("newfile.txt missing from final tree")
	}

	// Check 3: 4 commits total (initial + add-submodule + bump + file)
	count := gitLog(t, parentDir, "HEAD")
	if count != 4 {
		t.Errorf("expected 4 commits, got %d", count)
	}
}

var submoduleEnv = []string{"CLAUDE_CODE_SESSION_ID=submodule-test"}

// Phase 1.4: Scrub a file in the parent repo while a submodule exists.
// Verify the submodule gitlink SHA is identical before and after scrub.
func TestScrubFilePreservesGitlink(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)

	// Record the submodule gitlink entry before scrub
	subEntryBefore := lsTreeEntry(t, parentDir, "mysub")
	if subEntryBefore == "" {
		t.Fatal("submodule entry not found in HEAD tree before test")
	}

	// Add a file with secret content, commit with safegit
	if err := os.WriteFile(filepath.Join(parentDir, "secret.txt"), []byte("secret content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv, "commit", "-m", "add secret", "--", "secret.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	shas := revListReverse(t, parentDir)
	initialSHA := shas[0]

	// Modify the file on disk to contain clean content (scrub replaces with on-disk content)
	if err := os.WriteFile(filepath.Join(parentDir, "secret.txt"), []byte("clean content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Run scrub file
	_, stderr, code = runSafegitEnv(t, parentDir, submoduleEnv, "--force", "scrub", "file", "--from", initialSHA, "--reason", "test gitlink preservation", "secret.txt")
	if code != 0 {
		t.Fatalf("scrub file failed (code %d): %s", code, stderr)
	}

	// Verify the submodule gitlink SHA is identical after scrub
	subEntryAfter := lsTreeEntry(t, parentDir, "mysub")
	subSHABefore := lsTreeSHA(t, subEntryBefore)
	subSHAAfter := lsTreeSHA(t, subEntryAfter)
	if subSHABefore != subSHAAfter {
		t.Errorf("submodule gitlink SHA changed after scrub file:\n  before: %s\n  after:  %s", subSHABefore, subSHAAfter)
	}

	// Verify the scrubbed file has the clean content
	headSHA := revParseHEAD(t, parentDir)
	content, ok := gitShow(t, parentDir, headSHA, "secret.txt")
	if !ok {
		t.Error("secret.txt not found in HEAD after scrub")
	} else if content != "clean content\n" {
		t.Errorf("secret.txt = %q, want %q", content, "clean content\n")
	}
}

// Phase 1.5: Scrub match in the parent repo while a submodule exists.
// Verify the submodule gitlink SHA is identical before and after, and the
// file content is replaced.
func TestScrubMatchPreservesGitlink(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)

	// Record the submodule gitlink entry before scrub
	subEntryBefore := lsTreeEntry(t, parentDir, "mysub")
	if subEntryBefore == "" {
		t.Fatal("submodule entry not found in HEAD tree before test")
	}

	// Add a file with secret content, commit with safegit
	if err := os.WriteFile(filepath.Join(parentDir, "secret.txt"), []byte("SUPERSECRET123\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv, "commit", "-m", "add secret", "--", "secret.txt")
	if code != 0 {
		t.Fatalf("safegit commit failed (code %d): %s", code, stderr)
	}

	// Run scrub match
	_, stderr, code = runSafegitEnv(t, parentDir, submoduleEnv,
		"--force", "scrub", "match",
		"--pattern", "SUPERSECRET123",
		"--replace", "REDACTED",
		"--reason", "test gitlink preservation",
		"--entire-history",
	)
	if code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	// Verify the submodule gitlink SHA is identical after scrub
	subEntryAfter := lsTreeEntry(t, parentDir, "mysub")
	subSHABefore := lsTreeSHA(t, subEntryBefore)
	subSHAAfter := lsTreeSHA(t, subEntryAfter)
	if subSHABefore != subSHAAfter {
		t.Errorf("submodule gitlink SHA changed after scrub match:\n  before: %s\n  after:  %s", subSHABefore, subSHAAfter)
	}

	// Verify the file content is now REDACTED
	headSHA := revParseHEAD(t, parentDir)
	content, ok := gitShow(t, parentDir, headSHA, "secret.txt")
	if !ok {
		t.Error("secret.txt not found in HEAD after scrub match")
	} else if !strings.Contains(content, "REDACTED") {
		t.Errorf("secret.txt should contain REDACTED, got: %q", content)
	}
	if strings.Contains(content, "SUPERSECRET123") {
		t.Errorf("secret.txt still contains SUPERSECRET123: %q", content)
	}
}

// Phase 1.6: Attempt to scrub a path that starts with a gitlink (mysub/somefile.txt).
// The operation should either no-op cleanly (exit 0, no changes) or error meaningfully.
// It must not crash or corrupt the tree.
func TestScrubFileGitlinkPath(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)

	// Record tree state before scrub
	subEntryBefore := lsTreeEntry(t, parentDir, "mysub")
	headBefore := revParseHEAD(t, parentDir)

	shas := revListReverse(t, parentDir)
	firstSHA := shas[0]

	// Create the file on disk so scrub has something to read (even though
	// the path traverses a gitlink in the committed tree)
	subFilePath := filepath.Join(parentDir, "mysub", "somefile.txt")
	if err := os.WriteFile(subFilePath, []byte("replacement\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Run scrub file targeting a path inside the submodule
	_, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv, "--force", "scrub", "file", "--from", firstSHA, "--reason", "test gitlink path", "mysub/somefile.txt")

	// Either clean exit (no-op) or meaningful error is acceptable.
	// Crash (signal death, panic) or corruption is not.
	if code > 1 {
		// Code > 1 might indicate a crash/signal; code 1 is a normal error
		t.Logf("scrub exited with code %d: %s (checking for corruption)", code, stderr)
	}

	// Verify: the tree is not corrupted regardless of exit code.
	// The submodule gitlink SHA must be unchanged.
	subEntryAfter := lsTreeEntry(t, parentDir, "mysub")
	subSHABefore := lsTreeSHA(t, subEntryBefore)
	subSHAAfter := lsTreeSHA(t, subEntryAfter)
	if subSHABefore != subSHAAfter {
		t.Errorf("submodule gitlink SHA corrupted after scrub file on gitlink path:\n  before: %s\n  after:  %s", subSHABefore, subSHAAfter)
	}

	// If exit code was 0 (no-op), HEAD should be unchanged
	if code == 0 {
		headAfter := revParseHEAD(t, parentDir)
		if headAfter != headBefore {
			t.Errorf("HEAD changed despite no-op scrub: %s -> %s", headBefore, headAfter)
		}
	}

	// Verify git fsck passes (no corruption)
	fsck := exec.Command("git", "fsck", "--no-dangling")
	fsck.Dir = parentDir
	if out, err := fsck.CombinedOutput(); err != nil {
		t.Errorf("git fsck failed after scrub on gitlink path: %v\n%s", err, out)
	}
}

// Phase 0.7: Two concurrent safegit commits adding different files to the parent
// while submodule exists. Verify both files in tree, submodule entry unchanged,
// total commits = 4.
func TestSubmoduleConcurrentParent(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)

	// Set CAS max attempts high for concurrent test
	runSafegit(t, parentDir, "config", "set", "commit.casMaxAttempts", "50")

	// Record the submodule entry before concurrent commits
	subEntryBefore := lsTreeEntry(t, parentDir, "mysub")

	// Create the files for concurrent commits
	if err := os.WriteFile(filepath.Join(parentDir, "file-a.txt"), []byte("content-a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentDir, "file-b.txt"), []byte("content-b\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Launch two concurrent safegit commits
	stdouts := make([]string, 2)
	stderrs := make([]string, 2)
	codes := make([]int, 2)

	parallel(2, func(i int) {
		switch i {
		case 0:
			stdouts[0], stderrs[0], codes[0] = runSafegit(t, parentDir, "commit", "-m", "add a", "--", "file-a.txt")
		case 1:
			stdouts[1], stderrs[1], codes[1] = runSafegit(t, parentDir, "commit", "-m", "add b", "--", "file-b.txt")
		}
	})

	if codes[0] != 0 {
		t.Fatalf("agent A failed (code %d): %s", codes[0], stderrs[0])
	}
	if codes[1] != 0 {
		t.Fatalf("agent B failed (code %d): %s", codes[1], stderrs[1])
	}

	// Check 1: 4 commits total (initial + add-submodule + 2 concurrent)
	count := gitLog(t, parentDir, "HEAD")
	if count != 4 {
		t.Errorf("expected 4 commits, got %d", count)
	}

	// Check 2: both files exist in the final tree
	names := lsTreeNames(t, parentDir)
	if !names["file-a.txt"] {
		t.Error("file-a.txt missing from final tree")
	}
	if !names["file-b.txt"] {
		t.Error("file-b.txt missing from final tree")
	}

	// Check 3: submodule entry is unchanged
	subEntryAfter := lsTreeEntry(t, parentDir, "mysub")
	if subEntryBefore != subEntryAfter {
		t.Errorf("submodule entry changed:\n  before: %s\n  after:  %s", subEntryBefore, subEntryAfter)
	}
}

// prepSubmoduleForCommit puts the submodule checkout on the "main" branch
// (submodule add leaves it detached) and configures user identity so safegit
// commit can operate inside it. Returns the submodule directory path.
func prepSubmoduleForCommit(t *testing.T, parentDir string) string {
	t.Helper()
	subDir := filepath.Join(parentDir, "mysub")
	for _, args := range [][]string{
		{"git", "checkout", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v in submodule: %v\n%s", args, err, out)
		}
	}
	return subDir
}

// Phase 0.8: Commit a new file inside the submodule using safegit.
// Verify the commit lands on the submodule's branch and the parent is unaffected.
func TestSubmoduleCommitInside(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)

	parentCountBefore := gitLog(t, parentDir, "HEAD")
	subCountBefore := gitLog(t, subDir, "HEAD")

	// Create a new file in the submodule.
	if err := os.WriteFile(filepath.Join(subDir, "new_in_sub.txt"), []byte("hello from sub\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Commit from inside the submodule.
	_, stderr, code := runSafegit(t, subDir, "commit", "-m", "sub commit", "--", "new_in_sub.txt")
	if code != 0 {
		t.Fatalf("safegit commit in submodule failed (code %d): %s", code, stderr)
	}

	// Verify: submodule branch advanced by 1.
	subCountAfter := gitLog(t, subDir, "HEAD")
	if subCountAfter != subCountBefore+1 {
		t.Errorf("submodule commit count: want %d, got %d", subCountBefore+1, subCountAfter)
	}

	// Verify: parent repo is unaffected.
	parentCountAfter := gitLog(t, parentDir, "HEAD")
	if parentCountAfter != parentCountBefore {
		t.Errorf("parent commit count changed: was %d, now %d", parentCountBefore, parentCountAfter)
	}

	// Verify the file is in the submodule's HEAD tree.
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	cmd.Dir = subDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "new_in_sub.txt") {
		t.Errorf("new_in_sub.txt not found in submodule HEAD tree; got:\n%s", out)
	}
}

// Phase 0.9: safegit commit should refuse to operate in a detached HEAD state.
// Submodules are often in detached HEAD after checkout; this documents that
// safegit commit does not support detached HEAD.
func TestSubmoduleCommitDetachedHead(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := filepath.Join(parentDir, "mysub")

	// Configure user identity (needed even for a failing commit attempt).
	for _, args := range [][]string{
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = subDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v in submodule: %v\n%s", args, err, out)
		}
	}

	// Detach HEAD in the submodule.
	cmd := exec.Command("git", "checkout", "--detach")
	cmd.Dir = subDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout --detach: %v\n%s", err, out)
	}

	// Create a new file.
	if err := os.WriteFile(filepath.Join(subDir, "detached.txt"), []byte("detached\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// safegit commit should fail with an error about detached HEAD.
	_, stderr, code := runSafegit(t, subDir, "commit", "-m", "detached commit", "--", "detached.txt")
	if code == 0 {
		t.Fatal("safegit commit in detached HEAD should have failed, but exited 0")
	}

	combined := strings.ToLower(stderr)
	if !strings.Contains(combined, "detach") && !strings.Contains(combined, "branch") {
		t.Errorf("expected error mentioning detached HEAD or branch, got: %s", stderr)
	}
}

// Phase 0.10: Undo inside a submodule reverts the submodule HEAD to the
// pre-commit SHA. The parent repo's recorded pointer to the submodule is
// unchanged (undo only touches the submodule's own refs).
func TestSubmoduleUndo(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)
	env := []string{"CLAUDE_CODE_SESSION_ID=submodule-undo-test"}

	parentSubPointerBefore := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	subHEADBefore := revParseHEAD(t, subDir)

	// Create a file and commit inside the submodule.
	if err := os.WriteFile(filepath.Join(subDir, "undo_me.txt"), []byte("will undo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, subDir, env, "commit", "-m", "commit to undo", "--", "undo_me.txt")
	if code != 0 {
		t.Fatalf("safegit commit in submodule failed (code %d): %s", code, stderr)
	}

	subHEADAfterCommit := revParseHEAD(t, subDir)
	if subHEADAfterCommit == subHEADBefore {
		t.Fatal("submodule HEAD did not advance after commit")
	}

	// Undo from inside the submodule.
	_, stderr, code = runSafegitEnv(t, subDir, env, "undo")
	if code != 0 {
		t.Fatalf("safegit undo in submodule failed (code %d): %s", code, stderr)
	}

	// Verify: submodule HEAD is back to pre-commit SHA.
	subHEADAfterUndo := revParseHEAD(t, subDir)
	if subHEADAfterUndo != subHEADBefore {
		t.Errorf("after undo, submodule HEAD = %s, want %s", subHEADAfterUndo, subHEADBefore)
	}

	// Verify: parent repo's pointer to submodule is unchanged.
	parentSubPointerAfter := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if parentSubPointerAfter != parentSubPointerBefore {
		t.Errorf("parent's submodule pointer changed: was %s, now %s", parentSubPointerBefore, parentSubPointerAfter)
	}
}

// Phase 0.11: Redo inside a submodule restores the commit after undo.
// commit -> undo -> redo should return the submodule HEAD to the post-commit
// SHA. Parent repo remains unaffected throughout.
func TestSubmoduleRedo(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)
	env := []string{"CLAUDE_CODE_SESSION_ID=submodule-redo-test"}

	parentCountBefore := gitLog(t, parentDir, "HEAD")
	parentSubPointerBefore := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))

	// Create a file and commit inside the submodule.
	if err := os.WriteFile(filepath.Join(subDir, "redo_me.txt"), []byte("will redo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, subDir, env, "commit", "-m", "commit for redo", "--", "redo_me.txt")
	if code != 0 {
		t.Fatalf("safegit commit in submodule failed (code %d): %s", code, stderr)
	}
	subHEADAfterCommit := revParseHEAD(t, subDir)

	// Undo.
	_, stderr, code = runSafegitEnv(t, subDir, env, "undo")
	if code != 0 {
		t.Fatalf("safegit undo in submodule failed (code %d): %s", code, stderr)
	}

	// Redo.
	_, stderr, code = runSafegitEnv(t, subDir, env, "redo")
	if code != 0 {
		t.Fatalf("safegit redo in submodule failed (code %d): %s", code, stderr)
	}

	// Verify: submodule HEAD is back to the post-commit SHA.
	subHEADAfterRedo := revParseHEAD(t, subDir)
	if subHEADAfterRedo != subHEADAfterCommit {
		t.Errorf("after redo, submodule HEAD = %s, want %s", subHEADAfterRedo, subHEADAfterCommit)
	}

	// Verify: parent repo is unaffected.
	parentCountAfter := gitLog(t, parentDir, "HEAD")
	if parentCountAfter != parentCountBefore {
		t.Errorf("parent commit count changed: was %d, now %d", parentCountBefore, parentCountAfter)
	}
	parentSubPointerAfter := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if parentSubPointerAfter != parentSubPointerBefore {
		t.Errorf("parent's submodule pointer changed: was %s, now %s", parentSubPointerBefore, parentSubPointerAfter)
	}
}

// Phase 0.12: Concurrent safegit commits in the parent and submodule should
// not block each other. Their .git directories are separate, so lock files
// are independent.
func TestSubmoduleLockIsolation(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)

	// Create files for each commit.
	if err := os.WriteFile(filepath.Join(parentDir, "parent_new.txt"), []byte("parent file\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "sub_new.txt"), []byte("sub file\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var parentCode, subCode int
	var parentStderr, subStderr string

	// Launch both commits concurrently.
	parallel(2, func(i int) {
		if i == 0 {
			_, parentStderr, parentCode = runSafegit(t, parentDir, "commit", "-m", "parent concurrent", "--", "parent_new.txt")
		} else {
			_, subStderr, subCode = runSafegit(t, subDir, "commit", "-m", "sub concurrent", "--", "sub_new.txt")
		}
	})

	if parentCode != 0 {
		t.Fatalf("parent commit failed (code %d): %s", parentCode, parentStderr)
	}
	if subCode != 0 {
		t.Fatalf("submodule commit failed (code %d): %s", subCode, subStderr)
	}

	// Verify: parent has its new file.
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	cmd.Dir = parentDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "parent_new.txt") {
		t.Errorf("parent_new.txt not found in parent HEAD tree; got:\n%s", out)
	}

	// Verify: submodule has its new file.
	cmd = exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
	cmd.Dir = subDir
	out, err = cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "sub_new.txt") {
		t.Errorf("sub_new.txt not found in submodule HEAD tree; got:\n%s", out)
	}
}

// Phase 0.13: Amending a commit in the parent preserves the submodule's
// gitlink entry. The recorded submodule SHA must not change when only a
// regular file is amended.
func TestSubmoduleAmendPreservesGitlink(t *testing.T) {
	t.Parallel()
	parentDir, _ := newRepoWithSubmodule(t)
	env := []string{"CLAUDE_CODE_SESSION_ID=submodule-amend-test"}

	// Record the submodule pointer before the test.
	subPointerBefore := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))

	// Create and commit a file in the parent.
	filePath := filepath.Join(parentDir, "amend_target.txt")
	if err := os.WriteFile(filePath, []byte("original content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegitEnv(t, parentDir, env, "commit", "-m", "will amend", "--", "amend_target.txt")
	if code != 0 {
		t.Fatalf("initial commit failed (code %d): %s", code, stderr)
	}

	// Modify the file and amend.
	if err := os.WriteFile(filePath, []byte("amended content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runSafegitEnv(t, parentDir, env, "commit", "--amend", "-m", "amended", "--", "amend_target.txt")
	if code != 0 {
		t.Fatalf("amend commit failed (code %d): %s", code, stderr)
	}

	// Verify: the gitlink entry for the submodule is preserved.
	subPointerAfter := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	if subPointerAfter != subPointerBefore {
		t.Errorf("submodule gitlink changed after amend: was %s, now %s", subPointerBefore, subPointerAfter)
	}

	// Verify: the amended file has the new content in the tree.
	cmd := exec.Command("git", "show", "HEAD:amend_target.txt")
	cmd.Dir = parentDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git show HEAD:amend_target.txt: %v", err)
	}
	if !strings.Contains(string(out), "amended content") {
		t.Errorf("amended content not found in HEAD tree; got: %s", out)
	}
}
