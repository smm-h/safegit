package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/hooks"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
)

// Exit codes specific to push
const (
	exitPushHookFailed  = 20
	exitPushHookTimeout = 21
	exitPushGitFailed   = 40
)

// pushRefInfo describes a single ref being pushed.
type pushRefInfo struct {
	LocalRef  string `json:"localRef"`
	LocalSHA  string `json:"localSha"`
	RemoteRef string `json:"remoteRef"`
	RemoteSHA string `json:"remoteSha"`
}

// pushResult is the JSON output for a successful push.
type pushResult struct {
	Remote   string              `json:"remote"`
	Refs     []pushRefInfo       `json:"refs"`
	HooksRun []hooks.HookResult  `json:"hooksRun,omitempty"`
}

func runPush(flags globalFlags, args []string) int {
	gitDir := mustGitDir(flags)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		pushDie(flags, 1, err.Error())
		return 1
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		pushDie(flags, 1, fmt.Sprintf("loading config: %v", err))
		return 1
	}

	// Parse push-specific flags
	noPrePrePush := false
	forceFlag := flags.force
	var positional []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-pre-pre-push":
			noPrePrePush = true
		case "--force", "-f":
			forceFlag = true
		default:
			positional = append(positional, args[i])
		}
	}

	// Resolve remote and refspecs
	remote := "origin"
	var refspecs []string
	if len(positional) >= 1 {
		remote = positional[0]
	}
	if len(positional) >= 2 {
		refspecs = positional[1:]
	}

	// Resolve the remote URL
	ctx := context.Background()
	remoteURL, err := resolveRemoteURL(ctx, remote)
	if err != nil {
		pushDie(flags, 1, fmt.Sprintf("resolving remote URL: %v", err))
		return 1
	}

	// Resolve refs to push
	refs, err := resolveRefsForPush(ctx, remote, refspecs)
	if err != nil {
		pushDie(flags, 1, fmt.Sprintf("resolving refs: %v", err))
		return 1
	}

	if len(refs) == 0 {
		pushDie(flags, 1, "nothing to push (no matching refs)")
		return 1
	}

	// Build hook stdin (same format as git pre-push)
	var stdinLines []string
	for _, r := range refs {
		stdinLines = append(stdinLines, fmt.Sprintf("%s %s %s %s", r.LocalRef, r.LocalSHA, r.RemoteRef, r.RemoteSHA))
	}
	hookStdin := []byte(strings.Join(stdinLines, "\n") + "\n")

	// Run pre-pre-push hooks (unless disabled)
	var hookResults []hooks.HookResult
	if !noPrePrePush {
		timeoutSec := cfg.Hooks.PrePrePush.TimeoutSeconds
		if timeoutSec <= 0 {
			timeoutSec = 1800
		}

		hookEnv := []string{
			"SAFEGIT_REMOTE_NAME=" + remote,
			"SAFEGIT_REMOTE_URL=" + remoteURL,
			"SAFEGIT_PHASE=pre-pre-push",
			fmt.Sprintf("SAFEGIT_HOOK_TIMEOUT_S=%d", timeoutSec),
		}

		hookResults, err = hooks.Run(ctx, gitDir, hookStdin, timeoutSec, hookEnv)
		if err != nil {
			pushDie(flags, 1, fmt.Sprintf("running hooks: %v", err))
			return 1
		}

		// Check hook results
		for _, hr := range hookResults {
			if hr.TimedOut {
				if flags.format == formatJSON {
					emitJSON("push", pushResult{Remote: remote, Refs: refs, HooksRun: hookResults}, nil, nil)
				} else {
					fmt.Fprintf(os.Stderr, "hook %s timed out after %v\n", hr.Name, hr.Duration)
				}
				return exitPushHookTimeout
			}
			if hr.ExitCode != 0 {
				if flags.format == formatJSON {
					emitJSON("push", pushResult{Remote: remote, Refs: refs, HooksRun: hookResults}, nil, nil)
				} else {
					fmt.Fprintf(os.Stderr, "hook %s failed (exit %d)\n", hr.Name, hr.ExitCode)
				}
				return exitPushHookFailed
			}
		}
	}

	// Execute git push with retries
	retryAttempts := cfg.Push.RetryAttempts
	if retryAttempts <= 0 {
		retryAttempts = 3
	}

	pushArgs := buildGitPushArgs(remote, refspecs, forceFlag)
	var pushErr error
	for attempt := 1; attempt <= retryAttempts; attempt++ {
		pushErr = execGitPush(ctx, pushArgs)
		if pushErr == nil {
			break
		}
		// Only retry on transport errors (not on rejected pushes like non-fast-forward)
		if !isTransportError(pushErr) {
			break
		}
		if attempt < retryAttempts {
			// Exponential backoff: 1s, 4s, 16s
			backoff := time.Duration(1<<(2*(attempt-1))) * time.Second
			if !flags.quiet {
				fmt.Fprintf(os.Stderr, "transport error, retrying in %v (attempt %d/%d)...\n", backoff, attempt+1, retryAttempts)
			}
			time.Sleep(backoff)
		}
	}

	if pushErr != nil {
		if flags.format == formatJSON {
			emitJSON("push", nil, &jsonError{Code: exitPushGitFailed, Message: pushErr.Error()}, nil)
		} else {
			fmt.Fprintf(os.Stderr, "push failed: %v\n", pushErr)
		}
		return exitPushGitFailed
	}

	// Log to oplog
	sgDir := repo.SafegitDir(gitDir)
	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "push",
		Extra: map[string]interface{}{
			"remote":    remote,
			"refCount":  len(refs),
			"hooksRun":  len(hookResults),
		},
	})

	// Output result
	result := pushResult{Remote: remote, Refs: refs, HooksRun: hookResults}
	if flags.format == formatJSON {
		emitJSON("push", result, nil, nil)
	} else if !flags.quiet {
		for _, r := range refs {
			fmt.Printf("  %s -> %s\n", shortRef(r.LocalRef), shortRef(r.RemoteRef))
		}
		if len(hookResults) > 0 {
			fmt.Printf("(%d pre-pre-push hook(s) passed)\n", len(hookResults))
		}
	}
	return 0
}

