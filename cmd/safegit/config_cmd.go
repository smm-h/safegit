package main

import (
	"fmt"
	"os"

	"github.com/smm-h/safegit/internal/repo"
)

func runConfig(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	switch len(args) {
	case 0:
		// Show all config
		for _, key := range repo.ValidConfigKeys() {
			val, _ := repo.GetConfigValue(cfg, key)
			fmt.Printf("%s = %v\n", key, val)
		}
		return 0

	case 1:
		// Get a specific key
		key := args[0]
		val, err := repo.GetConfigValue(cfg, key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("%v\n", val)
		return 0

	case 2:
		// Set a key
		key, value := args[0], args[1]
		if err := repo.SetConfigValue(cfg, key, value); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		// Write to override path if --config was set, otherwise default location
		var saveErr error
		if flags.configPath != "" {
			saveErr = repo.SaveConfigTo(flags.configPath, cfg)
		} else {
			saveErr = repo.SaveConfig(gitDir, cfg)
		}
		if saveErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", saveErr)
			return 1
		}
		if !flags.quiet {
			fmt.Printf("%s = %s\n", key, value)
		}
		return 0

	default:
		fmt.Fprintln(os.Stderr, "usage: safegit config [<key> [<value>]]")
		return 2
	}
}
