package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/smm-h/safegit/internal/index"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/wip"
)

func runGC(flags globalFlags) {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		if flags.format == formatJSON {
			emitJSON("gc", nil, &jsonError{Code: 4, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(4)
	}

	sgDir := repo.SafegitDir(gitDir)
	cfg, _ := loadConfig(flags, gitDir)

	if flags.dryRun {
		// Dry run: report what would be cleaned
		orphanDirs, err := index.GarbageCollectDryRun(sgDir)
		if err != nil {
			if flags.format == formatJSON {
				emitJSON("gc", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
			} else {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
			}
			os.Exit(1)
		}

		orphanWipLocks, _ := wip.OrphanLocks(sgDir)

		// Check for legacy queue directory
		queueDir := filepath.Join(sgDir, "queue")
		hasLegacyQueue := false
		if info, err := os.Stat(queueDir); err == nil && info.IsDir() {
			hasLegacyQueue = true
		}

		logSizeMB := float64(0)
		logSize, _ := oplog.LogSize(sgDir)
		if logSize > 0 {
			logSizeMB = float64(logSize) / (1024 * 1024)
		}
		maxMB := 100
		if cfg != nil && cfg.Log.MaxSizeMB > 0 {
			maxMB = cfg.Log.MaxSizeMB
		}
		wouldRotate := logSize >= int64(maxMB)*1024*1024

		if flags.format == formatJSON {
			emitJSON("gc", map[string]interface{}{
				"dryRun":           true,
				"orphanDirs":       len(orphanDirs),
				"orphanWipLocks":   len(orphanWipLocks),
				"hasLegacyQueue":   hasLegacyQueue,
				"logSizeMB":        logSizeMB,
				"wouldRotateLog":   wouldRotate,
			}, nil, nil)
		} else if !flags.quiet {
			fmt.Printf("would remove %d orphan tmp dir(s)\n", len(orphanDirs))
			if len(orphanWipLocks) > 0 {
				fmt.Printf("would remove %d orphan wip-lock(s)\n", len(orphanWipLocks))
			}
			if hasLegacyQueue {
				fmt.Println("would remove legacy queue directory")
			}
			fmt.Printf("log size: %.1f MB (max: %d MB)", logSizeMB, maxMB)
			if wouldRotate {
				fmt.Print(" -- would rotate")
			}
			fmt.Println()
		}
		return
	}

	// Actual GC
	removed, err := index.GarbageCollect(sgDir)
	if err != nil {
		if flags.format == formatJSON {
			emitJSON("gc", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(1)
	}

	// Clean orphan wip-locks
	wipCleaned, wipErr := wip.CleanOrphanLocks(sgDir)
	if wipErr != nil {
		if flags.format == formatJSON {
			emitJSON("gc", nil, &jsonError{Code: 1, Message: wipErr.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error cleaning wip locks: %v\n", wipErr)
		}
		os.Exit(1)
	}

	// Clean up legacy queue directory (removed in v0.2)
	queueDir := filepath.Join(sgDir, "queue")
	queueRemoved := false
	if info, err := os.Stat(queueDir); err == nil && info.IsDir() {
		os.RemoveAll(queueDir)
		queueRemoved = true
	}

	// Log rotation
	maxMB := 100
	if cfg != nil && cfg.Log.MaxSizeMB > 0 {
		maxMB = cfg.Log.MaxSizeMB
	}
	rotated, rotErr := oplog.Rotate(sgDir, maxMB)
	if rotErr != nil && !flags.quiet {
		fmt.Fprintf(os.Stderr, "warning: log rotation failed: %v\n", rotErr)
	}

	if flags.format == formatJSON {
		data := map[string]interface{}{
			"removed":         removed,
			"wipLocksRemoved": wipCleaned,
			"queueRemoved":    queueRemoved,
			"logRotated":      rotated,
		}
		emitJSON("gc", data, nil, nil)
	} else if !flags.quiet {
		fmt.Printf("removed %d orphan tmp dir(s)\n", removed)
		if wipCleaned > 0 {
			fmt.Printf("removed %d orphan wip-lock(s)\n", wipCleaned)
		}
		if queueRemoved {
			fmt.Println("removed legacy queue directory")
		}
		if rotated {
			fmt.Println("log rotated (old log saved as log.1)")
		}
	}
}
