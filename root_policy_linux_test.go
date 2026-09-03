//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsWSLWindowsDriveMount(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/mnt/c", want: true},
		{path: "/mnt/C/project", want: true},
		{path: "/mnt/z", want: true},
		{path: "/mnt", want: false},
		{path: "/mnt/cache", want: false},
		{path: "/mnt/cc/project", want: false},
		{path: "/home/user/project", want: false},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := isWSLWindowsDriveMount(test.path); got != test.want {
				t.Fatalf("isWSLWindowsDriveMount(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}

func TestValidatePlatformRootAcceptsLinuxFilesystem(t *testing.T) {
	if err := validatePlatformRoot(t.TempDir()); err != nil {
		t.Fatalf("validatePlatformRoot() error = %v", err)
	}
}

func TestResolveMCPRootRejectsWSLWindowsFilesystem(t *testing.T) {
	const windowsRoot = "/mnt/c"
	if _, err := os.Stat(windowsRoot); err != nil {
		t.Skipf("%s is unavailable: %v", windowsRoot, err)
	}

	if _, err := resolveMCPRoot(windowsRoot); err == nil || !strings.Contains(err.Error(), "Windows drive mount") {
		t.Fatalf("resolveMCPRoot(%q) error = %v, want Windows drive mount rejection", windowsRoot, err)
	}

	link := filepath.Join(t.TempDir(), "windows-root")
	if err := os.Symlink(windowsRoot, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := resolveMCPRoot(link); err == nil || !strings.Contains(err.Error(), "Windows drive mount") {
		t.Fatalf("resolveMCPRoot(symlink) error = %v, want Windows drive mount rejection", err)
	}
}
