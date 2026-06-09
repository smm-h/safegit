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

// coordGuard runs coord.Check and prints a refusal if dirty.
// Returns exit code 5 if dirty, 0 if clean, or 1 on error.
func coordGuard(flags globalFlags, sgDir, operation string) int {
	ctx := context.Background()
	dirty, err := coord.Check(ctx, sgDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if dirty != nil {
		fmt.Fprint(os.Stderr, dirty.Refuse(operation))
		return 5
	}
	return 0
}

// syncMainIndex runs git read-tree HEAD to keep the main index in sync after tree mutations.
func syncMainIndex(flags globalFlags, op string) {
	ctx := context.Background()
	if err := git.SyncMainIndex(ctx, "HEAD"); err != nil {
		if !flags.quiet {
			fmt.Fprintf(os.Stderr, "warning: failed to sync main index after %s: %v\n", op, err)
		}
	}
}

func runCheckout(flags globalFlags, args []string) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		commandHelp("checkout [git checkout args...]", "Checkout a ref (guarded: checks for uncommitted work).")
	}

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
	oldHead, _ := git.RevParse(ctx, "HEAD")

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

	newHead, _ := git.RevParse(ctx, "HEAD")
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

// pullMode represents the fast-forward merge strategy for pull.
type pullMode int

const (
	pullFFOnly pullMode = iota // --ff-only: fail if not fast-forward
	pullFF                     // --ff: fast-forward if possible, merge commit otherwise
	pullNoFF                   // --no-ff: always create a merge commit
)

func runPull(flags globalFlags, mode pullMode, remote string, branch string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := coordGuard(flags, sgDir, "pull"); code != 0 {
		return code
	}

	ctx := context.Background()

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
	switch mode {
	case pullFFOnly:
		mergeArgs = append(mergeArgs, "--ff-only")
	case pullNoFF:
		mergeArgs = append(mergeArgs, "--no-ff")
	case pullFF:
		// git's default: fast-forward if possible, merge commit otherwise
	}
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
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		commandHelp("merge [git merge args...]", "Merge a branch (guarded: checks for uncommitted work).")
	}

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

	resultSHA, _ := git.RevParse(ctx, "HEAD")
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
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		commandHelp("rebase [git rebase args...]", "Rebase onto upstream (guarded: checks for uncommitted work).")
	}

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
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		commandHelp("reset [git reset args...]", "Reset HEAD (guarded for --hard).")
	}

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
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		commandHelp("bisect [git bisect args...]", "Bisect (guarded: checks for uncommitted work).")
	}

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

// guardedHelp maps guarded passthrough commands to their help descriptions.
var guardedHelp = map[string]string{
	"cherry-pick": "Cherry-pick commits (guarded: checks for uncommitted work).",
	"revert":      "Revert commits (guarded: checks for uncommitted work).",
}

// runGuardedPassthrough runs a coordination check, then passes through to git.
func runGuardedPassthrough(flags globalFlags, gitCmd string, args []string) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		desc := guardedHelp[gitCmd]
		if desc == "" {
			desc = fmt.Sprintf("Guarded wrapper around git %s.", gitCmd)
		}
		commandHelp(fmt.Sprintf("%s [git %s args...]", gitCmd, gitCmd), desc)
	}

	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := coordGuard(flags, sgDir, gitCmd); code != 0 {
		return code
	}

	code := runPassthrough(gitCmd, args)

	syncMainIndex(flags, gitCmd)

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: gitCmd,
		Extra: map[string]interface{}{
			"args": strings.Join(args, " "),
		},
	})
	return code
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
