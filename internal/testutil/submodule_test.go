package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInitRepoWithSubmodule(t *testing.T) {
	sr := InitRepoWithSubmodule(t)

	for _, dir := range []struct {
		name, path string
	}{
		{"ParentDir", sr.ParentDir},
		{"ParentGitDir", sr.ParentGitDir},
		{"SubDir", sr.SubDir},
		{"SubGitDir", sr.SubGitDir},
		{"SubOriginDir", sr.SubOriginDir},
	} {
		info, err := os.Stat(dir.path)
		if err != nil {
			t.Errorf("%s: %v", dir.name, err)
		} else if !info.IsDir() {
			t.Errorf("%s: not a directory", dir.name)
		}
	}

	if sr.SubName != "mysub" {
		t.Errorf("SubName = %q, want %q", sr.SubName, "mysub")
	}

	if sr.SubDir != filepath.Join(sr.ParentDir, "mysub") {
		t.Errorf("SubDir = %q, want %q", sr.SubDir, filepath.Join(sr.ParentDir, "mysub"))
	}

	// Verify the submodule file exists
	content, err := os.ReadFile(filepath.Join(sr.SubDir, "sub-file.txt"))
	if err != nil {
		t.Fatalf("reading sub-file.txt: %v", err)
	}
	if string(content) != "sub-file.txt\n" {
		t.Errorf("sub-file.txt content = %q, want %q", content, "sub-file.txt\n")
	}

	// Verify git status in submodule is clean
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = sr.SubDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status in submodule: %v\n%s", err, out)
	}
	if len(out) != 0 {
		t.Errorf("submodule has dirty status: %s", out)
	}

	// Verify git status in parent is clean
	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = sr.ParentDir
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status in parent: %v\n%s", err, out)
	}
	if len(out) != 0 {
		t.Errorf("parent has dirty status: %s", out)
	}
}

func TestInitRepoWithTwoSubmodules(t *testing.T) {
	sub1, sub2 := InitRepoWithTwoSubmodules(t)

	if sub1.ParentDir != sub2.ParentDir {
		t.Errorf("ParentDir mismatch: %q vs %q", sub1.ParentDir, sub2.ParentDir)
	}
	if sub1.ParentGitDir != sub2.ParentGitDir {
		t.Errorf("ParentGitDir mismatch: %q vs %q", sub1.ParentGitDir, sub2.ParentGitDir)
	}

	if sub1.SubName != "mysub" {
		t.Errorf("sub1.SubName = %q, want %q", sub1.SubName, "mysub")
	}
	if sub2.SubName != "mysub2" {
		t.Errorf("sub2.SubName = %q, want %q", sub2.SubName, "mysub2")
	}

	if sub1.SubDir == sub2.SubDir {
		t.Error("sub1 and sub2 have the same SubDir")
	}
	if sub1.SubGitDir == sub2.SubGitDir {
		t.Error("sub1 and sub2 have the same SubGitDir")
	}
	if sub1.SubOriginDir == sub2.SubOriginDir {
		t.Error("sub1 and sub2 have the same SubOriginDir")
	}

	// Verify both submodule files exist
	for _, tc := range []struct {
		dir, file string
	}{
		{sub1.SubDir, "sub-file.txt"},
		{sub2.SubDir, "sub2-file.txt"},
	} {
		if _, err := os.Stat(filepath.Join(tc.dir, tc.file)); err != nil {
			t.Errorf("%s/%s: %v", tc.dir, tc.file, err)
		}
	}

	// Verify parent is clean
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = sub1.ParentDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status in parent: %v\n%s", err, out)
	}
	if len(out) != 0 {
		t.Errorf("parent has dirty status: %s", out)
	}
}
