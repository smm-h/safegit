package main

import (
	"fmt"
	"os"

	"github.com/smm-h/safegit/internal/hooks"
	"github.com/smm-h/safegit/internal/repo"
)

func runInit(flags globalFlags, args []string) {
	// Parse init-specific flags
	uninstall := false
	for _, a := range args {
		switch a {
		case "--uninstall":
			uninstall = true
		}
	}

	gitDir := mustGitDir(flags)

	if uninstall {
		err := repo.Uninstall(gitDir)
		if err != nil {
			if flags.format == formatJSON {
				emitJSON("init", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
			} else {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
			}
			os.Exit(1)
		}
		if flags.format == formatJSON {
			emitJSON("init", map[string]string{"action": "uninstalled"}, nil, nil)
		} else if !flags.quiet {
			fmt.Println("safegit uninstalled")
		}
		return
	}

	err := repo.Init(gitDir, flags.force)
	if err != nil {
		if flags.format == formatJSON {
			emitJSON("init", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(1)
	}

	// Install placeholder pre-pre-push hook if not present
	var warnings []string
	if err := hooks.InstallPlaceholder(gitDir); err != nil {
		warnings = append(warnings, fmt.Sprintf("could not install placeholder hook: %v", err))
	}

	sgDir := repo.SafegitDir(gitDir)
	if flags.format == formatJSON {
		emitJSON("init", map[string]string{"safegit_dir": sgDir}, nil, warnings)
	} else if !flags.quiet {
		fmt.Printf("initialized safegit at %s\n", sgDir)
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
	}
}
