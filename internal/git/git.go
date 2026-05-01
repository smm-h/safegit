// Package git wraps os/exec calls to the git binary.
// All functions shell out to git plumbing commands and return structured results.
package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Run executes a git command and returns stdout, stderr, and any error.
func Run(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	return RunWithEnv(ctx, nil, args...)
}

// RunWithEnv executes a git command with additional environment variables.
func RunWithEnv(ctx context.Context, env []string, args ...string) (stdout, stderr string, err error) {
	// Prepend --no-optional-locks to avoid contention on .git/index.lock
	fullArgs := append([]string{"--no-optional-locks"}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	err = cmd.Run()
	stdout = outBuf.String()
	stderr = errBuf.String()

	if err != nil {
		err = fmt.Errorf("git %s: %w\nstderr: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr))
	}
	return
}

// RunWithEnvStdin executes a git command with environment variables and stdin data.
func RunWithEnvStdin(ctx context.Context, env []string, stdin []byte, args ...string) (stdout, stderr string, err error) {
	fullArgs := append([]string{"--no-optional-locks"}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	cmd.Stdin = bytes.NewReader(stdin)

	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	err = cmd.Run()
	stdout = outBuf.String()
	stderr = errBuf.String()

	if err != nil {
		err = fmt.Errorf("git %s: %w\nstderr: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr))
	}
	return
}

// RepoRoot returns the absolute path to the repository root.
func RepoRoot() (string, error) {
	ctx := context.Background()
	out, _, err := Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// GitDir returns the path to the .git directory.
func GitDir() (string, error) {
	ctx := context.Background()
	out, _, err := Run(ctx, "rev-parse", "--git-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ErrDetachedHead is returned when HEAD is not on a branch.
var ErrDetachedHead = fmt.Errorf("HEAD is detached (not on a branch); check out a branch first or use --branch")

// HeadRef returns the current branch ref (e.g. "refs/heads/main").
// Returns ErrDetachedHead if HEAD is not on a branch.
func HeadRef() (string, error) {
	ctx := context.Background()
	out, _, err := Run(ctx, "symbolic-ref", "HEAD")
	if err != nil {
		return "", ErrDetachedHead
	}
	return strings.TrimSpace(out), nil
}

// RevParse resolves a revision to a full SHA.
func RevParse(rev string) (string, error) {
	ctx := context.Background()
	out, _, err := Run(ctx, "rev-parse", rev)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ReadTree populates a temporary index from a treeish (commit/tree SHA or ref).
func ReadTree(indexPath, treeish string) error {
	ctx := context.Background()
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	_, _, err := RunWithEnv(ctx, env, "read-tree", treeish)
	return err
}

// WriteTree writes the index content as a tree object, returns the tree SHA.
func WriteTree(indexPath string) (string, error) {
	ctx := context.Background()
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	out, _, err := RunWithEnv(ctx, env, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitTree creates a commit object from a tree SHA and parent, returns commit SHA.
// If parentSHA is empty, creates a root commit.
func CommitTree(treeSHA, parentSHA, message string) (string, error) {
	ctx := context.Background()
	args := []string{"commit-tree", treeSHA, "-m", message}
	if parentSHA != "" {
		args = []string{"commit-tree", treeSHA, "-p", parentSHA, "-m", message}
	}
	out, _, err := Run(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// UpdateRef atomically updates a ref using compare-and-swap.
// oldSHA is the expected current value; if empty, the ref must not exist.
func UpdateRef(ref, newSHA, oldSHA string) error {
	ctx := context.Background()
	args := []string{"update-ref", ref, newSHA}
	if oldSHA != "" {
		args = append(args, oldSHA)
	}
	_, _, err := Run(ctx, args...)
	return err
}

// AddFile stages a file into a custom index.
func AddFile(indexPath, filePath string) error {
	ctx := context.Background()
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	_, _, err := RunWithEnv(ctx, env, "add", "--", filePath)
	return err
}

// RmCached removes a file from a custom index without touching the working tree.
func RmCached(indexPath, filePath string) error {
	ctx := context.Background()
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	_, _, err := RunWithEnv(ctx, env, "rm", "--cached", "--", filePath)
	return err
}

// IsTracked checks whether a file is tracked by git (present in HEAD tree).
// Uses cat-file instead of ls-files because safegit never writes to the main
// index -- files committed via safegit exist in HEAD but not in .git/index.
func IsTracked(filePath string) (bool, error) {
	ctx := context.Background()
	_, _, err := Run(ctx, "cat-file", "-e", "HEAD:"+filePath)
	if err != nil {
		// Non-zero exit means the object doesn't exist in HEAD
		return false, nil
	}
	return true, nil
}

// SyncMainIndex updates the main .git/index to match the given treeish.
// This makes git status/diff reflect the committed state after safegit commits.
func SyncMainIndex(treeish string) error {
	ctx := context.Background()
	_, _, err := Run(ctx, "read-tree", treeish)
	return err
}

// RunPassthrough executes a git command with stdin/stdout/stderr wired to
// the terminal (os.Stdin, os.Stdout, os.Stderr). It prepends --no-optional-locks
// like Run, but does not capture output -- suitable for interactive/pager commands.
func RunPassthrough(ctx context.Context, args ...string) error {
	fullArgs := append([]string{"--no-optional-locks"}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// CommonGitDir returns the path to the shared .git directory.
// For normal repos this equals GitDir(); for worktrees it returns the main
// .git dir that is shared across all worktrees. Lock files should live here
// so that worktrees committing to the same branch serialize correctly.
func CommonGitDir() (string, error) {
	ctx := context.Background()
	out, _, err := Run(ctx, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// IsIgnored checks whether a file matches a gitignore rule.
func IsIgnored(filePath string) (bool, error) {
	ctx := context.Background()
	_, _, err := Run(ctx, "check-ignore", "-q", "--", filePath)
	if err != nil {
		// Exit code 1 means not ignored; exit code 128 means path error
		return false, nil
	}
	return true, nil
}

// CommitMessage returns the full commit message of the given revision.
func CommitMessage(rev string) (string, error) {
	ctx := context.Background()
	out, _, err := Run(ctx, "log", "-1", "--format=%B", rev)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}
