package oplog

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func setupSafegitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	sgDir := filepath.Join(dir, "safegit")
	os.MkdirAll(sgDir, 0755)
	// Create the log file
	os.WriteFile(filepath.Join(sgDir, "log"), nil, 0644)
	return sgDir
}

func TestAppendAndRead(t *testing.T) {
	sgDir := setupSafegitDir(t)

	entry := Entry{
		Timestamp: time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
		PID:       42,
		Op:        "commit",
		Extra: map[string]interface{}{
			"ref": "refs/heads/main",
			"sha": "abc123",
		},
	}

	if err := Append(sgDir, entry); err != nil {
		t.Fatal(err)
	}

	entries, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	got := entries[0]
	if got.Op != "commit" {
		t.Errorf("Op = %q, want commit", got.Op)
	}
	if got.PID != 42 {
		t.Errorf("PID = %d, want 42", got.PID)
	}
	if ref, ok := got.Extra["ref"].(string); !ok || ref != "refs/heads/main" {
		t.Errorf("Extra[ref] = %v, want refs/heads/main", got.Extra["ref"])
	}
}

func TestAppendAutoFillsTimestampAndPID(t *testing.T) {
	sgDir := setupSafegitDir(t)

	entry := Entry{Op: "test"}
	if err := Append(sgDir, entry); err != nil {
		t.Fatal(err)
	}

	entries, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	got := entries[0]
	if got.Timestamp.IsZero() {
		t.Error("timestamp should be auto-filled")
	}
	if got.PID == 0 {
		t.Error("PID should be auto-filled")
	}
}

func TestAppendRejectsOversizedLine(t *testing.T) {
	sgDir := setupSafegitDir(t)

	// Create an entry with a huge Extra field
	bigValue := strings.Repeat("x", maxLineBytes)
	entry := Entry{
		Op:    "test",
		Extra: map[string]interface{}{"big": bigValue},
	}

	err := Append(sgDir, entry)
	if err == nil {
		t.Fatal("expected error for oversized entry")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention exceeds, got: %v", err)
	}
}

func TestReadEmptyLog(t *testing.T) {
	sgDir := setupSafegitDir(t)

	entries, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0", len(entries))
	}
}

func TestReadNonExistentLog(t *testing.T) {
	dir := t.TempDir()
	sgDir := filepath.Join(dir, "safegit")
	os.MkdirAll(sgDir, 0755)
	// Don't create the log file

	entries, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if entries != nil {
		t.Errorf("expected nil entries for non-existent log, got %v", entries)
	}
}

func TestLastRefUpdate(t *testing.T) {
	sgDir := setupSafegitDir(t)

	// Append several entries
	entries := []Entry{
		{Op: "commit", Extra: map[string]interface{}{"ref": "refs/heads/main", "sha": "aaa"}},
		{Op: "push", Extra: map[string]interface{}{"ref": "refs/heads/main"}},
		{Op: "commit", Extra: map[string]interface{}{"ref": "refs/heads/feature", "sha": "bbb"}},
		{Op: "amend", Extra: map[string]interface{}{"ref": "refs/heads/main", "sha": "ccc"}},
		{Op: "commit", Extra: map[string]interface{}{"ref": "refs/heads/feature", "sha": "ddd"}},
	}
	for _, e := range entries {
		if err := Append(sgDir, e); err != nil {
			t.Fatal(err)
		}
	}

	// Last ref update for main should be the amend with sha=ccc
	got, err := LastRefUpdate(sgDir, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil entry")
	}
	if got.Op != "amend" {
		t.Errorf("Op = %q, want amend", got.Op)
	}
	if sha, _ := got.Extra["sha"].(string); sha != "ccc" {
		t.Errorf("sha = %q, want ccc", sha)
	}

	// Last ref update for feature should be commit with sha=ddd
	got, err = LastRefUpdate(sgDir, "refs/heads/feature")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil entry")
	}
	if sha, _ := got.Extra["sha"].(string); sha != "ddd" {
		t.Errorf("sha = %q, want ddd", sha)
	}

	// Non-existent ref returns nil
	got, err = LastRefUpdate(sgDir, "refs/heads/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent ref")
	}
}

func TestConcurrentAppend(t *testing.T) {
	sgDir := setupSafegitDir(t)

	const goroutines = 20
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			entry := Entry{
				Op:    "commit",
				Extra: map[string]interface{}{"id": id},
			}
			if err := Append(sgDir, entry); err != nil {
				t.Errorf("goroutine %d: %v", id, err)
			}
		}(i)
	}

	wg.Wait()

	entries, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != goroutines {
		t.Errorf("got %d entries, want %d", len(entries), goroutines)
	}
}
