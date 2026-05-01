package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/index"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/wip"
)

type checkResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "ok", "warn", "error"
	Detail string `json:"detail,omitempty"`
}

func runDoctor(flags globalFlags) {
	ctx := context.Background()
	gitDir := mustGitDir(flags)

	var checks []checkResult

	// Check 1: Is safegit initialized?
	if repo.IsInitialized(gitDir) {
		checks = append(checks, checkResult{Name: "initialized", Status: "ok"})
	} else {
		checks = append(checks, checkResult{Name: "initialized", Status: "error", Detail: "run 'safegit init'"})
	}

	sgDir := repo.SafegitDir(gitDir)

	// Check 2: Orphan tmp dirs (report only, don't delete -- use gc for cleanup)
	if repo.IsInitialized(gitDir) {
		orphans, err := index.GarbageCollectDryRun(sgDir)
		if err != nil {
			checks = append(checks, checkResult{Name: "tmp_dirs", Status: "warn", Detail: err.Error()})
		} else if len(orphans) > 0 {
			checks = append(checks, checkResult{
				Name:   "tmp_dirs",
				Status: "warn",
				Detail: fmt.Sprintf("%d orphan tmp dir(s) found (run 'safegit gc' to clean)", len(orphans)),
			})
		} else {
			checks = append(checks, checkResult{Name: "tmp_dirs", Status: "ok"})
		}
	}

	// Check 3: Stale locks (scan the shared safegit dir so worktree locks are found)
	if repo.IsInitialized(gitDir) {
		sharedDir := repo.SharedSafegitDir(ctx, gitDir)
		locksDir := filepath.Join(sharedDir, "locks", "refs", "heads")
		staleCount := 0
		entries, err := os.ReadDir(locksDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".lock") {
					lp := filepath.Join(locksDir, e.Name())
					stale, sErr := lock.IsStale(lp)
					if sErr == nil && stale {
						staleCount++
					}
				}
			}
		}
		if staleCount > 0 {
			checks = append(checks, checkResult{
				Name:   "stale_locks",
				Status: "warn",
				Detail: fmt.Sprintf("%d stale lock(s) found", staleCount),
			})
		} else {
			checks = append(checks, checkResult{Name: "stale_locks", Status: "ok"})
		}
	}

	// Check 4: Orphan wip-locks
	if repo.IsInitialized(gitDir) {
		orphans, err := wip.OrphanLocks(ctx, sgDir)
		if err != nil {
			checks = append(checks, checkResult{Name: "wip_locks", Status: "warn", Detail: err.Error()})
		} else if len(orphans) > 0 {
			checks = append(checks, checkResult{
				Name:   "wip_locks",
				Status: "warn",
				Detail: fmt.Sprintf("%d orphan wip-lock(s) found (run 'safegit gc' to clean)", len(orphans)),
			})
		} else {
			checks = append(checks, checkResult{Name: "wip_locks", Status: "ok"})
		}
	}

	// Check 5: Config readable
	if repo.IsInitialized(gitDir) {
		cfg, err := repo.LoadConfig(gitDir)
		if err != nil {
			checks = append(checks, checkResult{Name: "config", Status: "error", Detail: err.Error()})
		} else if cfg.SchemaVersion != 1 {
			checks = append(checks, checkResult{
				Name:   "config",
				Status: "warn",
				Detail: fmt.Sprintf("unknown schema version %d", cfg.SchemaVersion),
			})
		} else {
			checks = append(checks, checkResult{Name: "config", Status: "ok"})
		}
	}

	// Check 6: Raw git bypass detection -- compare oplog's last ref-update against actual tip
	if repo.IsInitialized(gitDir) {
		ref, refErr := git.HeadRef(ctx)
		if refErr == nil && ref != "" {
			lastEntry, entryErr := oplog.LastRefUpdate(sgDir, ref)
			if entryErr == nil && lastEntry != nil {
				if sha := oplog.TipSHA(lastEntry.Extra); sha != "" {
					tipSHA, tipErr := git.RevParse(ctx, ref)
					if tipErr == nil && tipSHA != sha {
						checks = append(checks, checkResult{
							Name:   "bypass_detect",
							Status: "warn",
							Detail: fmt.Sprintf("tip of %s (%s) diverged from last oplog entry (%s); raw git may have been used", refShortName(ref), tipSHA[:8], sha[:8]),
						})
					} else if tipErr == nil {
						checks = append(checks, checkResult{Name: "bypass_detect", Status: "ok"})
					}
				}
			} else if entryErr == nil {
				// No oplog entries for this ref -- skip check
				checks = append(checks, checkResult{Name: "bypass_detect", Status: "ok", Detail: "no oplog entries for current ref"})
			}
		}
	}

	// Check 7: NFS/network filesystem detection (platform-specific)
	checks = append(checks, checkFilesystem(gitDir))

	// Check 8: Non-executable hooks in pre-pre-push.d/
	if repo.IsInitialized(gitDir) {
		hookDir := filepath.Join(gitDir, "hooks", "pre-pre-push.d")
		entries, readErr := os.ReadDir(hookDir)
		if readErr == nil {
			var nonExec []string
			for _, e := range entries {
				if e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasSuffix(e.Name(), "~") {
					continue
				}
				info, sErr := e.Info()
				if sErr != nil {
					continue
				}
				if info.Mode()&0111 == 0 {
					nonExec = append(nonExec, e.Name())
				}
			}
			if len(nonExec) > 0 {
				checks = append(checks, checkResult{
					Name:   "hook_perms",
					Status: "warn",
					Detail: fmt.Sprintf("%d non-executable hook(s) in pre-pre-push.d/: %s", len(nonExec), strings.Join(nonExec, ", ")),
				})
			} else {
				checks = append(checks, checkResult{Name: "hook_perms", Status: "ok"})
			}
		}
	}

	// Check 9: Unsupported features (.gitmodules, LFS)
	{
		repoRoot, rootErr := git.RepoRoot(ctx)
		if rootErr == nil {
			var unsupported []string
			if _, err := os.Stat(filepath.Join(repoRoot, ".gitmodules")); err == nil {
				unsupported = append(unsupported, ".gitmodules detected (submodules not supported)")
			}
			attrsPath := filepath.Join(repoRoot, ".gitattributes")
			if data, err := os.ReadFile(attrsPath); err == nil {
				if strings.Contains(string(data), "filter=lfs") {
					unsupported = append(unsupported, ".gitattributes has filter=lfs (LFS not supported)")
				}
			}
			if len(unsupported) > 0 {
				checks = append(checks, checkResult{
					Name:   "unsupported",
					Status: "warn",
					Detail: strings.Join(unsupported, "; "),
				})
			} else {
				checks = append(checks, checkResult{Name: "unsupported", Status: "ok"})
			}
		}
	}

	if flags.format == formatJSON {
		emitJSON("doctor", checks, nil, nil)
		return
	}

	allOK := true
	for _, c := range checks {
		icon := "OK"
		switch c.Status {
		case "warn":
			icon = "WARN"
			allOK = false
		case "error":
			icon = "FAIL"
			allOK = false
		}
		if c.Detail != "" {
			fmt.Printf("[%s] %s: %s\n", icon, c.Name, c.Detail)
		} else {
			fmt.Printf("[%s] %s\n", icon, c.Name)
		}
	}
	if allOK && !flags.quiet {
		fmt.Println("all checks passed")
	}
}
