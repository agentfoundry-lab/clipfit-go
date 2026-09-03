//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestValidatePlatformRootAcceptsLocalWindowsDirectory(t *testing.T) {
	if err := validatePlatformRoot(t.TempDir()); err != nil {
		t.Fatalf("validatePlatformRoot() error = %v", err)
	}
}

func TestResolveMCPRootRejectsUNCPathsBeforeAccess(t *testing.T) {
	roots := []string{
		"\\\\wsl$\\Ubuntu-24.04\\home\\user",
		"\\\\wsl.localhost\\Ubuntu-24.04\\home\\user",
		"\\\\server\\share\\project",
	}
	for _, root := range roots {
		t.Run(root, func(t *testing.T) {
			_, err := resolveMCPRoot(root)
			if err == nil || !strings.Contains(err.Error(), "drive-letter") {
				t.Fatalf("resolveMCPRoot(%q) error = %v, want pre-access UNC rejection", root, err)
			}
		})
	}
}
