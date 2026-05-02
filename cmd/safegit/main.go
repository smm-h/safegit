package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/stage"
)

// Set via -ldflags "-X main.version=..." at build time.
// Falls back to the module version embedded by go install.
var version = ""

func init() {
	if version != "" {
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "(devel)" {
		version = info.Main.Version
	} else {
		version = "dev"
	}
}

// globalFlags holds flags parsed before command dispatch.
type globalFlags struct {
	quiet      bool
	verbose    bool
	noColor    bool
	dryRun     bool
	force      bool
	configPath string
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
	case "wip":
		runWip(flags, cmdArgs)
	case "doctor":
		runDoctor(flags, cmdArgs)
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
	case "push":
		os.Exit(runPush(flags, cmdArgs))
	case "hook":
		os.Exit(runHook(flags, cmdArgs))
	case "config":
		os.Exit(runConfig(flags, cmdArgs))
	case "unlock":
		os.Exit(runUnlock(flags, cmdArgs))
	case "undo":
		runUndo(flags, cmdArgs)
	case "cherry-pick":
		os.Exit(runGuardedPassthrough(flags, "cherry-pick", cmdArgs))
	case "revert":
		os.Exit(runGuardedPassthrough(flags, "revert", cmdArgs))
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
	fmt.Print(usageText())
}

func unknownCommand(flags globalFlags, cmd string) {
	fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
	fmt.Fprint(os.Stderr, usageText())
}

func usageText() string {
	return `Usage: safegit <command> [options]

Commands:
  init        Initialize .git/safegit/ directory structure
  commit      Stage and commit files atomically (--amend to amend/reword)
  undo        Reverse the last commit/amend/reword using the oplog
  wip         Save/restore work-in-progress snapshots
  checkout    Checkout a ref (guarded: checks for uncommitted work)
  pull        Fetch + merge (guarded, default --ff-only)
  merge       Merge a branch (guarded)
  rebase      Rebase onto upstream (guarded)
  reset       Reset (guarded for --hard)
  bisect      Bisect (guarded: checks for uncommitted work)
  push        Push with pre-pre-push hooks and retry logic
  hook        Manage pre-pre-push hooks (list, run, install)
  cherry-pick Cherry-pick commits (guarded)
  revert      Revert commits (guarded)
  config      Show or set configuration values
  unlock      Release a stale ref lock (--force to override live holder)
  doctor      Health checks (--fix to garbage-collect and repair)
  version     Print version and build info
  help        Print this help

Global flags:
  --config <path>       Override config.json path
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
	ctx := context.Background()
	gitDir, err := git.GitDir(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "not a git repository (or git is not installed)")
		os.Exit(3)
	}
	// Resolve to absolute path
	abs, err := filepath.Abs(gitDir)
	if err != nil {
		abs = gitDir
	}
	return abs
}

// die prints an error for the given subcommand and exits with code.
func die(flags globalFlags, cmd string, code int, msg string) {
	fmt.Fprintf(os.Stderr, "error: %s\n", msg)
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

// parseFileSpecs converts raw file arguments (possibly with hunk suffixes like
// "file.txt:1,3") into commit.FileSpec structs. Dies on malformed hunk specs.
func parseFileSpecs(files []string, flags globalFlags, cmd string) []commit.FileSpec {
	specs := make([]commit.FileSpec, 0, len(files))
	for _, f := range files {
		spec := commit.FileSpec{}
		colonIdx := strings.LastIndex(f, ":")
		// Only attempt hunk parsing when:
		// 1. There is a colon (not at position 0)
		// 2. The suffix looks like a hunk spec
		// 3. The full string doesn't exist as a file (avoids misidentifying "1:2" as hunk spec)
		if colonIdx > 0 && isHunkSpec(f[colonIdx+1:]) && !fileExists(f) {
			hunks, err := stage.ParseHunkSpec(f[colonIdx+1:])
			if err != nil {
				die(flags, cmd, 2, fmt.Sprintf("invalid hunk spec in %q: %v", f, err))
			}
			spec.Path = f[:colonIdx]
			spec.Hunks = hunks
		} else {
			spec.Path = f
		}
		specs = append(specs, spec)
	}
	return specs
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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

