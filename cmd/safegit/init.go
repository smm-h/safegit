package main

import (
	"fmt"
	"os"

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
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if !flags.quiet {
			fmt.Println("safegit uninstalled")
		}
		return
	}

	err := repo.Init(gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	sgDir := repo.SafegitDir(gitDir)
	if !flags.quiet {
		fmt.Printf("initialized safegit at %s\n", sgDir)
	}
}
