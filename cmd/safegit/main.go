package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/coord"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/hooks"
	"github.com/smm-h/safegit/internal/index"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/stage"
	"github.com/smm-h/safegit/internal/wip"
)

// Set via -ldflags "-X main.version=..." at build time.
var version = "dev"

// output format for the entire CLI
type outputFormat int

const (
	formatHuman outputFormat = iota
	formatJSON
)

// globalFlags holds flags parsed before command dispatch.
type globalFlags struct {
	format     outputFormat
	quiet      bool
	verbose    bool
	noColor    bool
	dryRun     bool
	force      bool
	configPath string
}

// jsonResponse is the envelope for all JSON output.
type jsonResponse struct {
	OK       bool        `json:"ok"`
	Command  string      `json:"command"`
	Data     interface{} `json:"data,omitempty"`
	Error    *jsonError  `json:"error"`
	Warnings []string    `json:"warnings"`
}

type jsonError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	flags, args := parseGlobalFlags(os.Args[1:])

	if len(args) == 0 {
		printUsage(flags)
		os.Exit(0)
	}

	cmd := args[0]
	cmdArgs := args[1:]
	switch cmd {
	case "init":
		runInit(flags, cmdArgs)
	case "commit":
		runCommit(flags, cmdArgs)
	case "amend":
		runAmend(flags, cmdArgs)
	case "reword":
		runReword(flags, cmdArgs)
	case "wip":
		runWip(flags, cmdArgs)
	case "unwip":
		runUnwip(flags, cmdArgs)
	case "doctor":
		runDoctor(flags)
	case "gc":
		runGC(flags)
	case "version":
		runVersion(flags)
	case "checkout":
		os.Exit(runCheckout(flags, cmdArgs))
	case "pull":
		os.Exit(runPull(flags, cmdArgs))
	case "merge":
		os.Exit(runMerge(flags, cmdArgs))
	case "rebase":
		os.Exit(runRebase(flags, cmdArgs))
	case "reset":
		os.Exit(runReset(flags, cmdArgs))
	case "bisect":
		os.Exit(runBisect(flags, cmdArgs))
	case "status":
		os.Exit(runStatus(flags, cmdArgs))
	case "diff":
		os.Exit(runDiff(flags, cmdArgs))
	case "log":
		os.Exit(runLog(flags, cmdArgs))
	case "show":
		os.Exit(runShow(flags, cmdArgs))
	case "push":
		os.Exit(runPush(flags, cmdArgs))
	case "fetch":
		os.Exit(runFetch(cmdArgs))
	case "hook":
		os.Exit(runHook(flags, cmdArgs))
	case "config":
		os.Exit(runConfig(flags, cmdArgs))
	case "unlock":
		os.Exit(runUnlock(flags, cmdArgs))
	case "branch":
		os.Exit(runBranch(flags, cmdArgs))
	case "help", "--help", "-h":
		printUsage(flags)
	default:
		unknownCommand(flags, cmd)
		os.Exit(2)
	}
}

func parseGlobalFlags(args []string) (globalFlags, []string) {
	var f globalFlags
	var rest []string

	for i := 0; i < len(args); i++ {
		// "--" is the file separator for commit; pass it and everything after through.
		if args[i] == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		switch args[i] {
		case "--format":
			if i+1 < len(args) {
				i++
				switch args[i] {
				case "json":
					f.format = formatJSON
				case "human":
					f.format = formatHuman
				default:
					fmt.Fprintf(os.Stderr, "unknown format: %s (expected human|json)\n", args[i])
					os.Exit(2)
				}
			} else {
				fmt.Fprintln(os.Stderr, "--format requires an argument (human|json)")
				os.Exit(2)
			}
		case "--quiet", "-q":
			f.quiet = true
		case "--verbose", "-v":
			f.verbose = true
		case "--no-color":
			f.noColor = true
		case "--dry-run", "-n":
			f.dryRun = true
		case "--force", "-f":
			f.force = true
		case "--config":
			if i+1 < len(args) {
				i++
				f.configPath = args[i]
			} else {
				fmt.Fprintln(os.Stderr, "--config requires an argument")
				os.Exit(2)
			}
		default:
			// Not a global flag -- pass through (subcommand name or subcommand flag).
			rest = append(rest, args[i])
		}
	}
	return f, rest
}

