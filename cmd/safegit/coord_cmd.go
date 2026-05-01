package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/smm-h/safegit/internal/coord"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
)

// coordGuard runs coord.Check and prints a refusal if dirty (unless force).
// Returns exit code 5 if dirty, 0 if clean, or 1 on error.
func coordGuard(flags globalFlags, sgDir, operation string) int {
	dirty, err := coord.Check(sgDir)
	if err != nil {
		if flags.format == formatJSON {
			emitJSON(operation, nil, &jsonError{Code: 1, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		return 1
	}
	if dirty != nil && !flags.force {
		if flags.format == formatJSON {
			emitJSON(operation, nil, &jsonError{Code: 5, Message: dirty.Refuse(operation)}, nil)
		} else {
			fmt.Fprint(os.Stderr, dirty.Refuse(operation))
		}
		return 5
	}
	if dirty != nil {
		// --force bypass: log that coordination guard was skipped
		_ = oplog.Append(sgDir, oplog.Entry{
			Op: "coordination_bypassed",
			Extra: map[string]interface{}{
				"operation": operation,
				"modified":  len(dirty.ModifiedFiles),
				"wipLocks":  len(dirty.WipLocks),
			},
		})
	}
	return 0
}

// syncMainIndex runs git read-tree HEAD to keep the main index in sync after tree mutations.
func syncMainIndex(flags globalFlags, op string) {
	if err := git.SyncMainIndex("HEAD"); err != nil {
		if !flags.quiet {
			fmt.Fprintf(os.Stderr, "warning: failed to sync main index after %s: %v\n", op, err)
		}
	}
}

func runCheckout(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := coordGuard(flags, sgDir, "checkout"); code != 0 {
		return code
	}

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: safegit checkout <ref>")
		return 2
	}

	// Capture old HEAD for oplog
	ctx := context.Background()
	oldHead, _ := git.RevParse("HEAD")

	stdout, stderr, err := git.Run(ctx, append([]string{"checkout"}, args...)...)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if err != nil {
		return 1
	}

	syncMainIndex(flags, "checkout")

	newHead, _ := git.RevParse("HEAD")
	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "checkout",
		Extra: map[string]interface{}{
			"ref":  args[0],
			"from": oldHead,
			"to":   newHead,
		},
	})
	return 0
}

func runPull(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := coordGuard(flags, sgDir, "pull"); code != 0 {
		return code
	}

	// Parse optional remote, branch, --ff-only
	ctx := context.Background()
	remote := "origin"
	branch := ""
	ffOnly := true // default to ff-only for safety
	var extra []string

	for _, a := range args {
		switch a {
		case "--ff-only":
			ffOnly = true
		case "--no-ff":
			ffOnly = false
		default:
			extra = append(extra, a)
		}
	}
	if len(extra) >= 1 {
		remote = extra[0]
	}
	if len(extra) >= 2 {
		branch = extra[1]
	}

	// Step 1: fetch
	fetchArgs := []string{"fetch", remote}
	if branch != "" {
		fetchArgs = append(fetchArgs, branch)
	}
	_, stderr, err := git.Run(ctx, fetchArgs...)
	if err != nil {
		fmt.Fprint(os.Stderr, stderr)
		return 1
	}

	// Step 2: merge
	mergeArgs := []string{"merge"}
	if ffOnly {
		mergeArgs = append(mergeArgs, "--ff-only")
	}
	// Merge the fetched ref
	mergeTarget := "FETCH_HEAD"
	mergeArgs = append(mergeArgs, mergeTarget)
	stdout, stderr, err := git.Run(ctx, mergeArgs...)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if err != nil {
		return 1
	}

	syncMainIndex(flags, "pull")

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "pull",
		Extra: map[string]interface{}{
			"remote": remote,
			"branch": branch,
		},
	})
	return 0
}

func runMerge(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := coordGuard(flags, sgDir, "merge"); code != 0 {
		return code
	}

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: safegit merge <branch>")
		return 2
	}

	ctx := context.Background()
	stdout, stderr, err := git.Run(ctx, append([]string{"merge"}, args...)...)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if err != nil {
		return 1
	}

	syncMainIndex(flags, "merge")

	resultSHA, _ := git.RevParse("HEAD")
	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "merge",
		Extra: map[string]interface{}{
			"branch": args[0],
			"result": resultSHA,
		},
	})
	return 0
}

func runRebase(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := coordGuard(flags, sgDir, "rebase"); code != 0 {
		return code
	}

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: safegit rebase <upstream>")
		return 2
	}

	ctx := context.Background()
	stdout, stderr, err := git.Run(ctx, append([]string{"rebase"}, args...)...)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if err != nil {
		return 1
	}

	syncMainIndex(flags, "rebase")

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "rebase",
		Extra: map[string]interface{}{
			"upstream": args[0],
		},
	})
	return 0
}

func runReset(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	sgDir := repo.SafegitDir(gitDir)

	// Only guard --hard resets (those are the tree-mutating ones)
	isHard := false
	for _, a := range args {
		if a == "--hard" {
			isHard = true
			break
		}
	}

	if isHard {
		if code := coordGuard(flags, sgDir, "reset --hard"); code != 0 {
			return code
		}
	}

	ctx := context.Background()
	stdout, stderr, err := git.Run(ctx, append([]string{"reset"}, args...)...)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if err != nil {
		return 1
	}

	if isHard {
		syncMainIndex(flags, "reset")
	}

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "reset",
		Extra: map[string]interface{}{
			"args": strings.Join(args, " "),
		},
	})
	return 0
}

func runBisect(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	sgDir := repo.SafegitDir(gitDir)

	// Guard tree-moving subcommands (good, bad, reset, start with a rev)
	needsGuard := false
	if len(args) > 0 {
		switch args[0] {
		case "good", "bad", "old", "new", "reset", "start":
			needsGuard = true
		}
	}

	if needsGuard {
		if code := coordGuard(flags, sgDir, "bisect"); code != 0 {
			return code
		}
	}

	ctx := context.Background()
	stdout, stderr, err := git.Run(ctx, append([]string{"bisect"}, args...)...)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if err != nil {
		return 1
	}

	syncMainIndex(flags, "bisect")

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "bisect",
		Extra: map[string]interface{}{
			"args": strings.Join(args, " "),
		},
	})
	return 0
}

// runPassthrough executes a git command directly, forwarding all args.
// Used for read-only commands (status, diff, log, show).
func runPassthrough(gitCmd string, args []string) int {
	ctx := context.Background()
	if err := git.RunPassthrough(ctx, append([]string{gitCmd}, args...)...); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}
