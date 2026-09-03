//go:build linux

package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
)

const v9fsSuperMagic = 0x01021997

func validatePlatformRootInput(root string) error {
	if isWSLWindowsDriveMount(filepath.Clean(root)) {
		return fmt.Errorf("Linux MCP root must not be a Windows drive mount under /mnt/<drive>: %s", root)
	}
	return nil
}

func validatePlatformRoot(root string) error {
	if err := validatePlatformRootInput(root); err != nil {
		return err
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(root, &stat); err != nil {
		return fmt.Errorf("inspect root filesystem: %w", err)
	}
	if uint64(stat.Type) == v9fsSuperMagic {
		return fmt.Errorf("Linux MCP root must be on a Linux filesystem, not 9p/DrvFs: %s", root)
	}
	return nil
}

func isWSLWindowsDriveMount(path string) bool {
	clean := filepath.Clean(path)
	parts := strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	if len(parts) < 2 || parts[0] != "mnt" || len(parts[1]) != 1 {
		return false
	}
	drive := parts[1][0]
	return (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')
}
