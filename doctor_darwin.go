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
	raw := buf.Fstypename[:]
	b := make([]byte, 0, len(raw))
	for _, c := range raw {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	fsType := string(b)
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