func runVersion(flags globalFlags) {
	gitVer := gitVersion()

	if flags.format == formatJSON {
		data := map[string]string{
			"safegit_version": version,
			"go_version":      runtime.Version(),
			"os":              runtime.GOOS,
			"arch":            runtime.GOARCH,
			"git_version":     gitVer,
		}
		emitJSON("version", data, nil, nil)
		return
	}

	fmt.Printf("safegit %s\n", version)
	fmt.Printf("go      %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Printf("git     %s\n", gitVer)
}

func gitVersion() string {
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func printUsage(flags globalFlags) {
	if flags.format == formatJSON {
		emitJSON("help", map[string]string{"usage": usageText()}, nil, nil)
		return
	}
	fmt.Print(usageText())
}

func unknownCommand(flags globalFlags, cmd string) {
	msg := fmt.Sprintf("unknown command: %s", cmd)
	if flags.format == formatJSON {
		emitJSON(cmd, nil, &jsonError{Code: 2, Message: msg}, nil)
		return
	}
	fmt.Fprintln(os.Stderr, msg)
	fmt.Fprint(os.Stderr, usageText())
}

func usageText() string {
	return `Usage: safegit <command> [options]

Commands:
  init        Initialize .git/safegit/ directory structure
  commit      Stage and commit files atomically (per-invocation index)
  amend       Amend the tip commit with new files (CAS-safe)
  reword      Rewrite the tip commit message (CAS-safe)
  wip         Save/restore work-in-progress snapshots
  checkout    Checkout a ref (guarded: checks for uncommitted work)
  pull        Fetch + merge (guarded, default --ff-only)
  merge       Merge a branch (guarded)
  rebase      Rebase onto upstream (guarded)
  reset       Reset (guarded for --hard)
  bisect      Bisect (guarded: checks for uncommitted work)
  push        Push with pre-pre-push hooks and retry logic
  fetch       Fetch from remote (git passthrough)
  hook        Manage pre-pre-push hooks (list, run, install)
  branch      List, create, or delete branches
  status      Show working tree status (git passthrough)
  diff        Show diffs (git passthrough)
  log         Show commit log (git passthrough)
  show        Show objects (git passthrough)
  config      Show or set configuration values
  unlock      Release a stale ref lock (--force to override live holder)
  doctor      Health checks (initialized? orphan dirs? stale locks?)
  gc          Garbage-collect dead tmp index directories and rotate logs
  version     Print version and build info
  help        Print this help

Global flags:
  --config <path>       Override config.json path
  --format human|json   Output format (default: human)
  --quiet, -q           Suppress non-essential output
  --verbose, -v         Verbose output
  --no-color            Disable colored output
  --dry-run, -n         Show what would be done without doing it
  --force, -f           Skip safety checks (bypass coordination guard)
`
}

// loadConfig loads the safegit config, using the override path if --config was set.
func loadConfig(flags globalFlags, gitDir string) (*repo.Config, error) {
	if flags.configPath != "" {
		return repo.LoadConfigFrom(flags.configPath)
	}
	return repo.LoadConfig(gitDir)
}

// mustGitDir resolves the .git directory or exits with an error.
func mustGitDir(flags globalFlags) string {
	gitDir, err := git.GitDir()
	if err != nil {
		msg := "not a git repository (or git is not installed)"
		if flags.format == formatJSON {
			emitJSON("", nil, &jsonError{Code: 3, Message: msg}, nil)
		} else {
			fmt.Fprintln(os.Stderr, msg)
		}
		os.Exit(3)
	}
	// Resolve to absolute path
	abs, err := filepath.Abs(gitDir)
	if err != nil {
		abs = gitDir
	}
	return abs
}

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

func runCommit(flags globalFlags, args []string) {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		if flags.format == formatJSON {
			emitJSON("commit", nil, &jsonError{Code: 4, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(4)
	}

	// Parse commit-specific flags
	var messages []string
	var messageFile string
	var branch string
	var allowEmpty bool
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
				commitDie(flags, 2, "-m requires an argument")
			}
			i++
			messages = append(messages, args[i])
		case "-F":
			if i+1 >= len(args) {
				commitDie(flags, 2, "-F requires an argument")
			}
			i++
			messageFile = args[i]
		case "--branch":
			if i+1 >= len(args) {
				commitDie(flags, 2, "--branch requires an argument")
			}
			i++
			branch = args[i]
		case "--allow-empty":
			allowEmpty = true
		default:
			commitDie(flags, 2, fmt.Sprintf("unknown flag: %s", args[i]))
		}
	}

	if messageFile != "" {
		data, err := os.ReadFile(messageFile)
		if err != nil {
			commitDie(flags, 1, fmt.Sprintf("reading message file: %v", err))
		}
		messages = append(messages, strings.TrimRight(string(data), "\n"))
	}

	if len(messages) == 0 {
		commitDie(flags, 2, "commit message required (-m or -F)")
	}
	if len(files) == 0 {
		commitDie(flags, 2, "no files specified (use -- file1 file2 ...)")
	}

	msg := strings.Join(messages, "\n")

	// Parse file:hunks syntax (e.g. "file.txt:1,3" or "file.txt:2-4")
	fileSpecs := make([]commit.FileSpec, 0, len(files))
	for _, f := range files {
		spec := commit.FileSpec{}
		if colonIdx := strings.LastIndex(f, ":"); colonIdx > 0 {
			// Check if what's after the colon looks like a hunk spec (digits, commas, dashes)
			suffix := f[colonIdx+1:]
			if isHunkSpec(suffix) {
				hunks, err := stage.ParseHunkSpec(suffix)
				if err != nil {
					commitDie(flags, 2, fmt.Sprintf("invalid hunk spec in %q: %v", f, err))
				}
				spec.Path = f[:colonIdx]
				spec.Hunks = hunks
			} else {
				// Colon is part of the filename (e.g. Windows path or something)
				spec.Path = f
			}
		} else {
			spec.Path = f
		}
		fileSpecs = append(fileSpecs, spec)
	}

	sgDir := repo.SafegitDir(gitDir)
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		commitDie(flags, 1, fmt.Sprintf("loading config: %v", err))
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
		if flags.format == formatJSON {
			emitJSON("commit", nil, &jsonError{Code: code, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(code)
	}

	if flags.format == formatJSON {
		emitJSON("commit", result, nil, nil)
	} else if !flags.quiet {
		fmt.Printf("[%s %s] %s\n", refShortName(result.Ref), result.SHA[:8], firstLine(msg))
		fmt.Printf(" %d file(s) committed", len(files))
		if result.Attempts > 1 {
			fmt.Printf(" (%d CAS retries)", result.Attempts-1)
		}
		fmt.Println()
	}
}

func runAmend(flags globalFlags, args []string) {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		if flags.format == formatJSON {
			emitJSON("amend", nil, &jsonError{Code: 4, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(4)
	}

	// Parse amend-specific flags
	var messages []string
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
				amendDie(flags, 2, "-m requires an argument")
			}
			i++
			messages = append(messages, args[i])
		default:
			amendDie(flags, 2, fmt.Sprintf("unknown flag: %s", args[i]))
		}
	}

	if len(files) == 0 {
		amendDie(flags, 2, "no files specified (use -- file1 file2 ...)")
	}

	var msg string
	if len(messages) > 0 {
		msg = strings.Join(messages, "\n")
	}

	// Parse file:hunks syntax
	fileSpecs := make([]commit.FileSpec, 0, len(files))
	for _, f := range files {
		spec := commit.FileSpec{}
		if colonIdx := strings.LastIndex(f, ":"); colonIdx > 0 {
			suffix := f[colonIdx+1:]
			if isHunkSpec(suffix) {
				hunks, err := stage.ParseHunkSpec(suffix)
				if err != nil {
					amendDie(flags, 2, fmt.Sprintf("invalid hunk spec in %q: %v", f, err))
				}
				spec.Path = f[:colonIdx]
				spec.Hunks = hunks
			} else {
				spec.Path = f
			}
		} else {
			spec.Path = f
		}
		fileSpecs = append(fileSpecs, spec)
	}

	sgDir := repo.SafegitDir(gitDir)
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		amendDie(flags, 1, fmt.Sprintf("loading config: %v", err))
	}

	p := &commit.Pipeline{SafegitDir: sgDir, Config: *cfg}
	result, err := p.Amend(context.Background(), commit.AmendRequest{
		Message:   msg,
		FileSpecs: fileSpecs,
		Force:     flags.force,
		DryRun:    flags.dryRun,
	})
	if err != nil {
		code := 1
		if ce, ok := err.(*commit.CommitError); ok {
			code = ce.Code
		}
		if flags.format == formatJSON {
			emitJSON("amend", nil, &jsonError{Code: code, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(code)
	}

	if flags.format == formatJSON {
		emitJSON("amend", result, nil, nil)
	} else if !flags.quiet {
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
}

func runReword(flags globalFlags, args []string) {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		if flags.format == formatJSON {
			emitJSON("reword", nil, &jsonError{Code: 4, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(4)
	}

	// Parse reword flags
	var messages []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-m":
			if i+1 >= len(args) {
				rewordDie(flags, 2, "-m requires an argument")
			}
			i++
			messages = append(messages, args[i])
		default:
			rewordDie(flags, 2, fmt.Sprintf("unknown flag: %s", args[i]))
		}
	}

	if len(messages) == 0 {
		rewordDie(flags, 2, "commit message required (-m)")
	}

	msg := strings.Join(messages, "\n")

	sgDir := repo.SafegitDir(gitDir)
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		rewordDie(flags, 1, fmt.Sprintf("loading config: %v", err))
	}

	p := &commit.Pipeline{SafegitDir: sgDir, Config: *cfg}
	result, err := p.Reword(context.Background(), commit.RewordRequest{
		Message: msg,
		DryRun:  flags.dryRun,
	})
	if err != nil {
		code := 1
		if ce, ok := err.(*commit.CommitError); ok {
			code = ce.Code
		}
		if flags.format == formatJSON {
			emitJSON("reword", nil, &jsonError{Code: code, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(code)
	}

	if flags.format == formatJSON {
		emitJSON("reword", result, nil, nil)
	} else if !flags.quiet {
		fmt.Printf("[%s %s] %s\n", refShortName(result.Ref), result.SHA[:8], firstLine(msg))
		fmt.Printf(" reworded (was %s)\n", result.OldSHA[:8])
	}
}

// amendDie prints an error and exits for the amend subcommand.
func amendDie(flags globalFlags, code int, msg string) {
	if flags.format == formatJSON {
		emitJSON("amend", nil, &jsonError{Code: code, Message: msg}, nil)
	} else {
		fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	}
	os.Exit(code)
}

// rewordDie prints an error and exits for the reword subcommand.
func rewordDie(flags globalFlags, code int, msg string) {
	if flags.format == formatJSON {
		emitJSON("reword", nil, &jsonError{Code: code, Message: msg}, nil)
	} else {
		fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	}
	os.Exit(code)
}


// commitDie prints an error and exits for the commit subcommand.
func commitDie(flags globalFlags, code int, msg string) {
	if flags.format == formatJSON {
		emitJSON("commit", nil, &jsonError{Code: code, Message: msg}, nil)
	} else {
		fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	}
	os.Exit(code)
}

// refShortName strips the refs/heads/ prefix from a ref for display.
func refShortName(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

// firstLine returns the first line of a multi-line string.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func runWip(flags globalFlags, args []string) {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		wipDie(flags, 4, err.Error())
	}
	sgDir := repo.SafegitDir(gitDir)

	// "wip list" subcommand
	if len(args) > 0 && args[0] == "list" {
		wips, err := wip.List(sgDir)
		if err != nil {
			wipDie(flags, 1, err.Error())
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
		wipDie(flags, 2, "usage: safegit wip <file1> [<file2> ...] | safegit wip list")
	}

	result, err := wip.Create(sgDir, args)
	if err != nil {
		wipDie(flags, 1, err.Error())
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
		wipDie(flags, 4, err.Error())
	}
	sgDir := repo.SafegitDir(gitDir)

	if len(args) == 0 {
		wipDie(flags, 2, "usage: safegit unwip <wip-id> [--force]")
	}

	wipID := args[0]
	restored, err := wip.Restore(sgDir, wipID, flags.force)
	if err != nil {
		wipDie(flags, 1, err.Error())
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

// wipDie prints an error and exits for wip/unwip subcommands.
func wipDie(flags globalFlags, code int, msg string) {
	if flags.format == formatJSON {
		emitJSON("wip", nil, &jsonError{Code: code, Message: msg}, nil)
	} else {
		fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	}
	os.Exit(code)
}

func runDoctor(flags globalFlags) {
	gitDir := mustGitDir(flags)

	type checkResult struct {
		Name   string `json:"name"`
		Status string `json:"status"` // "ok", "warn", "error"
		Detail string `json:"detail,omitempty"`
	}

	var checks []checkResult

	// Check 1: Is safegit initialized?
	if repo.IsInitialized(gitDir) {
		checks = append(checks, checkResult{Name: "initialized", Status: "ok"})
	} else {
		checks = append(checks, checkResult{Name: "initialized", Status: "error", Detail: "run 'safegit init'"})
	}

	sgDir := repo.SafegitDir(gitDir)

	// Check 2: Orphan tmp dirs
	if repo.IsInitialized(gitDir) {
		orphans, err := index.GarbageCollect(sgDir)
		if err != nil {
			checks = append(checks, checkResult{Name: "tmp_dirs", Status: "warn", Detail: err.Error()})
		} else if orphans > 0 {
			checks = append(checks, checkResult{
				Name:   "tmp_dirs",
				Status: "warn",
				Detail: fmt.Sprintf("cleaned %d orphan tmp dir(s)", orphans),
			})
		} else {
			checks = append(checks, checkResult{Name: "tmp_dirs", Status: "ok"})
		}
	}

	// Check 3: Stale locks
	if repo.IsInitialized(gitDir) {
		locksDir := filepath.Join(sgDir, "locks", "refs", "heads")
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
		orphans, err := wip.OrphanLocks(sgDir)
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
		ref, refErr := git.HeadRef()
		if refErr == nil && ref != "" {
			lastEntry, entryErr := oplog.LastRefUpdate(sgDir, ref)
			if entryErr == nil && lastEntry != nil {
				if sha := oplog.TipSHA(lastEntry.Extra); sha != "" {
					tipSHA, tipErr := git.RevParse(ref)
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

	// Check 7: NFS/network filesystem detection
	{
		var buf syscall.Statfs_t
		if err := syscall.Statfs(gitDir, &buf); err != nil {
			checks = append(checks, checkResult{Name: "filesystem", Status: "warn", Detail: fmt.Sprintf("statfs failed: %v", err)})
		} else {
			// Known network filesystem magic numbers (Linux)
			const (
				nfsMagic  int64 = 0x6969
				cifsMagic int64 = 0xFF534D42
				smbMagic  int64 = 0x517B
				fuseMagic int64 = 0x65735546
			)
			fsType := int64(buf.Type)
			var fsName string
			switch fsType {
			case nfsMagic:
				fsName = "NFS"
			case cifsMagic:
				fsName = "CIFS/SMB"
			case smbMagic:
				fsName = "SMB"
			case fuseMagic:
				fsName = "FUSE (possibly SSHFS)"
			}
			if fsName != "" {
				checks = append(checks, checkResult{
					Name:   "filesystem",
					Status: "warn",
					Detail: fmt.Sprintf("network filesystem detected: %s (lock atomicity not guaranteed)", fsName),
				})
			} else {
				checks = append(checks, checkResult{Name: "filesystem", Status: "ok"})
			}
		}
	}

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
		repoRoot, rootErr := git.RepoRoot()
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

func runGC(flags globalFlags) {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		if flags.format == formatJSON {
			emitJSON("gc", nil, &jsonError{Code: 4, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(4)
	}

	sgDir := repo.SafegitDir(gitDir)
	cfg, _ := loadConfig(flags, gitDir)

	if flags.dryRun {
		// Dry run: report what would be cleaned
		orphanDirs, err := index.GarbageCollectDryRun(sgDir)
		if err != nil {
			if flags.format == formatJSON {
				emitJSON("gc", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
			} else {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
			}
			os.Exit(1)
		}

		orphanWipLocks, _ := wip.OrphanLocks(sgDir)

		// Check for legacy queue directory
		queueDir := filepath.Join(sgDir, "queue")
		hasLegacyQueue := false
		if info, err := os.Stat(queueDir); err == nil && info.IsDir() {
			hasLegacyQueue = true
		}

		logSizeMB := float64(0)
		logSize, _ := oplog.LogSize(sgDir)
		if logSize > 0 {
			logSizeMB = float64(logSize) / (1024 * 1024)
		}
		maxMB := 100
		if cfg != nil && cfg.Log.MaxSizeMB > 0 {
			maxMB = cfg.Log.MaxSizeMB
		}
		wouldRotate := logSize >= int64(maxMB)*1024*1024

		if flags.format == formatJSON {
			emitJSON("gc", map[string]interface{}{
				"dryRun":           true,
				"orphanDirs":       len(orphanDirs),
				"orphanWipLocks":   len(orphanWipLocks),
				"hasLegacyQueue":   hasLegacyQueue,
				"logSizeMB":        logSizeMB,
				"wouldRotateLog":   wouldRotate,
			}, nil, nil)
		} else if !flags.quiet {
			fmt.Printf("would remove %d orphan tmp dir(s)\n", len(orphanDirs))
			if len(orphanWipLocks) > 0 {
				fmt.Printf("would remove %d orphan wip-lock(s)\n", len(orphanWipLocks))
			}
			if hasLegacyQueue {
				fmt.Println("would remove legacy queue directory")
			}
			fmt.Printf("log size: %.1f MB (max: %d MB)", logSizeMB, maxMB)
			if wouldRotate {
				fmt.Print(" -- would rotate")
			}
			fmt.Println()
		}
		return
	}

	// Actual GC
	removed, err := index.GarbageCollect(sgDir)
	if err != nil {
		if flags.format == formatJSON {
			emitJSON("gc", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(1)
	}

	// Clean orphan wip-locks
	wipCleaned, wipErr := wip.CleanOrphanLocks(sgDir)
	if wipErr != nil {
		if flags.format == formatJSON {
			emitJSON("gc", nil, &jsonError{Code: 1, Message: wipErr.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error cleaning wip locks: %v\n", wipErr)
		}
		os.Exit(1)
	}

	// Clean up legacy queue directory (removed in v0.2)
	queueDir := filepath.Join(sgDir, "queue")
	queueRemoved := false
	if info, err := os.Stat(queueDir); err == nil && info.IsDir() {
		os.RemoveAll(queueDir)
		queueRemoved = true
	}

	// Log rotation
	maxMB := 100
	if cfg != nil && cfg.Log.MaxSizeMB > 0 {
		maxMB = cfg.Log.MaxSizeMB
	}
	rotated, rotErr := oplog.Rotate(sgDir, maxMB)
	if rotErr != nil && !flags.quiet {
		fmt.Fprintf(os.Stderr, "warning: log rotation failed: %v\n", rotErr)
	}

	if flags.format == formatJSON {
		data := map[string]interface{}{
			"removed":         removed,
			"wipLocksRemoved": wipCleaned,
			"queueRemoved":    queueRemoved,
			"logRotated":      rotated,
		}
		emitJSON("gc", data, nil, nil)
	} else if !flags.quiet {
		fmt.Printf("removed %d orphan tmp dir(s)\n", removed)
		if wipCleaned > 0 {
			fmt.Printf("removed %d orphan wip-lock(s)\n", wipCleaned)
		}
		if queueRemoved {
			fmt.Println("removed legacy queue directory")
		}
		if rotated {
			fmt.Println("log rotated (old log saved as log.1)")
		}
	}
}

// isHunkSpec returns true if s looks like a hunk specifier (digits, commas, dashes only).
func isHunkSpec(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c != ',' && c != '-' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// coordGuard runs coord.Check and prints a refusal if dirty (unless force).
// Returns exit code 5 if dirty, 0 if clean, or 1 on error.
func coordGuard(flags globalFlags, sgDir, operation string) int {
	dirty, err := coord.Check(sgDir)
	if err != nil {
		if flags.format == formatJSON {
			emitJSON(operation, nil, &jsonError{Code: 1, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		return 1
	}
	if dirty != nil && !flags.force {
		if flags.format == formatJSON {
			emitJSON(operation, nil, &jsonError{Code: 5, Message: dirty.Refuse(operation)}, nil)
		} else {
			fmt.Fprint(os.Stderr, dirty.Refuse(operation))
		}
		return 5
	}
	if dirty != nil {
		// --force bypass: log that coordination guard was skipped
		_ = oplog.Append(sgDir, oplog.Entry{
			Op: "coordination_bypassed",
			Extra: map[string]interface{}{
				"operation": operation,
				"modified":  len(dirty.ModifiedFiles),
				"wipLocks":  len(dirty.WipLocks),
			},
		})
	}
	return 0
}

// syncMainIndex runs git read-tree HEAD to keep the main index in sync after tree mutations.
func syncMainIndex(flags globalFlags, op string) {
	if err := git.SyncMainIndex("HEAD"); err != nil {
		if !flags.quiet {
			fmt.Fprintf(os.Stderr, "warning: failed to sync main index after %s: %v\n", op, err)
		}
	}
}

func runCheckout(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := coordGuard(flags, sgDir, "checkout"); code != 0 {
		return code
	}

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: safegit checkout <ref>")
		return 2
	}

	// Capture old HEAD for oplog
	ctx := context.Background()
	oldHead, _ := git.RevParse("HEAD")

	stdout, stderr, err := git.Run(ctx, append([]string{"checkout"}, args...)...)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if err != nil {
		return 1
	}

	syncMainIndex(flags, "checkout")

	newHead, _ := git.RevParse("HEAD")
	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "checkout",
		Extra: map[string]interface{}{
			"ref":  args[0],
			"from": oldHead,
			"to":   newHead,
		},
	})
	return 0
}

func runPull(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := coordGuard(flags, sgDir, "pull"); code != 0 {
		return code
	}

	// Parse optional remote, branch, --ff-only
	ctx := context.Background()
	remote := "origin"
	branch := ""
	ffOnly := true // default to ff-only for safety
	var extra []string

	for _, a := range args {
		switch a {
		case "--ff-only":
			ffOnly = true
		case "--no-ff":
			ffOnly = false
		default:
			extra = append(extra, a)
		}
	}
	if len(extra) >= 1 {
		remote = extra[0]
	}
	if len(extra) >= 2 {
		branch = extra[1]
	}

	// Step 1: fetch
	fetchArgs := []string{"fetch", remote}
	if branch != "" {
		fetchArgs = append(fetchArgs, branch)
	}
	_, stderr, err := git.Run(ctx, fetchArgs...)
	if err != nil {
		fmt.Fprint(os.Stderr, stderr)
		return 1
	}

	// Step 2: merge
	mergeArgs := []string{"merge"}
	if ffOnly {
		mergeArgs = append(mergeArgs, "--ff-only")
	}
	// Merge the fetched ref
	mergeTarget := "FETCH_HEAD"
	mergeArgs = append(mergeArgs, mergeTarget)
	stdout, stderr, err := git.Run(ctx, mergeArgs...)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if err != nil {
		return 1
	}

	syncMainIndex(flags, "pull")

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "pull",
		Extra: map[string]interface{}{
			"remote": remote,
			"branch": branch,
		},
	})
	return 0
}

func runMerge(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := coordGuard(flags, sgDir, "merge"); code != 0 {
		return code
	}

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: safegit merge <branch>")
		return 2
	}

	ctx := context.Background()
	stdout, stderr, err := git.Run(ctx, append([]string{"merge"}, args...)...)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if err != nil {
		return 1
	}

	syncMainIndex(flags, "merge")

	resultSHA, _ := git.RevParse("HEAD")
	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "merge",
		Extra: map[string]interface{}{
			"branch": args[0],
			"result": resultSHA,
		},
	})
	return 0
}

func runRebase(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := coordGuard(flags, sgDir, "rebase"); code != 0 {
		return code
	}

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: safegit rebase <upstream>")
		return 2
	}

	ctx := context.Background()
	stdout, stderr, err := git.Run(ctx, append([]string{"rebase"}, args...)...)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if err != nil {
		return 1
	}

	syncMainIndex(flags, "rebase")

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "rebase",
		Extra: map[string]interface{}{
			"upstream": args[0],
		},
	})
	return 0
}

func runReset(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	sgDir := repo.SafegitDir(gitDir)

	// Only guard --hard resets (those are the tree-mutating ones)
	isHard := false
	for _, a := range args {
		if a == "--hard" {
			isHard = true
			break
		}
	}

	if isHard {
		if code := coordGuard(flags, sgDir, "reset --hard"); code != 0 {
			return code
		}
	}

	ctx := context.Background()
	stdout, stderr, err := git.Run(ctx, append([]string{"reset"}, args...)...)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if err != nil {
		return 1
	}

	if isHard {
		syncMainIndex(flags, "reset")
	}

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "reset",
		Extra: map[string]interface{}{
			"args": strings.Join(args, " "),
		},
	})
	return 0
}

func runBisect(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	sgDir := repo.SafegitDir(gitDir)

	// Guard tree-moving subcommands (good, bad, reset, start with a rev)
	needsGuard := len(args) == 0
	if len(args) > 0 {
		switch args[0] {
		case "good", "bad", "old", "new", "reset", "start":
			needsGuard = true
		}
	}

	if needsGuard {
		if code := coordGuard(flags, sgDir, "bisect"); code != 0 {
			return code
		}
	}

	ctx := context.Background()
	stdout, stderr, err := git.Run(ctx, append([]string{"bisect"}, args...)...)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if err != nil {
		return 1
	}

	syncMainIndex(flags, "bisect")

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "bisect",
		Extra: map[string]interface{}{
			"args": strings.Join(args, " "),
		},
	})
	return 0
}

// runPassthrough executes a git command directly, forwarding all args.
// Used for read-only commands (status, diff, log, show).
func runPassthrough(gitCmd string, args []string) int {
	cmd := exec.Command("git", append([]string{gitCmd}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

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

func runUnlock(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		if flags.format == formatJSON {
			emitJSON("unlock", nil, &jsonError{Code: 4, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		return 4
	}

	if len(args) == 0 {
		msg := "usage: safegit unlock <ref> [--force]"
		if flags.format == formatJSON {
			emitJSON("unlock", nil, &jsonError{Code: 2, Message: msg}, nil)
		} else {
			fmt.Fprintln(os.Stderr, msg)
		}
		return 2
	}

	ref := args[0]
	if !strings.HasPrefix(ref, "refs/") {
		ref = "refs/heads/" + ref
	}

	sgDir := repo.SafegitDir(gitDir)

	// Check if lock exists
	lp := filepath.Join(sgDir, "locks", ref+".lock")
	if _, err := os.Stat(lp); os.IsNotExist(err) {
		msg := fmt.Sprintf("no lock held on %s", ref)
		if flags.format == formatJSON {
			emitJSON("unlock", nil, &jsonError{Code: 1, Message: msg}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %s\n", msg)
		}
		return 1
	}

	// Unless --force, refuse if holder is alive
	if !flags.force {
		stale, err := lock.IsStale(lp)
		if err != nil {
			msg := fmt.Sprintf("cannot check lock status: %v", err)
			if flags.format == formatJSON {
				emitJSON("unlock", nil, &jsonError{Code: 1, Message: msg}, nil)
			} else {
				fmt.Fprintf(os.Stderr, "error: %s\n", msg)
			}
			return 1
		}
		if !stale {
			msg := fmt.Sprintf("lock on %s is held by a live process; use --force to override", ref)
			if flags.format == formatJSON {
				emitJSON("unlock", nil, &jsonError{Code: 1, Message: msg}, nil)
			} else {
				fmt.Fprintf(os.Stderr, "error: %s\n", msg)
			}
			return 1
		}
	}

	// Release the lock
	if err := lock.ForceRelease(sgDir, ref); err != nil {
		if flags.format == formatJSON {
			emitJSON("unlock", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		return 1
	}

	if flags.format == formatJSON {
		emitJSON("unlock", map[string]string{"ref": ref, "action": "released"}, nil, nil)
	} else if !flags.quiet {
		fmt.Printf("lock on %s released\n", refShortName(ref))
	}
	return 0
}

func runBranch(flags globalFlags, args []string) int {
	// Parse flags
	deleteFlag := false
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d", "--delete":
			deleteFlag = true
		default:
			positional = append(positional, args[i])
		}
	}

	ctx := context.Background()

	if deleteFlag {
		// safegit branch -d <name>
		if len(positional) == 0 {
			fmt.Fprintln(os.Stderr, "usage: safegit branch -d <name>")
			return 2
		}
		name := positional[0]

		// Check if deleting the current branch -- that would mutate tree
		headRef, _ := git.HeadRef()
		if headRef == "refs/heads/"+name {
			gitDir := mustGitDir(flags)
			if err := repo.EnsureInitialized(gitDir); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 4
			}
			sgDir := repo.SafegitDir(gitDir)
			if code := coordGuard(flags, sgDir, "branch -d"); code != 0 {
				return code
			}
		}

		stdout, stderr, err := git.Run(ctx, "branch", "-d", name)
		if stdout != "" {
			fmt.Print(stdout)
		}
		if stderr != "" {
			fmt.Fprint(os.Stderr, stderr)
		}
		if err != nil {
			return 1
		}
		return 0
	}

	if len(positional) == 0 {
		// safegit branch -- list branches
		return runPassthrough("branch", nil)
	}

	// safegit branch <name> -- create branch (no coordination needed)
	name := positional[0]
	stdout, stderr, err := git.Run(ctx, "branch", name)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if err != nil {
		return 1
	}
	if !flags.quiet {
		fmt.Printf("branch %s created\n", name)
	}
	return 0
}


// emitJSON writes a JSON envelope to stdout.
func emitJSON(command string, data interface{}, errVal *jsonError, warnings []string) {
	resp := jsonResponse{
		OK:       errVal == nil,
		Command:  command,
		Data:     data,
		Error:    errVal,
		Warnings: warnings,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(resp)
}
