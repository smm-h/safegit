package repo

import (
	"encoding/json"
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

	if err := Init(gitDir); err != nil {
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

func TestInitIdempotent(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	os.MkdirAll(gitDir, 0755)

	Init(gitDir)

	// Second init should succeed (idempotent)
	err := Init(gitDir)
	if err != nil {
		t.Fatalf("expected nil on double init, got: %v", err)
	}
}

func TestEnsureInitialized(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	os.MkdirAll(gitDir, 0755)

	// EnsureInitialized should auto-init when not initialized
	err := EnsureInitialized(gitDir)
	if err != nil {
		t.Fatalf("unexpected error from auto-init: %v", err)
	}
	if !IsInitialized(gitDir) {
		t.Fatal("should be initialized after EnsureInitialized auto-init")
	}

	// Calling again on an already-initialized repo should succeed
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

	Init(gitDir)
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

func TestAutoBumpParent_DefaultNil(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Commit.AutoBumpParent != nil {
		t.Errorf("AutoBumpParent should be nil by default, got %v", *cfg.Commit.AutoBumpParent)
	}
}

func TestAutoBumpParent_LoadWithoutField(t *testing.T) {
	// A config JSON without autoBumpParent should load with nil.
	jsonData := `{
		"schemaVersion": 1,
		"commit": {"casMaxAttempts": 5},
		"lock": {"acquireTimeoutSeconds": 30},
		"hooks": {"preprepush": {"timeoutSeconds": 1800}},
		"push": {"retryAttempts": 3},
		"log": {"maxSizeMB": 100}
	}`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(jsonData), 0644)

	cfg, err := LoadConfigFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Commit.AutoBumpParent != nil {
		t.Errorf("AutoBumpParent should be nil when absent from JSON, got %v", *cfg.Commit.AutoBumpParent)
	}
}

func TestAutoBumpParent_SetTrue(t *testing.T) {
	cfg := DefaultConfig()
	err := SetConfigValue(&cfg, "commit.autoBumpParent", "true")
	if err != nil {
		t.Fatal(err)
	}
	val, err := GetConfigValue(&cfg, "commit.autoBumpParent")
	if err != nil {
		t.Fatal(err)
	}
	boolPtr, ok := val.(*bool)
	if !ok {
		t.Fatalf("expected *bool, got %T", val)
	}
	if boolPtr == nil || !*boolPtr {
		t.Errorf("expected true, got %v", boolPtr)
	}
}

func TestAutoBumpParent_SetFalse(t *testing.T) {
	cfg := DefaultConfig()
	err := SetConfigValue(&cfg, "commit.autoBumpParent", "false")
	if err != nil {
		t.Fatal(err)
	}
	val, err := GetConfigValue(&cfg, "commit.autoBumpParent")
	if err != nil {
		t.Fatal(err)
	}
	boolPtr, ok := val.(*bool)
	if !ok {
		t.Fatalf("expected *bool, got %T", val)
	}
	if boolPtr == nil || *boolPtr {
		t.Errorf("expected false, got %v", boolPtr)
	}
}

func TestAutoBumpParent_SetInvalid(t *testing.T) {
	cfg := DefaultConfig()
	err := SetConfigValue(&cfg, "commit.autoBumpParent", "invalid")
	if err == nil {
		t.Fatal("expected error for invalid value")
	}
	err = SetConfigValue(&cfg, "commit.autoBumpParent", "1")
	if err == nil {
		t.Fatal("expected error for numeric value")
	}
	err = SetConfigValue(&cfg, "commit.autoBumpParent", "TRUE")
	if err == nil {
		t.Fatal("expected error for uppercase TRUE")
	}
}

func TestAutoBumpParent_SaveReload(t *testing.T) {
	cfg := DefaultConfig()
	v := true
	cfg.Commit.AutoBumpParent = &v

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	err := SaveConfigTo(path, &cfg)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadConfigFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Commit.AutoBumpParent == nil {
		t.Fatal("AutoBumpParent should not be nil after reload")
	}
	if !*loaded.Commit.AutoBumpParent {
		t.Errorf("AutoBumpParent should be true after reload, got false")
	}

	// Also test with false
	f := false
	cfg.Commit.AutoBumpParent = &f
	err = SaveConfigTo(path, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadConfigFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Commit.AutoBumpParent == nil {
		t.Fatal("AutoBumpParent should not be nil after reload (false)")
	}
	if *loaded.Commit.AutoBumpParent {
		t.Errorf("AutoBumpParent should be false after reload, got true")
	}
}

func TestAutoBumpParent_NilOmittedFromJSON(t *testing.T) {
	cfg := DefaultConfig()
	// AutoBumpParent is nil by default
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// The JSON should not contain "autoBumpParent"
	var raw map[string]json.RawMessage
	json.Unmarshal(data, &raw)
	var commitRaw map[string]json.RawMessage
	json.Unmarshal(raw["commit"], &commitRaw)
	if _, exists := commitRaw["autoBumpParent"]; exists {
		t.Errorf("autoBumpParent should be omitted from JSON when nil, but found in output: %s", string(data))
	}
}

func TestAutoBumpParent_GetValueNil(t *testing.T) {
	cfg := DefaultConfig()
	val, err := GetConfigValue(&cfg, "commit.autoBumpParent")
	if err != nil {
		t.Fatal(err)
	}
	boolPtr, ok := val.(*bool)
	if !ok {
		t.Fatalf("expected *bool, got %T", val)
	}
	if boolPtr != nil {
		t.Errorf("expected nil *bool for unset config, got %v", *boolPtr)
	}
}
