package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnchoredPreviewSelectsTargetAfterUniqueAnchor(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "anchored.go")
	original := "func first() {\n\tvalue := 1\n}\n\nfunc second() {\n\tvalue := 1\n\tvalue := 1\n}\n"
	if err := os.WriteFile(target, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	server := &mcpServer{root: root}

	preview, err := server.previewEdits(mcpPreviewArgs{
		Path: target,
		Operations: []EditOperation{{
			Type:    "replace_block",
			Anchor:  "func second() {",
			Find:    "value := 1",
			Replace: "value := 2",
		}},
	})
	if err != nil {
		t.Fatalf("anchored preview: %v", err)
	}
	if got := preview.Operations[0]; got.AnchorMatches != 1 || got.CandidateMatches != 2 || got.AppliedMatches != 1 {
		t.Fatalf("unexpected operation stats: %+v", got)
	}
	assertFileContent(t, target, original)

	applied, err := server.commitPreview(mcpCommitArgs{PreviewID: preview.PreviewID})
	if err != nil {
		t.Fatalf("commit anchored preview: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(applied.BackupPath) })
	want := "func first() {\n\tvalue := 1\n}\n\nfunc second() {\n\tvalue := 2\n\tvalue := 1\n}\n"
	assertFileContent(t, target, want)
}

func TestPreviewFailsClosedOnAmbiguousFind(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "ambiguous.txt")
	original := "same\nsame\n"
	if err := os.WriteFile(target, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	server := &mcpServer{root: root}

	_, err := server.previewEdits(mcpPreviewArgs{
		Path: target,
		Operations: []EditOperation{{
			Type:    "replace_block",
			Find:    "same",
			Replace: "changed",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "matched 2 locations") {
		t.Fatalf("expected ambiguous find error, got %v", err)
	}
	assertFileContent(t, target, original)
	if len(server.previews) != 0 {
		t.Fatal("ambiguous preview must not issue a token")
	}
}

func TestStructuredReplaceRejectsMultilineSides(t *testing.T) {
	tests := []struct {
		name        string
		find        string
		replacement string
	}{
		{name: "find", find: "old\nnext", replacement: "updated"},
		{name: "replacement", find: "old", replacement: "updated\nextra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "multiline.txt")
			original := "old\nnext\n"
			if err := os.WriteFile(target, []byte(original), 0644); err != nil {
				t.Fatal(err)
			}
			server := &mcpServer{root: root}
			operation := EditOperation{
				Type:    "replace",
				Find:    tt.find,
				Replace: tt.replacement,
			}

			if _, err := server.previewEdits(mcpPreviewArgs{Path: target, Operations: []EditOperation{operation}}); err == nil || !strings.Contains(err.Error(), "single-line find and replacement only") {
				t.Fatalf("expected preview to reject multiline replace, got %v", err)
			}
			if _, err := server.editDirect(mcpPreviewArgs{Path: target, Operations: []EditOperation{operation}}); err == nil || !strings.Contains(err.Error(), "single-line find and replacement only") {
				t.Fatalf("expected direct edit to reject multiline replace, got %v", err)
			}
			assertFileContent(t, target, original)
			if len(server.previews) != 0 {
				t.Fatal("rejected multiline replace must not issue a preview token")
			}
		})
	}
}

func TestLegacyReplaceRejectsMultilineSidesBeforeWrite(t *testing.T) {
	tests := []struct {
		name string
		spec string
	}{
		{
			name: "find",
			spec: "===REPLACE===\nold\nnext\n===WITH===\nupdated\n===END_REP===\n",
		},
		{
			name: "replacement",
			spec: "===REPLACE===\nold\n===WITH===\nupdated\nextra\n===END_REP===\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "legacy.txt")
			specPath := filepath.Join(root, "edit.clipfit")
			original := "old\nnext\n"
			if err := os.WriteFile(target, []byte(original), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(specPath, []byte(tt.spec), 0644); err != nil {
				t.Fatal(err)
			}

			if code := runApply(target, specPath, false, false); code != 2 {
				t.Fatalf("runApply exit code = %d, want 2", code)
			}
			assertFileContent(t, target, original)
			if _, err := os.Stat(backupPath(target)); !os.IsNotExist(err) {
				t.Fatalf("invalid command must not create a backup, stat error = %v", err)
			}
		})
	}
}

func TestPreviewFailsClosedOnAmbiguousAnchor(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "ambiguous-anchor.txt")
	original := "section\nvalue=old\nsection\nvalue=old\n"
	if err := os.WriteFile(target, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	server := &mcpServer{root: root}

	_, err := server.previewEdits(mcpPreviewArgs{
		Path: target,
		Operations: []EditOperation{{
			Type:    "replace_block",
			Anchor:  "section",
			Find:    "value=old",
			Replace: "value=new",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "anchor matched 2 locations") {
		t.Fatalf("expected ambiguous anchor error, got %v", err)
	}
	assertFileContent(t, target, original)
}

func TestApplyRejectsFileChangedAfterPreview(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "stale.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	server := &mcpServer{root: root}

	preview, err := server.previewEdits(mcpPreviewArgs{
		Path: target,
		Operations: []EditOperation{{
			Type:    "replace",
			Find:    "old",
			Replace: "previewed",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("external change\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err = server.commitPreview(mcpCommitArgs{PreviewID: preview.PreviewID})
	if err == nil || !strings.Contains(err.Error(), "changed since preview") {
		t.Fatalf("expected stale preview rejection, got %v", err)
	}
	assertFileContent(t, target, "external change\n")
	if _, ok := server.previews[preview.PreviewID]; ok {
		t.Fatal("stale preview token should be invalidated")
	}
}

func TestDirectEditAppliesWithoutPreview(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "direct.txt")
	if err := os.WriteFile(target, []byte("alpha\nbeta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	server := &mcpServer{root: root}

	receipt, err := server.editDirect(mcpPreviewArgs{
		Path: target,
		Operations: []EditOperation{{
			Type:    "replace_block",
			Find:    "alpha",
			Replace: "gamma",
		}},
	})
	if err != nil {
		t.Fatalf("direct edit: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(receipt.BackupPath) })
	if !receipt.OK || receipt.Action != "edit" || receipt.PreviewID != "" {
		t.Fatalf("unexpected direct receipt: %+v", receipt)
	}
	if receipt.OperationCount != 1 || receipt.ChangeCount != 1 ||
		receipt.BeforeSHA256 == "" || receipt.AfterSHA256 == "" ||
		receipt.BeforeSHA256 == receipt.AfterSHA256 {
		t.Fatalf("incomplete direct receipt: %+v", receipt)
	}
	if len(server.previews) != 0 {
		t.Fatal("direct edit must not retain a preview plan")
	}
	assertFileContent(t, target, "gamma\nbeta\n")
}

func TestDirectEditFailsClosedOnAmbiguousFind(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "direct-ambiguous.txt")
	original := "same\nsame\n"
	if err := os.WriteFile(target, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	server := &mcpServer{root: root}

	_, err := server.editDirect(mcpPreviewArgs{
		Path: target,
		Operations: []EditOperation{{
			Type:    "replace_block",
			Find:    "same",
			Replace: "changed",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "matched 2 locations") {
		t.Fatalf("expected ambiguous direct edit error, got %v", err)
	}
	assertFileContent(t, target, original)
}

func TestLegacyProtocolParsesAnchorAndTarget(t *testing.T) {
	spec := strings.Join([]string{
		"===REPLACE_BLOCK===",
		"===ANCHOR===",
		"func second() {",
		"===TARGET===",
		"value := 1",
		"===WITH===",
		"value := 2",
		"===END_REP===",
	}, "\n")
	commands := parseKissCommands(spec)
	if len(commands) != 1 {
		t.Fatalf("expected one command, got %d", len(commands))
	}
	if commands[0].AnchorStr != "func second() {" || commands[0].FindStr != "value := 1" {
		t.Fatalf("anchor/target parse mismatch: %+v", commands[0])
	}

	original := "func first() {\n\tvalue := 1\n}\n\nfunc second() {\n\tvalue := 1\n}\n"
	updated, stats := applyCommands(original, commands)
	if len(stats) != 1 || stats[0].Matches != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	want := "func first() {\n\tvalue := 1\n}\n\nfunc second() {\n\tvalue := 2\n}\n"
	if updated != want {
		t.Fatalf("updated content = %q, want %q", updated, want)
	}
}
