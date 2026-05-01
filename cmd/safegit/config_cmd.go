package main

import (
	"fmt"
	"os"

	"github.com/smm-h/safegit/internal/repo"
)

func runConfig(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		if flags.format == formatJSON {
			emitJSON("config", nil, &jsonError{Code: 4, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		return 4
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		if flags.format == formatJSON {
			emitJSON("config", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		return 1
	}

	switch len(args) {
	case 0:
		// Show all config
		if flags.format == formatJSON {
			emitJSON("config", cfg, nil, nil)
		} else {
			for _, key := range repo.ValidConfigKeys() {
				val, _ := repo.GetConfigValue(cfg, key)
				fmt.Printf("%s = %v\n", key, val)
			}
		}
		return 0

	case 1:
		// Get a specific key
		key := args[0]
		val, err := repo.GetConfigValue(cfg, key)
		if err != nil {
			if flags.format == formatJSON {
				emitJSON("config", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
			} else {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
			}
			return 1
		}
		if flags.format == formatJSON {
			emitJSON("config", map[string]interface{}{key: val}, nil, nil)
		} else {
			fmt.Printf("%v\n", val)
		}
		return 0

	case 2:
		// Set a key
		key, value := args[0], args[1]
		if err := repo.SetConfigValue(cfg, key, value); err != nil {
			if flags.format == formatJSON {
				emitJSON("config", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
			} else {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
			}
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
			if flags.format == formatJSON {
				emitJSON("config", nil, &jsonError{Code: 1, Message: saveErr.Error()}, nil)
			} else {
				fmt.Fprintf(os.Stderr, "error: %v\n", saveErr)
			}
			return 1
		}
		if flags.format == formatJSON {
			emitJSON("config", map[string]interface{}{key: value}, nil, nil)
		} else if !flags.quiet {
			fmt.Printf("%s = %s\n", key, value)
		}
		return 0

	default:
		msg := "usage: safegit config [<key> [<value>]]"
		if flags.format == formatJSON {
			emitJSON("config", nil, &jsonError{Code: 2, Message: msg}, nil)
		} else {
			fmt.Fprintln(os.Stderr, msg)
		}
		return 2
	}
}
