// Package lock provides ref-lock primitives for concurrent ref updates.
// Uses O_CREAT|O_EXCL for atomic lock file creation and exponential
// backoff polling (no inotify/kqueue in v1).
package lock

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// RefLock represents an acquired lock on a git ref.
type RefLock struct {
	Ref      string
	LockPath string
}

// backoff steps for polling: 10ms, 20ms, 50ms, 100ms, 200ms, 500ms, capped at 1s.
var backoffSteps = []time.Duration{
	10 * time.Millisecond,
	20 * time.Millisecond,
	50 * time.Millisecond,
	100 * time.Millisecond,
	200 * time.Millisecond,
	500 * time.Millisecond,
	1 * time.Second,
}

// lockDir returns the path to the lock directory for a given ref.
// ref is expected to be like "refs/heads/main" -> locks dir is locks/refs/heads/.
func lockDir(safegitDir, ref string) string {
	return filepath.Join(safegitDir, "locks", filepath.Dir(ref))
}

// lockPath returns the full path to the lock file for a ref.
// e.g. refs/heads/main -> .git/safegit/locks/refs/heads/main.lock
func lockPath(safegitDir, ref string) string {
	dir := lockDir(safegitDir, ref)
	base := filepath.Base(ref) + ".lock"
	return filepath.Join(dir, base)
}

// Acquire attempts to acquire a lock on the given ref.
// It uses O_CREAT|O_EXCL for atomic creation. If the lock is held by a dead
// process, it is automatically replaced. Uses exponential backoff polling
// bounded by timeout.
func Acquire(safegitDir, ref, op string, timeout time.Duration) (*RefLock, error) {
	lp := lockPath(safegitDir, ref)

	// Ensure the lock directory exists
	if err := os.MkdirAll(filepath.Dir(lp), 0755); err != nil {
		return nil, fmt.Errorf("creating lock dir: %w", err)
	}

	deadline := time.Now().Add(timeout)
	step := 0

	for {
		err := tryCreate(lp, op)
		if err == nil {
			return &RefLock{Ref: ref, LockPath: lp}, nil
		}

		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("creating lock file: %w", err)
		}

		// Lock file exists -- check if it's stale
		stale, staleErr := IsStale(lp)
		if staleErr == nil && stale {
			// Owner is dead; remove and retry immediately
			os.Remove(lp)
			continue
		}

		// Not stale -- wait with backoff
		if time.Now().After(deadline) {
			holder := describeHolder(lp)
			return nil, fmt.Errorf("timeout acquiring lock on %s (held by %s)", ref, holder)
		}

		delay := backoffSteps[step]
		if step < len(backoffSteps)-1 {
			step++
		}
		// Don't sleep past deadline
		remaining := time.Until(deadline)
		if delay > remaining {
			delay = remaining
		}
		time.Sleep(delay)
	}
}

// tryCreate atomically creates the lock file with O_CREAT|O_EXCL and writes owner info.
func tryCreate(path, op string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	hostname, _ := os.Hostname()
	content := fmt.Sprintf("pid=%d\nts=%s\nop=%s\nhost=%s\n",
		os.Getpid(),
		time.Now().UTC().Format(time.RFC3339Nano),
		op,
		hostname,
	)
	_, err = f.WriteString(content)
	return err
}

// Release removes the lock file.
func (l *RefLock) Release() error {
	return os.Remove(l.LockPath)
}

// IsStale checks whether the process that holds the lock file is dead.
// Returns (true, nil) if the lock is stale and can be reclaimed.
func IsStale(lockPath string) (bool, error) {
	pid, err := parsePID(lockPath)
	if err != nil {
		return false, err
	}

	// kill(pid, 0) checks existence without sending a signal
	err = syscall.Kill(pid, 0)
	if err == syscall.ESRCH {
		return true, nil // process does not exist
	}
	// EPERM means process exists but we can't signal it -- still alive
	return false, nil
}

// ForceRelease unconditionally removes the lock file for a ref.
func ForceRelease(safegitDir, ref string) error {
	lp := lockPath(safegitDir, ref)
	err := os.Remove(lp)
	if os.IsNotExist(err) {
		return fmt.Errorf("no lock held on %s", ref)
	}
	return err
}

// parsePID reads the lock file and extracts the pid= value.
func parsePID(lockPath string) (int, error) {
	f, err := os.Open(lockPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "pid=") {
			pidStr := strings.TrimPrefix(line, "pid=")
			pid, err := strconv.Atoi(pidStr)
			if err != nil {
				return 0, fmt.Errorf("invalid pid in lock file: %q", pidStr)
			}
			return pid, nil
		}
	}
	return 0, fmt.Errorf("no pid= line in lock file %s", lockPath)
}

// describeHolder returns a human-readable description of the lock holder.
func describeHolder(lockPath string) string {
	f, err := os.Open(lockPath)
	if err != nil {
		return "unknown"
	}
	defer f.Close()

	var pid, op, host string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "pid="):
			pid = strings.TrimPrefix(line, "pid=")
		case strings.HasPrefix(line, "op="):
			op = strings.TrimPrefix(line, "op=")
		case strings.HasPrefix(line, "host="):
			host = strings.TrimPrefix(line, "host=")
		}
	}
	return fmt.Sprintf("pid=%s op=%s host=%s", pid, op, host)
}
