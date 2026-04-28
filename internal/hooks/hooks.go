// Package hooks discovers and executes pre-pre-push hooks.
// Hooks run BEFORE any network I/O, solving the SSH timeout problem
// when pre-push checks are long-running.
package hooks

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// HookResult holds the outcome of running a single hook.
type HookResult struct {
	Name     string        `json:"name"`
	ExitCode int           `json:"exitCode"`
	Duration time.Duration `json:"duration"`
	TimedOut bool          `json:"timedOut,omitempty"`
}

// Discover finds pre-pre-push hooks in .git/hooks/.
// Returns executable hook paths in execution order:
// 1. .git/hooks/pre-pre-push (single file)
// 2. .git/hooks/pre-pre-push.d/* (lexical order, skip dot-prefixed and tilde-suffixed)
func Discover(gitDir string) ([]string, error) {
	hooksDir := filepath.Join(gitDir, "hooks")
	var hooks []string

	// Single-file hook
	single := filepath.Join(hooksDir, "pre-pre-push")
	if info, err := os.Stat(single); err == nil && !info.IsDir() {
		if isExecutable(info) {
			hooks = append(hooks, single)
		} else {
			fmt.Fprintf(os.Stderr, "warning: %s exists but is not executable, skipping\n", single)
		}
	}

	// Directory-based hooks
	dirPath := filepath.Join(hooksDir, "pre-pre-push.d")
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		// Directory doesn't exist -- that's fine
		if os.IsNotExist(err) {
			return hooks, nil
		}
		return hooks, fmt.Errorf("reading pre-pre-push.d: %w", err)
	}

	// Collect and sort lexically
	var names []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") {
			continue
		}
		if e.IsDir() {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		p := filepath.Join(dirPath, name)
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if isExecutable(info) {
			hooks = append(hooks, p)
		} else {
			fmt.Fprintf(os.Stderr, "warning: %s is not executable, skipping\n", p)
		}
	}

	return hooks, nil
}

// Run executes all discovered hooks sequentially with the given stdin.
// On non-zero exit, remaining hooks are skipped. On timeout: SIGTERM, 5s grace, SIGKILL.
func Run(ctx context.Context, gitDir string, stdin []byte, timeoutSec int, env []string) ([]HookResult, error) {
	hooks, err := Discover(gitDir)
	if err != nil {
		return nil, err
	}
	if len(hooks) == 0 {
		return nil, nil
	}

	var results []HookResult
	for _, hookPath := range hooks {
		result := runOne(ctx, hookPath, stdin, timeoutSec, env)
		results = append(results, result)
		if result.ExitCode != 0 {
			break // abort on first failure
		}
	}
	return results, nil
}

