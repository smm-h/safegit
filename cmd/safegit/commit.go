package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/repo"
)

func runCommit(flags globalFlags, args []string) {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(4)
	}

	// Parse commit-specific flags
	var messages []string
	var messageFile string
	var branch string
	var allowEmpty bool
	var amend bool
	var files []string
	pastSeparator := false

	for i := 0; i < len(args); i++ {
		if pastSeparator {
			files = append(files, args[i])
			continue
		}
		switch args[i] {
		case "--":
			pastSeparator = true
		case "-m":
			if i+1 >= len(args) {
				die(flags, "commit", 2, "-m requires an argument")
			}
			i++
			messages = append(messages, args[i])
		case "-F":
			if i+1 >= len(args) {
				die(flags, "commit", 2, "-F requires an argument")
			}
			i++
			messageFile = args[i]
		case "--branch":
			if i+1 >= len(args) {
				die(flags, "commit", 2, "--branch requires an argument")
			}
			i++
			branch = args[i]
		case "--allow-empty":
			allowEmpty = true
		case "--amend":
			amend = true
		default:
			die(flags, "commit", 2, fmt.Sprintf("unknown flag: %s", args[i]))
		}
	}

	if amend {
		// --amend mode: amend (with files) or reword (without files)
		if allowEmpty {
			die(flags, "commit", 2, "--allow-empty cannot be used with --amend")
		}
		if messageFile != "" {
			die(flags, "commit", 2, "-F cannot be used with --amend")
		}

		runCommitAmend(flags, gitDir, messages, branch, files)
		return
	}

	// Normal commit path
	if messageFile != "" {
		data, err := os.ReadFile(messageFile)
		if err != nil {
			die(flags, "commit", 1, fmt.Sprintf("reading message file: %v", err))
		}
		messages = append(messages, strings.TrimRight(string(data), "\n"))
	}

	if len(messages) == 0 {
		die(flags, "commit", 2, "commit message required (-m or -F)")
	}
	if len(files) == 0 {
		die(flags, "commit", 2, "no files specified (use -- file1 file2 ...)")
	}

	msg := strings.Join(messages, "\n")

	fileSpecs := parseFileSpecs(files, flags, "commit")

	sgDir := repo.SafegitDir(gitDir)
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(flags, "commit", 1, fmt.Sprintf("loading config: %v", err))
	}

	p := &commit.Pipeline{SafegitDir: sgDir, Config: *cfg}
	result, err := p.Execute(context.Background(), commit.CommitRequest{
		Message:    msg,
		FileSpecs:  fileSpecs,
		Branch:     branch,
		AllowEmpty: allowEmpty,
		Force:      flags.force,
		DryRun:     flags.dryRun,
	})
	if err != nil {
		code := 1
		if ce, ok := err.(*commit.CommitError); ok {
			code = ce.Code
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(code)
	}

	if !flags.quiet {
		fmt.Printf("[%s %s] %s\n", refShortName(result.Ref), result.SHA[:8], firstLine(msg))
		fmt.Printf(" %d file(s) committed", len(files))
		if result.Attempts > 1 {
			fmt.Printf(" (%d CAS retries)", result.Attempts-1)
		}
		fmt.Println()
	}
}

// runCommitAmend handles the --amend path: amend with files, or reword without.
func runCommitAmend(flags globalFlags, gitDir string, messages []string, branch string, files []string) {
	sgDir := repo.SafegitDir(gitDir)
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(flags, "commit", 1, fmt.Sprintf("loading config: %v", err))
	}
	p := &commit.Pipeline{SafegitDir: sgDir, Config: *cfg}

	if len(files) > 0 {
		// Amend: add new files to the tip commit
		var msg string
		if len(messages) > 0 {
			msg = strings.Join(messages, "\n")
		}

		fileSpecs := parseFileSpecs(files, flags, "commit")

		result, err := p.Amend(context.Background(), commit.AmendRequest{
			Message:   msg,
			FileSpecs: fileSpecs,
			Branch:    branch,
			Force:     flags.force,
			DryRun:    flags.dryRun,
		})
		if err != nil {
			code := 1
			if ce, ok := err.(*commit.CommitError); ok {
				code = ce.Code
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(code)
		}

		if !flags.quiet {
			msgDisplay := msg
			if msgDisplay == "" {
				msgDisplay = "(message preserved)"
			}
			fmt.Printf("[%s %s] %s\n", refShortName(result.Ref), result.SHA[:8], firstLine(msgDisplay))
			fmt.Printf(" amended (was %s)", result.OldSHA[:8])
			if result.Attempts > 1 {
				fmt.Printf(" (%d CAS retries)", result.Attempts-1)
			}
			fmt.Println()
		}
	} else {
		// Reword: change the tip commit message without touching files
		if len(messages) == 0 {
			die(flags, "commit", 2, "commit message required (-m) when using --amend without files")
		}

		msg := strings.Join(messages, "\n")

		result, err := p.Reword(context.Background(), commit.RewordRequest{
			Message: msg,
			Branch:  branch,
			DryRun:  flags.dryRun,
		})
		if err != nil {
			code := 1
			if ce, ok := err.(*commit.CommitError); ok {
				code = ce.Code
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(code)
		}

		if !flags.quiet {
			fmt.Printf("[%s %s] %s\n", refShortName(result.Ref), result.SHA[:8], firstLine(msg))
			fmt.Printf(" reworded (was %s)\n", result.OldSHA[:8])
		}
	}
}
