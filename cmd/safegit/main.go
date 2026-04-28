package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/index"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
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
	case "doctor":
		runDoctor(flags)
	case "gc":
		runGC(flags)
	case "version":
		runVersion(flags)
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
			// First non-flag arg starts the command; pass everything through.
			rest = append(rest, args[i:]...)
			return f, rest
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
  status      Show per-agent working tree status
  push        Push with pre-pre-push hooks and CAS retry
  wip         Save/restore work-in-progress snapshots
  log         Query the operation log
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
  --force, -f           Skip safety checks
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

	sgDir := repo.SafegitDir(gitDir)
	if flags.format == formatJSON {
		emitJSON("init", map[string]string{"safegit_dir": sgDir}, nil, nil)
	} else if !flags.quiet {
		fmt.Printf("initialized safegit at %s\n", sgDir)
	}
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

	// Check 4: Config readable
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

	if flags.format == formatJSON {
		emitJSON("gc", map[string]int{"removed": removed}, nil, nil)
	} else if !flags.quiet {
		fmt.Printf("removed %d orphan tmp dir(s)\n", removed)
	}
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
