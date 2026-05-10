//go:build linux

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
	const (
		nfsMagic  int64 = 0x6969
		cifsMagic int64 = 0xFF534D42
		smbMagic  int64 = 0x517B
		fuseMagic int64 = 0x65735546
	)
	fsType := int64(buf.Type)
	var fsName string
	switch fsType {
	case nfsMagic:
		fsName = "NFS"
	case cifsMagic:
		fsName = "CIFS/SMB"
	case smbMagic:
		fsName = "SMB"
	case fuseMagic:
		fsName = "FUSE (possibly SSHFS)"
	}
	if fsName != "" {
		return checkResult{
			Name:   "filesystem",
			Status: "warn",
			Detail: fmt.Sprintf("network filesystem detected: %s (lock atomicity not guaranteed)", fsName),
		}
	}
	return checkResult{Name: "filesystem", Status: "ok"}
}
