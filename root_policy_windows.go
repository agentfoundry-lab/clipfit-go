//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	driveRemovable = 2
	driveFixed     = 3
	driveRemote    = 4
	driveRAMDisk   = 6
)

var getDriveTypeW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDriveTypeW")

func validatePlatformRootInput(root string) error {
	return validateWindowsLocalDriveRoot(root)
}

func validatePlatformRoot(root string) error {
	return validateWindowsLocalDriveRoot(root)
}

func validateWindowsLocalDriveRoot(root string) error {
	volume := filepath.VolumeName(root)
	if len(volume) != 2 || volume[1] != ':' || !isWindowsDriveLetter(volume[0]) {
		return fmt.Errorf("Windows MCP root must use a local drive-letter path, not a UNC or device path: %s", root)
	}

	driveRoot := volume + "\\"
	driveRootUTF16, err := syscall.UTF16PtrFromString(driveRoot)
	if err != nil {
		return fmt.Errorf("inspect Windows root drive: %w", err)
	}
	driveType, _, _ := getDriveTypeW.Call(uintptr(unsafe.Pointer(driveRootUTF16)))
	switch uint32(driveType) {
	case driveRemovable, driveFixed, driveRAMDisk:
		return nil
	case driveRemote:
		return fmt.Errorf("Windows MCP root must be on a local drive, not a mapped network or WSL drive: %s", root)
	default:
		return fmt.Errorf("Windows MCP root drive type %d is not supported: %s", driveType, root)
	}
}

func isWindowsDriveLetter(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}
