package main

import (
	"context"

	"github.com/smm-h/safegit/internal/git"
)

// runFetch is a simple passthrough to git fetch -- no hooks, no coordination.
func runFetch(args []string) int {
	ctx := context.Background()
	if err := git.RunPassthrough(ctx, append([]string{"fetch"}, args...)...); err != nil {
		// Extract exit code via interface to avoid importing os/exec
		if exitErr, ok := err.(interface{ ExitCode() int }); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}
