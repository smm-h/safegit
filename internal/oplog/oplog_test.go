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

func TestAppendAutoFillsSessionID(t *testing.T) {
	sgDir := setupSafegitDir(t)

	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-abc-123")

	entry := Entry{Op: "commit"}
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
	if entries[0].SessionID != "sess-abc-123" {
		t.Errorf("SessionID = %q, want sess-abc-123", entries[0].SessionID)
	}
}

func TestAppendSessionIDEmptyWithoutEnv(t *testing.T) {
	sgDir := setupSafegitDir(t)

	t.Setenv("CLAUDE_CODE_SESSION_ID", "")

	entry := Entry{Op: "commit"}
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
	if entries[0].SessionID != "" {
		t.Errorf("SessionID = %q, want empty string", entries[0].SessionID)
	}
}

func TestSessionIDJSONRoundTrip(t *testing.T) {
	sgDir := setupSafegitDir(t)

	// Write a raw JSON line without "sid" field to simulate an old entry
	logFile := filepath.Join(sgDir, "log")
	oldJSON := `{"ts":"2026-04-26T12:00:00Z","pid":42,"op":"commit","extra":{"ref":"refs/heads/main","sha":"aaa"}}` + "\n"
	if err := os.WriteFile(logFile, []byte(oldJSON), 0644); err != nil {
		t.Fatal(err)
	}

	entries, err := Read(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].SessionID != "" {
		t.Errorf("SessionID = %q, want empty string for old entry without sid", entries[0].SessionID)
	}
	if entries[0].Op != "commit" {
		t.Errorf("Op = %q, want commit", entries[0].Op)
	}
}

func TestLastRefUpdateForSession(t *testing.T) {
	sgDir := setupSafegitDir(t)

	// Entries from two different sessions
	entries := []Entry{
		{Op: "commit", SessionID: "session-A", Extra: map[string]interface{}{"ref": "refs/heads/main", "sha": "aaa"}},
		{Op: "commit", SessionID: "session-B", Extra: map[string]interface{}{"ref": "refs/heads/main", "sha": "bbb"}},
		{Op: "amend", SessionID: "session-A", Extra: map[string]interface{}{"ref": "refs/heads/main", "sha": "ccc"}},
		{Op: "commit", SessionID: "session-B", Extra: map[string]interface{}{"ref": "refs/heads/main", "sha": "ddd"}},
	}
	for _, e := range entries {
		if err := Append(sgDir, e); err != nil {
			t.Fatal(err)
		}
	}

	// Session A's last ref update should be the amend with sha=ccc
	got, err := LastRefUpdateForSession(sgDir, "refs/heads/main", "session-A")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil entry for session-A")
	}
	if got.Op != "amend" {
		t.Errorf("Op = %q, want amend", got.Op)
	}
	if sha, _ := got.Extra["sha"].(string); sha != "ccc" {
		t.Errorf("sha = %q, want ccc", sha)
	}
	if got.SessionID != "session-A" {
		t.Errorf("SessionID = %q, want session-A", got.SessionID)
	}

	// Session B's last ref update should be commit with sha=ddd
	got, err = LastRefUpdateForSession(sgDir, "refs/heads/main", "session-B")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil entry for session-B")
	}
	if sha, _ := got.Extra["sha"].(string); sha != "ddd" {
		t.Errorf("sha = %q, want ddd", sha)
	}
}

func TestLastRefUpdateForSessionNoMatch(t *testing.T) {
	sgDir := setupSafegitDir(t)

	// Only entries from session-A
	entry := Entry{Op: "commit", SessionID: "session-A", Extra: map[string]interface{}{"ref": "refs/heads/main", "sha": "aaa"}}
	if err := Append(sgDir, entry); err != nil {
		t.Fatal(err)
	}

	// Query for session-B should return nil
	got, err := LastRefUpdateForSession(sgDir, "refs/heads/main", "session-B")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for non-matching session, got %+v", got)
	}

	// Query for non-existent ref should also return nil
	got, err = LastRefUpdateForSession(sgDir, "refs/heads/nonexistent", "session-A")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for non-existent ref, got %+v", got)
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
