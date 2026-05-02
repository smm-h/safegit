package repo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafegitDir(t *testing.T) {
	got := SafegitDir("/foo/.git")
	want := filepath.Join("/foo/.git", "safegit")
	if got != want {
		t.Errorf("SafegitDir = %q, want %q", got, want)
	}
}

func TestInitAndIsInitialized(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	os.MkdirAll(gitDir, 0755)

	if IsInitialized(gitDir) {
		t.Fatal("should not be initialized before Init")
	}

	if err := Init(gitDir, false); err != nil {
		t.Fatal(err)
	}

	if !IsInitialized(gitDir) {
		t.Fatal("should be initialized after Init")
	}

	// Verify directory structure
	sgDir := SafegitDir(gitDir)
	expectedDirs := []string{
		filepath.Join(sgDir, "locks", "refs", "heads"),
		filepath.Join(sgDir, "tmp"),
	}
	for _, d := range expectedDirs {
		if stat, err := os.Stat(d); err != nil || !stat.IsDir() {
			t.Errorf("expected directory %s to exist", d)
		}
	}

	// Verify config.json
	cfg, err := LoadConfig(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SchemaVersion != 1 {
		t.Errorf("schema version = %d, want 1", cfg.SchemaVersion)
	}
	if cfg.Commit.CASMaxAttempts != 5 {
		t.Errorf("CASMaxAttempts = %d, want 5", cfg.Commit.CASMaxAttempts)
	}

	// Verify log file exists
	logPath := filepath.Join(sgDir, "log")
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		t.Error("log file should exist")
	}
}

func TestInitAlreadyInitialized(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	os.MkdirAll(gitDir, 0755)

	Init(gitDir, false)

	// Second init without force should fail
	err := Init(gitDir, false)
	if err == nil {
		t.Fatal("expected error on double init without --force")
	}

	// With force should succeed
	if err := Init(gitDir, true); err != nil {
		t.Fatalf("init --force should succeed: %v", err)
	}
}

func TestEnsureInitialized(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	os.MkdirAll(gitDir, 0755)

	err := EnsureInitialized(gitDir)
	if err == nil {
		t.Fatal("expected error when not initialized")
	}

	Init(gitDir, false)

	err = EnsureInitialized(gitDir)
	if err != nil {
		t.Fatalf("unexpected error after init: %v", err)
	}
}

func TestUninstall(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	os.MkdirAll(gitDir, 0755)

	// Uninstall when not initialized
	err := Uninstall(gitDir)
	if err == nil {
		t.Fatal("expected error uninstalling when not initialized")
	}

	Init(gitDir, false)
	err = Uninstall(gitDir)
	if err != nil {
		t.Fatal(err)
	}

	if IsInitialized(gitDir) {
		t.Fatal("should not be initialized after uninstall")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", cfg.SchemaVersion)
	}
	if cfg.Lock.AcquireTimeoutSeconds != 30 {
		t.Errorf("AcquireTimeoutSeconds = %d, want 30", cfg.Lock.AcquireTimeoutSeconds)
	}
	if cfg.Hooks.PrePrePush.TimeoutSeconds != 1800 {
		t.Errorf("PrePrePush.TimeoutSeconds = %d, want 1800", cfg.Hooks.PrePrePush.TimeoutSeconds)
	}
	if cfg.Push.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %d, want 3", cfg.Push.RetryAttempts)
	}
	if cfg.Log.MaxSizeMB != 100 {
		t.Errorf("MaxSizeMB = %d, want 100", cfg.Log.MaxSizeMB)
	}
}