// runOne executes a single hook, streaming stdout/stderr to os.Stdout/os.Stderr.
// Respects timeout: SIGTERM then SIGKILL after 5s grace.
func runOne(ctx context.Context, hookPath string, stdin []byte, timeoutSec int, env []string) HookResult {
	name := filepath.Base(hookPath)
	start := time.Now()

	cmd := exec.Command(hookPath)
	cmd.Stdin = strings.NewReader(string(stdin))
	cmd.Stderr = os.Stderr
	cmd.Dir = filepath.Dir(hookPath)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	// Set process group so we can signal the entire group
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Pipe stdout to check the first line for timeout override
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return HookResult{Name: name, ExitCode: 1, Duration: time.Since(start)}
	}

	if err := cmd.Start(); err != nil {
		return HookResult{Name: name, ExitCode: 1, Duration: time.Since(start)}
	}

	// Stream stdout in a goroutine. Check the first line for timeout override
	// and signal the effective timeout via channel.
	timeoutCh := make(chan int, 1)
	ioDone := make(chan struct{})
	go func() {
		defer close(ioDone)
		reader := bufio.NewReader(stdoutPipe)
		firstLine, err := reader.ReadString('\n')
		if err == nil {
			override := parseTimeoutOverride(firstLine)
			if override > 0 {
				timeoutCh <- override
			} else {
				timeoutCh <- 0
				fmt.Fprint(os.Stdout, firstLine)
			}
		} else {
			timeoutCh <- 0
			if firstLine != "" {
				fmt.Fprint(os.Stdout, firstLine)
			}
		}
		// Stream remaining stdout
		io.Copy(os.Stdout, reader)
	}()

	// Determine effective timeout: use override if received quickly, else default
	effectiveTimeout := timeoutSec
	select {
	case override := <-timeoutCh:
		if override > 0 {
			effectiveTimeout = override
		}
	case <-time.After(2 * time.Second):
		// Hook hasn't printed anything in 2s -- use default timeout
	}

	// Wait for process completion with timeout
	procDone := make(chan error, 1)
	go func() {
		procDone <- cmd.Wait()
	}()

	timeout := time.Duration(effectiveTimeout) * time.Second
	select {
	case err := <-procDone:
		<-ioDone // wait for IO streaming to finish
		duration := time.Since(start)
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return HookResult{Name: name, ExitCode: exitErr.ExitCode(), Duration: duration}
			}
			return HookResult{Name: name, ExitCode: 1, Duration: duration}
		}
		return HookResult{Name: name, ExitCode: 0, Duration: duration}

	case <-time.After(timeout):
		// Timeout: SIGTERM the process group
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}

		// Grace period: 5 seconds
		select {
		case <-procDone:
			// Terminated gracefully
		case <-time.After(5 * time.Second):
			// SIGKILL the process group
			if cmd.Process != nil {
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			<-procDone
		}
		<-ioDone

		return HookResult{Name: name, ExitCode: 21, Duration: time.Since(start), TimedOut: true}

	case <-ctx.Done():
		// Parent context cancelled
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-procDone
		<-ioDone
		return HookResult{Name: name, ExitCode: 1, Duration: time.Since(start)}
	}
}

// parseTimeoutOverride checks if a line is "# safegit: timeout=NNN" and returns the value.
// Returns 0 if not a valid override.
func parseTimeoutOverride(line string) int {
	line = strings.TrimSpace(line)
	const prefix = "# safegit: timeout="
	if !strings.HasPrefix(line, prefix) {
		return 0
	}
	valStr := strings.TrimPrefix(line, prefix)
	val, err := strconv.Atoi(valStr)
	if err != nil || val <= 0 {
		return 0
	}
	return val
}

// isExecutable checks if a file has any execute permission bit set.
func isExecutable(info os.FileInfo) bool {
	return info.Mode()&0111 != 0
}

// Install copies a hook file to .git/hooks/ and makes it executable.
func Install(gitDir, srcPath string) error {
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return fmt.Errorf("creating hooks dir: %w", err)
	}

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("reading hook file: %w", err)
	}

	destName := filepath.Base(srcPath)
	dest := filepath.Join(hooksDir, destName)
	if err := os.WriteFile(dest, data, 0755); err != nil {
		return fmt.Errorf("writing hook file: %w", err)
	}
	return nil
}

// InstallPlaceholder writes a no-op pre-pre-push hook if one doesn't already exist.
func InstallPlaceholder(gitDir string) error {
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return fmt.Errorf("creating hooks dir: %w", err)
	}

	dest := filepath.Join(hooksDir, "pre-pre-push")
	if _, err := os.Stat(dest); err == nil {
		// Already exists, don't overwrite
		return nil
	}

	placeholder := `#!/bin/sh
# Installed by safegit init. This is a no-op placeholder.
# Add your pre-push validators here, or use .git/hooks/pre-pre-push.d/
exit 0
`
	if err := os.WriteFile(dest, []byte(placeholder), 0755); err != nil {
		return fmt.Errorf("writing placeholder hook: %w", err)
	}
	return nil
}
