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

func runRedo(flags globalFlags, bypassSession bool) {
	const cmd = "redo"

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
		die(flags, cmd, 1, "HEAD is detached; redo requires a branch")
	}

	// Find the last ref-updating oplog entry for this branch, scoped by session
	sessionID := os.Getenv("CLAUDE_CODE_SESSION_ID")
	var entry *oplog.Entry
	if bypassSession {
		entry, err = oplog.LastRefUpdate(sgDir, ref)
	} else if sessionID != "" {
		entry, err = oplog.LastRefUpdateForSession(sgDir, ref, sessionID)
	} else {
		die(flags, cmd, 1, "no session ID found (CLAUDE_CODE_SESSION_ID not set); pass --bypass-session to redo across all sessions")
	}
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("reading oplog: %v", err))
	}
	if entry == nil {
		die(flags, cmd, 1, fmt.Sprintf("no operations found for %s in the oplog", refShortName(ref)))
	}

	// Check that the last op is "undo"
	if entry.Op != "undo" {
		die(flags, cmd, 1, fmt.Sprintf("nothing to redo — last operation on %s was %q, not \"undo\"", refShortName(ref), entry.Op))
	}

	// Extract the pre-undo tip (what we're restoring)
	targetSHA, ok := entry.Extra["oldSha"].(string)
	if !ok || targetSHA == "" {
		die(flags, cmd, 1, "oplog entry for \"undo\" is missing \"oldSha\" field")
	}

	// The current tip SHA (what the ref points to now, i.e. after the undo)
	currentSHA := oplog.TipSHA(entry.Extra)
	if currentSHA == "" {
		die(flags, cmd, 1, "oplog entry has no resolvable tip SHA")
	}

	if flags.dryRun {
		fmt.Printf("would redo %s on %s\n", entry.Extra["undoneOp"], refShortName(ref))
		fmt.Printf("  %s -> %s\n", currentSHA[:8], targetSHA[:8])
		return
	}

	// Acquire lock on the ref
	timeout := time.Duration(cfg.Lock.AcquireTimeoutSeconds) * time.Second
	sharedDir := repo.SharedSafegitDir(ctx, gitDir)
	lk, err := lock.Acquire(sharedDir, sgDir, ref, "redo", timeout)
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

	if err := maybeAutoBumpParent(ctx, flags, gitDir, targetSHA, "redo", ""); err != nil {
		fmt.Fprintf(os.Stderr, "error: auto-bump parent: %v\n", err)
		os.Exit(1)
	}

	// Log the redo to the oplog
	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "redo",
		Extra: map[string]interface{}{
			"ref":      ref,
			"redoneOp": entry.Extra["undoneOp"],
			"sha":      targetSHA,
			"oldSha":   currentSHA,
		},
	})

	if !flags.quiet {
		fmt.Printf("redid %s on %s\n", entry.Extra["undoneOp"], refShortName(ref))
		fmt.Printf("  %s -> %s\n", currentSHA[:8], targetSHA[:8])
	}
}