// resolveRemoteURL gets the URL for a named remote.
func resolveRemoteURL(ctx context.Context, remote string) (string, error) {
	stdout, _, err := git.Run(ctx, "remote", "get-url", remote)
	if err != nil {
		return "", fmt.Errorf("remote %q not found", remote)
	}
	return strings.TrimSpace(stdout), nil
}

// resolveRefsForPush determines what refs will be pushed.
// If refspecs are provided, resolves them. Otherwise, uses current branch's tracking.
func resolveRefsForPush(ctx context.Context, remote string, refspecs []string) ([]pushRefInfo, error) {
	if len(refspecs) > 0 {
		var refs []pushRefInfo
		for _, spec := range refspecs {
			ref, err := resolveRefspec(ctx, remote, spec)
			if err != nil {
				return nil, err
			}
			refs = append(refs, ref)
		}
		return refs, nil
	}

	// No refspecs -- push current branch
	headRef, err := git.HeadRef()
	if err != nil {
		return nil, fmt.Errorf("cannot determine current branch (detached HEAD?)")
	}

	localSHA, err := git.RevParse(headRef)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", headRef, err)
	}

	// Get remote SHA (might not exist yet)
	remoteSHA := "0000000000000000000000000000000000000000"
	remoteRef := headRef // push to same-named remote branch
	stdout, _, err := git.Run(ctx, "ls-remote", remote, remoteRef)
	if err == nil && stdout != "" {
		parts := strings.Fields(stdout)
		if len(parts) >= 1 {
			remoteSHA = parts[0]
		}
	}

	return []pushRefInfo{{
		LocalRef:  headRef,
		LocalSHA:  localSHA,
		RemoteRef: remoteRef,
		RemoteSHA: remoteSHA,
	}}, nil
}

