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
		case "checkout":
			return runCheckout(gf, args)
		case "merge":
			return runMerge(gf, args)
		case "rebase":
			return runRebase(gf, args)
		case "reset":
			return runReset(gf, args)
		case "bisect":
			return runBisect(gf, args)
		case "cherry-pick":
			return runGuardedPassthrough(gf, "cherry-pick", args)
		case "revert":
			return runGuardedPassthrough(gf, "revert", args)
		}
		return 1
	}

	app.Command("commit", "stage and commit files atomically", func(kwargs map[string]interface{}) int {
		gf := globalsToFlags(kwargs)
		messages := kwargsStrSlice(kwargs["m"])
		var messageFile string
		if v := kwargs["F"]; v != nil {
			messageFile = v.(string)
		}
		var branch string
		if v := kwargs["branch"]; v != nil {
			branch = v.(string)
		}
		amend := kwargs["amend"].(bool)
		allowEmpty := kwargs["allow_empty"].(bool)
		trailers := kwargsStrSlice(kwargs["trailer"])
		files := kwargsStrSlice(kwargs["files"])
		runCommit(gf, messages, messageFile, branch, amend, allowEmpty, trailers, files)
		return 0
	},
		strictcli.WithFlags(
			strictcli.StringFlag("m", "commit message (repeatable)", strictcli.Short("m"), strictcli.Repeatable()),
			strictcli.StringFlag("F", "read commit message from file", strictcli.Short("F"), strictcli.Default(nil)),
			strictcli.StringFlag("branch", "commit to a different branch", strictcli.Default(nil)),
			strictcli.BoolFlag("amend", "amend the current HEAD commit"),
			strictcli.BoolFlag("allow-empty", "allow commits with no file changes"),
			strictcli.StringFlag("trailer", "add a trailer (repeatable)", strictcli.Repeatable()),
		),
		strictcli.WithArgs(
			strictcli.NewArg("files", "files to commit (supports hunk specs: file.go:1,3)", strictcli.ArgRequired(false), strictcli.Variadic()),
		),
	)
	app.Passthrough("checkout", "checkout a ref (guarded)", pt)
	app.Passthrough("merge", "merge a branch (guarded)", pt)
	app.Passthrough("rebase", "rebase onto upstream (guarded)", pt)
	app.Passthrough("reset", "reset HEAD (guarded for --hard)", pt)
	app.Passthrough("bisect", "bisect (guarded)", pt)
	app.Command("push", "push with pre-hooks and retry", func(kwargs map[string]interface{}) int {
		gf := globalsToFlags(kwargs)
		noPrePrePush := kwargs["no_pre_pre_push"].(bool)
		forcePush := kwargs["force_push"].(bool) || kwargs["force"].(bool)
		remote := "origin"
		if v := kwargs["remote"]; v != nil {
			remote = v.(string)
		}
		refspecs := kwargsStrSlice(kwargs["refspecs"])
		return runPush(gf, noPrePrePush, forcePush, remote, refspecs)
	},
		strictcli.WithFlags(
			strictcli.BoolFlag("no-pre-pre-push", "skip pre-pre-push hooks"),
			strictcli.BoolFlag("force-push", "force push to remote"),
		),
		strictcli.WithArgs(
			strictcli.NewArg("remote", "remote name", strictcli.ArgRequired(false)),
			strictcli.NewArg("refspecs", "refs to push", strictcli.ArgRequired(false), strictcli.Variadic()),
		),
	)
	app.Command("pull", "fetch and merge (default --ff-only)", func(kwargs map[string]interface{}) int {
		gf := globalsToFlags(kwargs)
		// Determine merge mode from mutex group
		var mode pullMode
		if kwargs["ff_only"].(bool) {
			mode = pullFFOnly
		} else if kwargs["ff"].(bool) {
			mode = pullFF
		} else if kwargs["no_ff"].(bool) {
			mode = pullNoFF
		}
		remote := "origin"
		if v := kwargs["remote"]; v != nil {
			remote = v.(string)
		}
		var branch string
		if v := kwargs["branch"]; v != nil {
			branch = v.(string)
		}
		return runPull(gf, mode, remote, branch)
	},
		strictcli.WithMutex(strictcli.MutexGroup{
			Flags: []strictcli.Flag{
				strictcli.BoolFlag("ff-only", "fast-forward only (fail if not possible)"),
				strictcli.BoolFlag("ff", "fast-forward if possible, merge commit otherwise"),
				strictcli.BoolFlag("no-ff", "always create a merge commit"),
			},
		}),
		strictcli.WithArgs(
			strictcli.NewArg("remote", "remote name", strictcli.ArgRequired(false)),
			strictcli.NewArg("branch", "branch to pull", strictcli.ArgRequired(false)),
		),
	)
	cg := app.Group("config", "show or set configuration values")
	cg.Command("show", "show all configuration", func(kwargs map[string]interface{}) int {
		return runConfigShow(globalsToFlags(kwargs))
	})
	cg.Command("get", "get a configuration value", func(kwargs map[string]interface{}) int {
		key := kwargs["key"].(string)
		return runConfigGet(globalsToFlags(kwargs), key)
	},
		strictcli.WithArgs(strictcli.NewArg("key", "configuration key")),
	)
	cg.Command("set", "set a configuration value", func(kwargs map[string]interface{}) int {
		key := kwargs["key"].(string)
		value := kwargs["value"].(string)
		return runConfigSet(globalsToFlags(kwargs), key, value)
	},
		strictcli.WithArgs(strictcli.NewArg("key", "configuration key"), strictcli.NewArg("value", "configuration value")),
	)

	hg := app.Group("hook", "manage pre-pre-push hooks")
	hg.Command("list", "list installed hooks", func(kwargs map[string]interface{}) int {
		return hookList(globalsToFlags(kwargs))
	})
	hg.Command("run", "run hooks", func(kwargs map[string]interface{}) int {
		var name string
		if v := kwargs["name"]; v != nil {
			name = v.(string)
		}
		return hookRun(globalsToFlags(kwargs), name)
	},
		strictcli.WithArgs(strictcli.NewArg("name", "hook name to run", strictcli.ArgRequired(false))),
	)
	hg.Command("install", "install a hook from a file", func(kwargs map[string]interface{}) int {
		path := kwargs["path"].(string)
		return hookInstall(globalsToFlags(kwargs), path)
	},
		strictcli.WithArgs(strictcli.NewArg("path", "path to hook script")),
	)
	app.Command("doctor", "health checks and repair", func(kwargs map[string]interface{}) int {
		runDoctor(globalsToFlags(kwargs), kwargs)
		return 0
	},
		strictcli.WithMutex(strictcli.MutexGroup{
			Flags: []strictcli.Flag{
				strictcli.BoolFlag("diagnose", "run health checks without fixing"),
				strictcli.BoolFlag("fix", "run health checks and fix issues"),
				strictcli.BoolFlag("uninstall", "remove safegit from this repository"),
			},
		}),
	)
	app.Command("rewrite-author", "rewrite author/committer across history", func(kwargs map[string]interface{}) int {
		return runRewriteAuthor(globalsToFlags(kwargs), kwargs)
	},
		strictcli.WithFlags(
			strictcli.StringFlag("old-name", "current author/committer name to match", strictcli.Default(nil)),
			strictcli.StringFlag("new-name", "replacement name", strictcli.Default(nil)),
			strictcli.StringFlag("old-email", "current author/committer email to match", strictcli.Default(nil)),
			strictcli.StringFlag("new-email", "replacement email", strictcli.Default(nil)),
			strictcli.BoolFlag("push", "force-push after rewriting"),
		),
		strictcli.WithDependencies(
			strictcli.CoRequired{Flags: []string{"old-name", "new-name"}},
			strictcli.CoRequired{Flags: []string{"old-email", "new-email"}},
		),
	)
	sg := app.Group("scrub", "surgically rewrite history to remove sensitive content")
	sg.Command("file", "replace or remove a file across history", func(kwargs map[string]interface{}) int {
		return runScrubFile(globalsToFlags(kwargs), kwargs)
	},
		strictcli.WithFlags(
			strictcli.StringFlag("from", "first commit to include in the rewrite"),
			strictcli.StringFlag("reason", "audit trail explaining why the scrub is needed"),
		),
		strictcli.WithArgs(
			strictcli.NewArg("file", "repo-relative file path to scrub"),
		),
	)
	sg.Command("match", "replace pattern matches across history", func(kwargs map[string]interface{}) int {
		return runScrubMatch(globalsToFlags(kwargs), kwargs)
	},
		strictcli.WithFlags(
			strictcli.StringFlag("pattern", "regex pattern to search for"),
			strictcli.StringFlag("replace", "replacement string"),
			strictcli.StringFlag("reason", "audit trail explaining why the scrub is needed"),
		),
		strictcli.WithMutex(strictcli.MutexGroup{
			Flags: []strictcli.Flag{
				strictcli.StringFlag("from", "first commit to include in the rewrite", strictcli.Default(nil)),
				strictcli.BoolFlag("entire-history", "rewrite all commits"),
			},
		}),
	)
	app.Passthrough("cherry-pick", "cherry-pick commits (guarded)", pt)
	app.Passthrough("revert", "revert commits (guarded)", pt)
	app.Command("undo", "reverse last commit/amend/reword via oplog", func(kwargs map[string]interface{}) int {
		bypassSession := kwargs["bypass_session"].(bool)
		runUndo(globalsToFlags(kwargs), bypassSession)
		return 0
	},
		strictcli.WithFlags(
			strictcli.BoolFlag("bypass-session", "undo across all sessions, ignoring session ID"),
		),
	)
	app.Command("redo", "restore what undo removed (one-shot)", func(kwargs map[string]interface{}) int {
		bypassSession := kwargs["bypass_session"].(bool)
		runRedo(globalsToFlags(kwargs), bypassSession)
		return 0
	},
		strictcli.WithFlags(
			strictcli.BoolFlag("bypass-session", "redo across all sessions, ignoring session ID"),
		),
	)
	app.Command("unlock", "release a stale ref lock", func(kwargs map[string]interface{}) int {
		ref := kwargs["ref"].(string)
		return runUnlock(globalsToFlags(kwargs), ref)
	},
		strictcli.WithArgs(strictcli.NewArg("ref", "the ref name to unlock")),
	)
	app.Command("version", "print version and build info", func(kwargs map[string]interface{}) int {
		runVersion(globalsToFlags(kwargs))
		return 0
	})

	app.Run()
}

// kwargsStrSlice converts a []interface{} value (from repeatable flags or
// variadic args) to []string. Returns nil if v is nil.
func kwargsStrSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	raw := v.([]interface{})
	out := make([]string, len(raw))
	for i, elem := range raw {
		out[i] = elem.(string)
	}
	return out
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

// requireCleanTree dies if the working tree has uncommitted changes,
// unless --force is set.
func requireCleanTree(ctx context.Context, flags globalFlags, cmd string) {
	statusOut, _, err := git.Run(ctx, "status", "--porcelain")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("checking working tree: %v", err))
	}
	if strings.TrimSpace(statusOut) != "" && !flags.force {
		die(flags, cmd, 1, "working tree is dirty; commit changes or use --force to skip this check")
	}
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

