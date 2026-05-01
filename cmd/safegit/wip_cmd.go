package main

import (
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
		wips, err := wip.List(sgDir)
		if err != nil {
			die(flags, "wip",1, err.Error())
		}
		if flags.format == formatJSON {
			emitJSON("wip list", map[string]interface{}{"wips": wips}, nil, nil)
		} else if !flags.quiet {
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

	result, err := wip.Create(sgDir, args)
	if err != nil {
		die(flags, "wip",1, err.Error())
	}

	if flags.format == formatJSON {
		data := map[string]interface{}{
			"id":    result.ID,
			"files": result.Files,
			"ref":   result.Ref,
		}
		emitJSON("wip", data, nil, nil)
	} else if !flags.quiet {
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

	wipID := args[0]
	restored, err := wip.Restore(sgDir, wipID)
	if err != nil {
		die(flags, "wip",1, err.Error())
	}

	if flags.format == formatJSON {
		data := map[string]interface{}{
			"id":       wipID,
			"restored": restored,
		}
		emitJSON("unwip", data, nil, nil)
	} else if !flags.quiet {
		fmt.Printf("wip %s restored (%d file(s))\n", wipID, len(restored))
	}
}
