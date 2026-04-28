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
	format  outputFormat
	quiet   bool
	verbose bool
	noColor bool
	dryRun  bool
	force   bool
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
	case "stage":
		runStage(flags, cmdArgs)
	case "unstage":
		runUnstage(flags, cmdArgs)
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
	case "status":
		os.Exit(runPassthrough("status", cmdArgs))
	case "diff":
		os.Exit(runPassthrough("diff", cmdArgs))
	case "log":
		os.Exit(runPassthrough("log", cmdArgs))
	case "show":
		os.Exit(runPassthrough("show", cmdArgs))
	case "push":
		os.Exit(runPush(flags, cmdArgs))
	case "fetch":
		os.Exit(runFetch(cmdArgs))
	case "hook":
		os.Exit(runHook(flags, cmdArgs))
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
  stage       Preview hunk-level staging (creates tmp index, prints result, cleans up)
  unstage     Preview hunk-level unstaging (creates tmp index, prints result, cleans up)
  wip         Save/restore work-in-progress snapshots
  checkout    Checkout a ref (guarded: checks for uncommitted work)
  pull        Fetch + merge (guarded, default --ff-only)
  merge       Merge a branch (guarded)
  rebase      Rebase onto upstream (guarded)
  reset       Reset (guarded for --hard)
  push        Push with pre-pre-push hooks and retry logic
  fetch       Fetch from remote (git passthrough)
  hook        Manage pre-pre-push hooks (list, run, install)
  branch      List, create, or delete branches
  status      Show working tree status (git passthrough)
  diff        Show diffs (git passthrough)
  log         Show commit log (git passthrough)
  show        Show objects (git passthrough)
  doctor      Health checks (initialized? orphan dirs? stale locks?)
  gc          Garbage-collect dead tmp index directories
  version     Print version and build info
  help        Print this help

Global flags:
  --format human|json   Output format (default: human)
  --quiet, -q           Suppress non-essential output
  --verbose, -v         Verbose output
  --no-color            Disable colored output
  --dry-run, -n         Show what would be done without doing it
  --force, -f           Skip safety checks (bypass coordination guard)