// resolveRefspec resolves a single refspec like "main" or "main:main" or "HEAD:refs/heads/feature".
func resolveRefspec(ctx context.Context, remote, spec string) (pushRefInfo, error) {
	localPart := spec
	remotePart := spec

	if idx := strings.Index(spec, ":"); idx > 0 {
		localPart = spec[:idx]
		remotePart = spec[idx+1:]
	}

	// Resolve local ref to full form and SHA
	localRef := localPart
	if !strings.HasPrefix(localRef, "refs/") {
		// Try refs/heads/ first
		if sha, err := git.RevParse("refs/heads/" + localRef); err == nil {
			return pushRefInfo{
				LocalRef:  "refs/heads/" + localRef,
				LocalSHA:  sha,
				RemoteRef: ensureFullRef(remotePart),
				RemoteSHA: getRemoteSHA(ctx, remote, ensureFullRef(remotePart)),
			}, nil
		}
		// Fall back to raw rev-parse
		sha, err := git.RevParse(localRef)
		if err != nil {
			return pushRefInfo{}, fmt.Errorf("cannot resolve local ref %q", localPart)
		}
		localRef = "refs/heads/" + localRef
		return pushRefInfo{
			LocalRef:  localRef,
			LocalSHA:  sha,
			RemoteRef: ensureFullRef(remotePart),
			RemoteSHA: getRemoteSHA(ctx, remote, ensureFullRef(remotePart)),
		}, nil
	}

	sha, err := git.RevParse(localRef)
	if err != nil {
		return pushRefInfo{}, fmt.Errorf("cannot resolve local ref %q", localPart)
	}

	return pushRefInfo{
		LocalRef:  localRef,
		LocalSHA:  sha,
		RemoteRef: ensureFullRef(remotePart),
		RemoteSHA: getRemoteSHA(ctx, remote, ensureFullRef(remotePart)),
	}, nil
}

func ensureFullRef(ref string) string {
	if strings.HasPrefix(ref, "refs/") {
		return ref
	}
	return "refs/heads/" + ref
}

func getRemoteSHA(ctx context.Context, remote, ref string) string {
	nullSHA := "0000000000000000000000000000000000000000"
	stdout, _, err := git.Run(ctx, "ls-remote", remote, ref)
	if err != nil || stdout == "" {
		return nullSHA
	}
	parts := strings.Fields(stdout)
	if len(parts) >= 1 {
		return parts[0]
	}
	return nullSHA
}

func buildGitPushArgs(remote string, refspecs []string, force bool) []string {
	args := []string{"push"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, remote)
	args = append(args, refspecs...)
	return args
}

// execGitPush runs git push, streaming stdout/stderr to the user.
func execGitPush(ctx context.Context, args []string) error {
	stdout, stderr, err := git.Run(ctx, args...)
	if stdout != "" {
		fmt.Print(stdout)
	}
	if stderr != "" {
		// git push writes progress to stderr even on success
		fmt.Fprint(os.Stderr, stderr)
	}
	return err
}

// isTransportError heuristically determines if a push error is a network/transport issue
// (worth retrying) vs. a logical rejection (non-fast-forward, permission denied).
func isTransportError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	transportPatterns := []string{
		"Could not resolve host",
		"Connection refused",
		"Connection reset",
		"Connection timed out",
		"Network is unreachable",
		"failed to connect",
		"SSL",
		"TLS",
		"EOF",
		"broken pipe",
		"transport",
	}
	for _, p := range transportPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

func shortRef(ref string) string {
	ref = strings.TrimPrefix(ref, "refs/heads/")
	ref = strings.TrimPrefix(ref, "refs/tags/")
	return ref
}

func pushDie(flags globalFlags, code int, msg string) {
	if flags.format == formatJSON {
		emitJSON("push", nil, &jsonError{Code: code, Message: msg}, nil)
	} else {
		fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	}
}

// MarshalJSON for HookResult to format Duration as a string.
func (r pushResult) MarshalJSON() ([]byte, error) {
	type hookResultJSON struct {
		Name     string `json:"name"`
		ExitCode int    `json:"exitCode"`
		Duration string `json:"duration"`
		TimedOut bool   `json:"timedOut,omitempty"`
	}

	hooksJSON := make([]hookResultJSON, len(r.HooksRun))
	for i, hr := range r.HooksRun {
		hooksJSON[i] = hookResultJSON{
			Name:     hr.Name,
			ExitCode: hr.ExitCode,
			Duration: hr.Duration.String(),
			TimedOut: hr.TimedOut,
		}
	}

	type alias struct {
		Remote   string           `json:"remote"`
		Refs     []pushRefInfo    `json:"refs"`
		HooksRun []hookResultJSON `json:"hooksRun,omitempty"`
	}

	return json.Marshal(alias{
		Remote:   r.Remote,
		Refs:     r.Refs,
		HooksRun: hooksJSON,
	})
}
