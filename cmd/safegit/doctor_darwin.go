//go:build darwin

package main

import (
	"fmt"
	"syscall"
)

func checkFilesystem(gitDir string) checkResult {
	var buf syscall.Statfs_t
	if err := syscall.Statfs(gitDir, &buf); err != nil {
		return checkResult{Name: "filesystem", Status: "warn", Detail: fmt.Sprintf("statfs failed: %v", err)}
	}
	fsType := string(buf.Fstypename[:])
	// Trim null bytes from the fixed-size array
	for i, b := range fsType {
		if b == 0 {
			fsType = fsType[:i]
			break
		}
	}
	switch fsType {
	case "nfs":
		return checkResult{Name: "filesystem", Status: "warn", Detail: "network filesystem detected: NFS (lock atomicity not guaranteed)"}
	case "smbfs":
		return checkResult{Name: "filesystem", Status: "warn", Detail: "network filesystem detected: SMB (lock atomicity not guaranteed)"}
	case "webdav":
		return checkResult{Name: "filesystem", Status: "warn", Detail: "network filesystem detected: WebDAV (lock atomicity not guaranteed)"}
	case "osxfuse", "macfuse", "fuse":
		return checkResult{Name: "filesystem", Status: "warn", Detail: "network filesystem detected: FUSE (lock atomicity not guaranteed)"}
	}
	return checkResult{Name: "filesystem", Status: "ok"}
}
