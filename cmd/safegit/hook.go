package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/hooks"
	"github.com/smm-h/safegit/internal/repo"
)

func runHook(flags globalFlags, args []string) int {
	if len(args) == 0 {
		hookUsage(flags)
		return 2
	}

	sub := args[0]
	subArgs := args[1:]

	switch sub {
	case "list":
		return hookList(flags)
	case "run":
		return hookRun(flags, subArgs)
	case "install":
		return hookInstall(flags, subArgs)
	default:
		hookUsage(flags)
		return 2
	}
}

// hookList discovers and lists all pre-pre-push hooks.
func hookList(flags globalFlags) int {
	gitDir := mustGitDir(flags)

	discovered, err := hooks.Discover(gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if len(discovered) == 0 {
		fmt.Println("no pre-pre-push hooks found")
		return 0
	}

	for _, h := range discovered {
		fmt.Printf("  %s  (%s)\n", filepath.Base(h), h)
	}
	fmt.Printf("%d hook(s)\n", len(discovered))
	return 0
}

// hookRun runs a specific hook by name (or all if no name given).
func hookRun(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading config: %v\n", err)
		return 1
	}

	// Synthesize stdin from current branch state
	ctx := context.Background()
	hookStdin, err := synthesizeHookStdin(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	timeoutSec := cfg.Hooks.PrePrePush.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 1800
	}

	hookEnv := []string{
		"SAFEGIT_REMOTE_NAME=origin",
		"SAFEGIT_REMOTE_URL=manual-run",
		"SAFEGIT_PHASE=pre-pre-push",
		fmt.Sprintf("SAFEGIT_HOOK_TIMEOUT_S=%d", timeoutSec),
	}

	if len(args) > 0 {
		// Run a specific hook by name
		name := args[0]
		discovered, dErr := hooks.Discover(gitDir)
		if dErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", dErr)
			return 1
		}

		var hookPath string
		for _, h := range discovered {
			if filepath.Base(h) == name {
				hookPath = h
				break
			}
		}
		if hookPath == "" {
			fmt.Fprintf(os.Stderr, "hook %q not found\n", name)
			return 1
		}

		fmt.Printf("running hook: %s\n", name)
		r := hooks.RunSingle(ctx, hookPath, hookStdin, timeoutSec, hookEnv)
		if r.TimedOut {
			fmt.Fprintf(os.Stderr, "hook %s timed out\n", name)
			return 21
		}
		if r.ExitCode != 0 {
			fmt.Fprintf(os.Stderr, "hook %s failed (exit %d)\n", name, r.ExitCode)
			return 20
		}
		fmt.Printf("hook %s passed (%v)\n", name, r.Duration)
		return 0
	}

	// No name -- run all hooks
	results, rErr := hooks.Run(ctx, gitDir, hookStdin, timeoutSec, hookEnv)
	if rErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", rErr)
		return 1
	}

	if len(results) == 0 {
		fmt.Println("no hooks to run")
		return 0
	}

	failed := false
	for _, r := range results {
		status := "passed"
		if r.TimedOut {
			status = "timed out"
			failed = true
		} else if r.ExitCode != 0 {
			status = fmt.Sprintf("failed (exit %d)", r.ExitCode)
			failed = true
		}
		fmt.Printf("  %s: %s (%v)\n", r.Name, status, r.Duration)
	}

	if failed {
		return 20
	}
	return 0
}

// hookInstall copies a hook file to .git/hooks/ and makes it executable.
func hookInstall(flags globalFlags, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: safegit hook install <path>")
		return 2
	}

	gitDir := mustGitDir(flags)
	srcPath := args[0]

	if err := hooks.Install(gitDir, srcPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	name := filepath.Base(srcPath)
	if !flags.quiet {
		fmt.Printf("installed hook: %s\n", name)
	}
	return 0
}

// synthesizeHookStdin builds hook stdin from the current branch's state.
func synthesizeHookStdin(ctx context.Context) ([]byte, error) {
	headRef, err := git.HeadRef(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot determine current branch (detached HEAD?)")
	}

	localSHA, err := git.RevParse(ctx, headRef)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", headRef, err)
	}

	nullSHA := "0000000000000000000000000000000000000000"
	line := fmt.Sprintf("%s %s %s %s\n", headRef, localSHA, headRef, nullSHA)
	return []byte(line), nil
}

func hookUsage(flags globalFlags) {
	msg := `usage: safegit hook <subcommand>

Subcommands:
  list             List discovered pre-pre-push hooks
  run [<name>]     Run a specific hook (or all hooks)
  install <path>   Copy a hook file to .git/hooks/, chmod +x
`
	fmt.Print(msg)
}
