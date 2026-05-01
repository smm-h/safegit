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
		if flags.format == formatJSON {
			emitJSON("unlock", nil, &jsonError{Code: 4, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		return 4
	}

	if len(args) == 0 {
		msg := "usage: safegit unlock <ref> [--force]"
		if flags.format == formatJSON {
			emitJSON("unlock", nil, &jsonError{Code: 2, Message: msg}, nil)
		} else {
			fmt.Fprintln(os.Stderr, msg)
		}
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
		msg := fmt.Sprintf("no lock held on %s", ref)
		if flags.format == formatJSON {
			emitJSON("unlock", nil, &jsonError{Code: 1, Message: msg}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %s\n", msg)
		}
		return 1
	}

	// Unless --force, refuse if holder is alive
	if !flags.force {
		stale, err := lock.IsStale(lp)
		if err != nil {
			msg := fmt.Sprintf("cannot check lock status: %v", err)
			if flags.format == formatJSON {
				emitJSON("unlock", nil, &jsonError{Code: 1, Message: msg}, nil)
			} else {
				fmt.Fprintf(os.Stderr, "error: %s\n", msg)
			}
			return 1
		}
		if !stale {
			msg := fmt.Sprintf("lock on %s is held by a live process; use --force to override", ref)
			if flags.format == formatJSON {
				emitJSON("unlock", nil, &jsonError{Code: 1, Message: msg}, nil)
			} else {
				fmt.Fprintf(os.Stderr, "error: %s\n", msg)
			}
			return 1
		}
	}

	// Release the lock
	if err := lock.ForceRelease(sharedDir, ref); err != nil {
		if flags.format == formatJSON {
			emitJSON("unlock", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		return 1
	}

	if flags.format == formatJSON {
		emitJSON("unlock", map[string]string{"ref": ref, "action": "released"}, nil, nil)
	} else if !flags.quiet {
		fmt.Printf("lock on %s released\n", refShortName(ref))
	}
	return 0
}
