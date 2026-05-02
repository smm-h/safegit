package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
)

func runUnlock(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: safegit unlock <ref> [--force]")
		return 2
	}

	ref := args[0]
	if !strings.HasPrefix(ref, "refs/") {
		ref = "refs/heads/" + ref
	}

	sharedDir := repo.SharedSafegitDir(context.Background(), gitDir)

	// Check if lock exists (locks live under the shared safegit dir)
	lp := filepath.Join(sharedDir, "locks", ref+".lock")
	if _, err := os.Stat(lp); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: no lock held on %s\n", ref)
		return 1
	}

	// Unless --force, refuse if holder is alive
	if !flags.force {
		stale, err := lock.IsStale(lp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot check lock status: %v\n", err)
			return 1
		}
		if !stale {
			fmt.Fprintf(os.Stderr, "error: lock on %s is held by a live process; use --force to override\n", ref)
			return 1
		}
	}

	// Release the lock
	if err := lock.ForceRelease(sharedDir, ref); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if !flags.quiet {
		fmt.Printf("lock on %s released\n", refShortName(ref))
	}
	return 0
}
