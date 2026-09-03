//go:build !linux && !windows

package main

func validatePlatformRootInput(string) error {
	return nil
}

func validatePlatformRoot(string) error {
	return nil
}
