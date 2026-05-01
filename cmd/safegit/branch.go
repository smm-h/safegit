package main

import (
	"context"
	"fmt"
	"os"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/repo"
)

func runBranch(flags globalFlags, args []string) int {
	// Parse flags
	deleteFlag := false
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d", "--delete":
			deleteFlag = true
		default:
			positional = append(positional, args[i])
		}
	}

	ctx := context.Background()

	if deleteFlag {
		// safegit branch -d <name>
		if len(positional) == 0 {
			fmt.Fprintln(os.Stderr, "usage: safegit branch -d <name>")
			return 2
		}
		name := positional[0]

		// Check if deleting the current branch -- that would mutate tree
		headRef, _ := git.HeadRef(ctx)
		if headRef == "refs/heads/"+name {
			gitDir := mustGitDir(flags)
			if err := repo.EnsureInitialized(gitDir); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 4
			}
			sgDir := repo.SafegitDir(gitDir)
			if code := coordGuard(flags, sgDir, "branch -d"); code != 0 {
				return code
			}
		}

		stdout, stderr, err := git.Run(ctx, "branch", "-d", name)
		if stdout != "" {
			fmt.Print(stdout)
		}
		if stderr != "" {
			fmt.Fprint(os.Stderr, stderr)
		}
		if err != nil {
			return 1
		}
		return 0
	}

	if len(positional) == 0 {
		// safegit branch -- list branches
		return runPassthrough("branch", nil)
	}

	// safegit branch <name> -- create branch (no coordination needed)
	name := positional[0]
	stdout, stderr, err := git.Run(ctx, "branch", name)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if err != nil {
		return 1
	}
	if !flags.quiet {
		fmt.Printf("branch %s created\n", name)
	}
	return 0
}
