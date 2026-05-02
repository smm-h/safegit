package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/wip"
)

func runWip(flags globalFlags, args []string) {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		die(flags, "wip",4, err.Error())
	}
	sgDir := repo.SafegitDir(gitDir)

	// "wip list" subcommand
	if len(args) > 0 && args[0] == "list" {
		ctx := context.Background()
		wips, err := wip.List(ctx, sgDir)
		if err != nil {
			die(flags, "wip",1, err.Error())
		}
		if !flags.quiet {
			if len(wips) == 0 {
				fmt.Println("no active wips")
			} else {
				for _, w := range wips {
					fmt.Printf("%s  %s  [%s]\n", w.ID, strings.Join(w.Files, ", "), w.CreatedAt.Format(time.RFC3339))
				}
			}
		}
		return
	}

	// "wip <file1> [<file2> ...]" -- create a wip
	if len(args) == 0 {
		die(flags, "wip",2, "usage: safegit wip <file1> [<file2> ...] | safegit wip list")
	}

	ctx := context.Background()
	result, err := wip.Create(ctx, sgDir, args)
	if err != nil {
		die(flags, "wip",1, err.Error())
	}

	if !flags.quiet {
		fmt.Printf("wip %s created (%d file(s) saved)\n", result.ID, len(result.Files))
	}
}

func runUnwip(flags globalFlags, args []string) {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		die(flags, "wip",4, err.Error())
	}
	sgDir := repo.SafegitDir(gitDir)

	if len(args) == 0 {
		die(flags, "wip",2, "usage: safegit unwip <wip-id>")
	}

	ctx := context.Background()
	wipID := args[0]
	restored, err := wip.Restore(ctx, sgDir, wipID)
	if err != nil {
		die(flags, "wip",1, err.Error())
	}

	if !flags.quiet {
		fmt.Printf("wip %s restored (%d file(s))\n", wipID, len(restored))
	}
}
