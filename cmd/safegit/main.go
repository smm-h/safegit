package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
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
	switch cmd {
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
  commit      Stage and commit files atomically (per-invocation index)
  status      Show per-agent working tree status
  push        Push with pre-pre-push hooks and CAS retry
  wip         Save/restore work-in-progress snapshots
  log         Query the operation log
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