`
}

// mustGitDir resolves the .git directory or exits with an error.
func mustGitDir(flags globalFlags) string {
	gitDir, err := git.GitDir()
	if err != nil {
		msg := "not a git repository (or git is not installed)"
		if flags.format == formatJSON {
			emitJSON("", nil, &jsonError{Code: 1, Message: msg}, nil)
		} else {
			fmt.Fprintln(os.Stderr, msg)
		}
		os.Exit(1)
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
			emitJSON("commit", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(1)
	}

	// Parse commit-specific flags
	var messages []string
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
		case "--branch":
			if i+1 >= len(args) {
				commitDie(flags, 2, "--branch requires an argument")
			}
			i++
			branch = args[i]
		case "--allow-empty":
			allowEmpty = true
		default:
			// Might be a flag value starting with -- that we don't recognise
			commitDie(flags, 2, fmt.Sprintf("unknown flag: %s", args[i]))
		}
	}

	if len(messages) == 0 {
		commitDie(flags, 2, "commit message required (-m)")
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
	cfg, err := repo.LoadConfig(gitDir)
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
			emitJSON("amend", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(1)
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
	cfg, err := repo.LoadConfig(gitDir)
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
			emitJSON("reword", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(1)
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
	cfg, err := repo.LoadConfig(gitDir)
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

func runStage(flags globalFlags, args []string) {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		stageDie(flags, 1, err.Error())
	}

	// Parse stage-specific flags
	var file string
	var hunksSpec string
	var deleteMode bool

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--hunks":
			if i+1 >= len(args) {
				stageDie(flags, 2, "--hunks requires an argument")
			}
			i++
			hunksSpec = args[i]
		case "--delete":
			deleteMode = true
		default:
			if strings.HasPrefix(args[i], "--") {
				stageDie(flags, 2, fmt.Sprintf("unknown flag: %s", args[i]))
			}
			if file != "" {
				stageDie(flags, 2, "only one file allowed per stage command")
			}
			file = args[i]
		}
	}

	if file == "" {
		stageDie(flags, 2, "file argument required")
	}

	sgDir := repo.SafegitDir(gitDir)

	// Create a tmp index for preview
	tmpIdx, err := index.New(sgDir)
	if err != nil {
		stageDie(flags, 1, fmt.Sprintf("creating tmp index: %v", err))
	}
	defer tmpIdx.Cleanup()

	if deleteMode {
		// Stage a deletion
		if err := git.RmCached(tmpIdx.IndexPath, file); err != nil {
			stageDie(flags, 1, fmt.Sprintf("staging deletion: %v", err))
		}
	} else if hunksSpec != "" {
		// Parse hunk indices
		hunkIndices, err := stage.ParseHunkSpec(hunksSpec)
		if err != nil {
			stageDie(flags, 2, fmt.Sprintf("invalid hunks spec: %v", err))
		}
		if err := stage.StageHunks(tmpIdx.IndexPath, file, hunkIndices); err != nil {
			stageDie(flags, 1, fmt.Sprintf("staging hunks: %v", err))
		}
	} else {
		// Stage whole file
		if err := stage.StageFile(tmpIdx.IndexPath, file); err != nil {
			stageDie(flags, 1, fmt.Sprintf("staging file: %v", err))
		}
	}

	// Write tree to get the SHA
	treeSHA, err := git.WriteTree(tmpIdx.IndexPath)
	if err != nil {
		stageDie(flags, 1, fmt.Sprintf("write-tree: %v", err))
	}

	// Determine how many hunks exist and which were applied
	totalHunks := 0
	var appliedHunks []int
	if !deleteMode {
		_, hunks, _ := stage.ExtractHunks(tmpIdx.IndexPath, file)
		if hunksSpec != "" {
			// Before staging we had N hunks; after staging some, the remaining diff shows the rest
			appliedHunks, _ = stage.ParseHunkSpec(hunksSpec)
			// Total hunks is the original count (applied + remaining)
			totalHunks = len(hunks) + len(appliedHunks)
		} else {
			// Whole file staged -- count original hunks from a fresh index
			tmpIdx2, err := index.New(sgDir)
			if err == nil {
				_, origHunks, _ := stage.ExtractHunks(tmpIdx2.IndexPath, file)
				totalHunks = len(origHunks)
				appliedHunks = make([]int, totalHunks)
				for i := range appliedHunks {
					appliedHunks[i] = i + 1
				}
				tmpIdx2.Cleanup()
			}
		}
	}

	if flags.format == formatJSON {
		data := map[string]interface{}{
			"file":         file,
			"hunksApplied": appliedHunks,
			"treeSha":      treeSHA,
			"totalHunks":   totalHunks,
		}
		if deleteMode {
			data["deleted"] = true
		}
		emitJSON("stage", data, nil, nil)
	} else if !flags.quiet {
		if deleteMode {
			fmt.Printf("staged deletion: %s\n", file)
		} else if hunksSpec != "" {
			fmt.Printf("staged hunks %v of %s (%d/%d hunks)\n", appliedHunks, file, len(appliedHunks), totalHunks)
		} else {
			fmt.Printf("staged all hunks of %s (%d hunks)\n", file, totalHunks)
		}
		fmt.Printf("tree: %s\n", treeSHA)
		fmt.Println("(preview only -- tmp index cleaned up)")
	}
}

func runUnstage(flags globalFlags, args []string) {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		stageDie(flags, 1, err.Error())
	}

	var file string
	var hunksSpec string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--hunks":
			if i+1 >= len(args) {
				stageDie(flags, 2, "--hunks requires an argument")
			}
			i++
			hunksSpec = args[i]
		default:
			if strings.HasPrefix(args[i], "--") {
				stageDie(flags, 2, fmt.Sprintf("unknown flag: %s", args[i]))
			}
			if file != "" {
				stageDie(flags, 2, "only one file allowed per unstage command")
			}
			file = args[i]
		}
	}

	if file == "" {
		stageDie(flags, 2, "file argument required")
	}

	sgDir := repo.SafegitDir(gitDir)

	// Create a tmp index, stage the file first, then unstage
	tmpIdx, err := index.New(sgDir)
	if err != nil {
		stageDie(flags, 1, fmt.Sprintf("creating tmp index: %v", err))
	}
	defer tmpIdx.Cleanup()

	// First stage the whole file so we have something to unstage from
	if err := stage.StageFile(tmpIdx.IndexPath, file); err != nil {
		stageDie(flags, 1, fmt.Sprintf("staging file for unstage preview: %v", err))
	}

	if hunksSpec != "" {
		hunkIndices, err := stage.ParseHunkSpec(hunksSpec)
		if err != nil {
			stageDie(flags, 2, fmt.Sprintf("invalid hunks spec: %v", err))
		}
		if err := stage.UnstageHunks(tmpIdx.IndexPath, file, hunkIndices); err != nil {
			stageDie(flags, 1, fmt.Sprintf("unstaging hunks: %v", err))
		}
	} else {
		if err := stage.UnstageFile(tmpIdx.IndexPath, file); err != nil {
			stageDie(flags, 1, fmt.Sprintf("unstaging file: %v", err))
		}
	}

	treeSHA, err := git.WriteTree(tmpIdx.IndexPath)
	if err != nil {
		stageDie(flags, 1, fmt.Sprintf("write-tree: %v", err))
	}

	if flags.format == formatJSON {
		data := map[string]interface{}{
			"file":    file,
			"treeSha": treeSHA,
		}
		if hunksSpec != "" {
			indices, _ := stage.ParseHunkSpec(hunksSpec)
			data["hunksUnstaged"] = indices
		}
		emitJSON("unstage", data, nil, nil)
	} else if !flags.quiet {
		if hunksSpec != "" {
			indices, _ := stage.ParseHunkSpec(hunksSpec)
			fmt.Printf("unstaged hunks %v of %s\n", indices, file)
		} else {
			fmt.Printf("unstaged %s (reset to HEAD)\n", file)
		}
		fmt.Printf("tree: %s\n", treeSHA)
		fmt.Println("(preview only -- tmp index cleaned up)")
	}
}

// stageDie prints an error and exits for stage/unstage subcommands.
func stageDie(flags globalFlags, code int, msg string) {
	if flags.format == formatJSON {
		emitJSON("stage", nil, &jsonError{Code: code, Message: msg}, nil)
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
		wipDie(flags, 1, err.Error())
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
		wipDie(flags, 1, err.Error())
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
				if sha, ok := lastEntry.Extra["sha"].(string); ok {
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
			emitJSON("gc", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(1)
	}

	sgDir := repo.SafegitDir(gitDir)
	removed, err := index.GarbageCollect(sgDir)
	if err != nil {
		if flags.format == formatJSON {
			emitJSON("gc", nil, &jsonError{Code: 1, Message: err.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(1)
	}

	// Also clean orphan wip-locks
	wipCleaned, wipErr := wip.CleanOrphanLocks(sgDir)
	if wipErr != nil {
		if flags.format == formatJSON {
			emitJSON("gc", nil, &jsonError{Code: 1, Message: wipErr.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "error cleaning wip locks: %v\n", wipErr)
		}
		os.Exit(1)
	}

	if flags.format == formatJSON {
		emitJSON("gc", map[string]int{"removed": removed, "wipLocksRemoved": wipCleaned}, nil, nil)
	} else if !flags.quiet {
		fmt.Printf("removed %d orphan tmp dir(s)\n", removed)
		if wipCleaned > 0 {
			fmt.Printf("removed %d orphan wip-lock(s)\n", wipCleaned)
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
		return 1
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
		return 1
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
		return 1
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
		return 1
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
		return 1
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
				return 1
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
