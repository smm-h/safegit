//go:build windows

package main

func checkFilesystem(gitDir string) checkResult {
	return checkResult{Name: "filesystem", Status: "ok", Detail: "filesystem check not available on Windows"}
}
