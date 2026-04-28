package main

import (
	"os"
	"os/exec"
)

// runFetch is a simple passthrough to git fetch -- no hooks, no coordination.
func runFetch(args []string) int {
	cmd := exec.Command("git", append([]string{"fetch"}, args...)...)
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
