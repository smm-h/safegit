// Package oplog implements the JSONL operation log.
// Each mutating operation appends one JSON line to .git/safegit/log.
// Uses O_APPEND for atomic writes (lines must be < 4096 bytes for atomicity).
package oplog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// maxLineBytes is the POSIX guarantee for atomic O_APPEND writes.
const maxLineBytes = 4096

// Entry represents a single operation log entry.
type Entry struct {
	Timestamp time.Time              `json:"ts"`
	PID       int                    `json:"pid"`
	Op        string                 `json:"op"`
	Extra     map[string]interface{} `json:"extra,omitempty"`
}

// logPath returns the path to the log file.
func logPath(safegitDir string) string {
	return filepath.Join(safegitDir, "log")
}

// Append writes a single entry to the log file atomically.
// The entry is serialized as a single JSON line. Lines exceeding 4096 bytes
// are rejected to preserve atomic write guarantees.
func Append(safegitDir string, entry Entry) error {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if entry.PID == 0 {
		entry.PID = os.Getpid()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshaling log entry: %w", err)
	}

	line := append(data, '\n')
	if len(line) > maxLineBytes {
		return fmt.Errorf("log entry exceeds %d bytes (%d bytes); truncate message or extra fields", maxLineBytes, len(line))
	}

	lp := logPath(safegitDir)
	f, err := os.OpenFile(lp, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}
	defer f.Close()

	// Advisory lock for extra safety on NFS or non-POSIX filesystems
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking log file: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("writing log entry: %w", err)
	}

	return nil
}

// Read returns all entries from the log file.
func Read(safegitDir string) ([]Entry, error) {
	lp := logPath(safegitDir)
	f, err := os.Open(lp)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening log file: %w", err)
	}
	defer f.Close()

	var entries []Entry
	scanner := bufio.NewScanner(f)
	// Increase buffer size to handle lines up to maxLineBytes
	scanner.Buffer(make([]byte, maxLineBytes), maxLineBytes)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			// Skip malformed lines but continue reading
			continue
		}
		entries = append(entries, e)
	}

	if err := scanner.Err(); err != nil {
		return entries, fmt.Errorf("reading log file: %w", err)
	}

	return entries, nil
}

// LastRefUpdate finds the most recent entry for a given ref with op "commit" or "amend".
// Returns nil if no matching entry is found.
func LastRefUpdate(safegitDir, ref string) (*Entry, error) {
	entries, err := Read(safegitDir)
	if err != nil {
		return nil, err
	}

	// Walk backwards to find the most recent match
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Op != "commit" && e.Op != "amend" {
			continue
		}
		if e.Extra == nil {
			continue
		}
		if entryRef, ok := e.Extra["ref"].(string); ok && entryRef == ref {
			return &e, nil
		}
	}

	return nil, nil
}
