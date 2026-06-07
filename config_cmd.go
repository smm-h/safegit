package main

import (
	"fmt"
	"os"

	"github.com/smm-h/safegit/internal/repo"
)

// formatConfigValue formats a config value for display.
// Handles *bool (prints "true", "false", or "not set") and other types via %v.
func formatConfigValue(val interface{}) string {
	if val == nil {
		return "not set"
	}
	switch v := val.(type) {
	case *bool:
		if v == nil {
			return "not set"
		}
		if *v {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", val)
	}
}

func runConfigShow(flags globalFlags) int {
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

	for _, key := range repo.ValidConfigKeys() {
		val, _ := repo.GetConfigValue(cfg, key)
		fmt.Printf("%s = %s\n", key, formatConfigValue(val))
	}
	return 0
}

func runConfigGet(flags globalFlags, key string) int {
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

	val, err := repo.GetConfigValue(cfg, key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("%s\n", formatConfigValue(val))
	return 0
}

func runConfigSet(flags globalFlags, key, value string) int {
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

	if err := repo.SetConfigValue(cfg, key, value); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

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
}
