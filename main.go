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
	"github.com/smm-h/strictcli/go/strictcli"
)

// Set via -ldflags "-X main.version=..." at build time.
// Falls back to the module version embedded by go install.
var version = ""

func init() {
	if version != "" {
		version = strings.TrimPrefix(version, "v")
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "(devel)" {
		version = strings.TrimPrefix(info.Main.Version, "v")
	} else {
		version = "dev"
	}
}

// globalFlags holds flags parsed before command dispatch.
type globalFlags struct {
	quiet      bool
	verbose    bool
	dryRun     bool
	force      bool
	configPath string
}

func main() {
	app := strictcli.NewApp("safegit", version, "concurrency-safe git for multi-agent use")

	app.GlobalFlag(strictcli.BoolFlag("quiet", "suppress non-essential output", strictcli.Short("q")))
	app.GlobalFlag(strictcli.BoolFlag("verbose", "verbose output"))
	app.GlobalFlag(strictcli.BoolFlag("dry-run", "preview changes without writing", strictcli.Short("n")))
	app.GlobalFlag(strictcli.BoolFlag("force", "force operation", strictcli.Short("f")))
	app.GlobalFlag(strictcli.StringFlag("config", "config file path", strictcli.Default("")))

	pt := func(name string, args []string, globals map[string]interface{}) int {
		gf := globalsToFlags(globals)
		switch name {
		case "commit":
			runCommit(gf, args)
			return 0
		case "undo":
			runUndo(gf, args)
			return 0
		case "doctor":
			runDoctor(gf, args)
			return 0
		case "rewrite-author":
			runRewriteAuthor(gf, args)
			return 0
		case "version":
			runVersion(gf)
			return 0
		case "checkout":
			return runCheckout(gf, args)
		case "pull":
			return runPull(gf, args)
		case "merge":
			return runMerge(gf, args)
		case "rebase":
			return runRebase(gf, args)
		case "reset":
			return runReset(gf, args)
		case "bisect":
			return runBisect(gf, args)
		case "push":
			return runPush(gf, args)
		case "hook":
			return runHook(gf, args)
		case "config":
			return runConfig(gf, args)
		case "unlock":
			return runUnlock(gf, args)
		case "cherry-pick":
			return runGuardedPassthrough(gf, "cherry-pick", args)
		case "revert":
			return runGuardedPassthrough(gf, "revert", args)
		}
		return 1
	}

	app.Passthrough("commit", "stage and commit files atomically", pt)
	app.Passthrough("undo", "reverse last commit/amend/reword via oplog", pt)
	app.Passthrough("checkout", "checkout a ref (guarded)", pt)
	app.Passthrough("pull", "fetch and merge (guarded, default --ff-only)", pt)
	app.Passthrough("merge", "merge a branch (guarded)", pt)
	app.Passthrough("rebase", "rebase onto upstream (guarded)", pt)
	app.Passthrough("reset", "reset HEAD (guarded for --hard)", pt)
	app.Passthrough("bisect", "bisect (guarded)", pt)
	app.Passthrough("push", "push with pre-hooks and retry", pt)
	app.Passthrough("hook", "manage pre-pre-push hooks", pt)
	app.Passthrough("config", "show or set configuration values", pt)
	app.Passthrough("unlock", "release a stale ref lock", pt)
	app.Passthrough("doctor", "health checks and repair", pt)
	app.Passthrough("rewrite-author", "rewrite author/committer across history", pt)
	app.Passthrough("cherry-pick", "cherry-pick commits (guarded)", pt)
	app.Passthrough("revert", "revert commits (guarded)", pt)
	app.Passthrough("version", "print version and build info", pt)

	app.Run()
}

// globalsToFlags converts the strictcli globals map to the globalFlags struct.
// strictcli converts flag names like "dry-run" to map keys "dry_run".
func globalsToFlags(globals map[string]interface{}) globalFlags {
	return globalFlags{
		quiet:      globals["quiet"].(bool),
		verbose:    globals["verbose"].(bool),
		dryRun:     globals["dry_run"].(bool),
		force:      globals["force"].(bool),
		configPath: globals["config"].(string),
	}
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

// commandHelp prints per-command help and exits.
func commandHelp(cmd, usage string) {
	fmt.Fprintf(os.Stderr, "Usage: safegit %s\n\n%s\n", cmd, usage)
	os.Exit(0)
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

// splitFlagValue splits a flag argument at the first '=' sign. For example,
// "--config=/path" returns ("--config", "/path", true). If there is no '='
// or the argument doesn't start with '-', it returns (arg, "", false).
func splitFlagValue(arg string) (flag, value string, hasValue bool) {
	if idx := strings.Index(arg, "="); idx >= 0 && strings.HasPrefix(arg, "-") {
		return arg[:idx], arg[idx+1:], true
	}
	return arg, "", false
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

