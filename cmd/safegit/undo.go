package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
)

// undoableOps maps op types to the extra key that holds the rollback target SHA.
// commit -> parent (the commit before this one)
// amend  -> oldSha (the commit that was replaced)
// reword -> oldSha (the commit that was replaced)
var undoableOps = map[string]string{
	"commit": "parent",
	"amend":  "oldSha",
	"reword": "oldSha",
}

func runUndo(flags globalFlags, args []string) {
	const cmd = "undo"

	// No sub-flags beyond global flags
	if len(args) > 0 {
		die(flags, cmd, 2, fmt.Sprintf("unknown argument: %s", args[0]))
	}

	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		die(flags, cmd, 4, err.Error())
	}

	sgDir := repo.SafegitDir(gitDir)

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("loading config: %v", err))
	}

	ctx := context.Background()

	// Resolve current branch
	ref, err := git.HeadRef(ctx)
	if err != nil || ref == "" {
		die(flags, cmd, 1, "HEAD is detached; undo requires a branch")
	}

	// Find the last ref-updating oplog entry for this branch
	entry, err := oplog.LastRefUpdate(sgDir, ref)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("reading oplog: %v", err))
	}
	if entry == nil {
		die(flags, cmd, 1, fmt.Sprintf("no operations found for %s in the oplog", refShortName(ref)))
	}

	// Check if the op is undoable
	targetKey, ok := undoableOps[entry.Op]
	if !ok {
		die(flags, cmd, 1, fmt.Sprintf("cannot undo %q", entry.Op))
	}

	// Extract the rollback target SHA
	targetSHA, ok := entry.Extra[targetKey].(string)
	if !ok || targetSHA == "" {
		die(flags, cmd, 1, fmt.Sprintf("oplog entry for %q is missing %q field", entry.Op, targetKey))
	}

	// The current tip SHA (what we expect the ref to point to now)
	currentSHA := oplog.TipSHA(entry.Extra)
	if currentSHA == "" {
		die(flags, cmd, 1, "oplog entry has no resolvable tip SHA")
	}

	if flags.dryRun {
		fmt.Printf("would undo %s on %s\n", entry.Op, refShortName(ref))
		fmt.Printf("  %s -> %s\n", currentSHA[:8], targetSHA[:8])
		return
	}

	// Acquire lock on the ref
	timeout := time.Duration(cfg.Lock.AcquireTimeoutSeconds) * time.Second
	sharedDir := repo.SharedSafegitDir(ctx, gitDir)
	lk, err := lock.Acquire(sharedDir, sgDir, ref, "undo", timeout)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("acquiring lock: %v", err))
	}
	defer lk.Release()

	// CAS update the ref
	if err := git.UpdateRef(ctx, ref, targetSHA, currentSHA); err != nil {
		die(flags, cmd, 1, fmt.Sprintf("update-ref failed (ref may have moved): %v", err))
	}

	// Sync main index so git status/diff reflect the change
	if err := git.SyncMainIndex(ctx, targetSHA); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to sync main index: %v\n", err)
	}

	// Log the undo to the oplog
	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "undo",
		Extra: map[string]interface{}{
			"ref":       ref,
			"undoneOp":  entry.Op,
			"sha":       targetSHA,
			"oldSha":    currentSHA,
		},
	})

	if !flags.quiet {
		fmt.Printf("undid %s on %s\n", entry.Op, refShortName(ref))
		fmt.Printf("  %s -> %s\n", currentSHA[:8], targetSHA[:8])
	}
}
