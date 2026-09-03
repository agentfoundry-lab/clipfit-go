package main

import (
	"runtime"
	"strings"
	"testing"
)

func platformText(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	if platformLineEnding != "\n" {
		content = strings.ReplaceAll(content, "\n", platformLineEnding)
	}
	return content
}

func TestPlatformLineEndingContract(t *testing.T) {
	want := "\n"
	if runtime.GOOS == "windows" {
		want = "\r\n"
	}
	if platformLineEnding != want {
		t.Fatalf("platform line ending = %q, want %q for %s", platformLineEnding, want, runtime.GOOS)
	}
}

func TestRenderOutputUsesPlatformLineEndings(t *testing.T) {
	input := "alpha\r\nbeta\rgamma\n"
	want := platformText("alpha\nbeta\ngamma\n")
	if got := string(renderOutput(input, fileMeta{})); got != want {
		t.Fatalf("rendered output = %q, want %s output %q", got, platformLineEndingName, want)
	}
}
