package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMCPCreateApplyAndRollback(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := resolveMCPRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	server := &mcpServer{root: resolvedRoot}
	createParents := true

	created, err := server.create(mcpCreateArgs{
		Path:          "nested/example.txt",
		Content:       "hello world\n",
		CreateParents: &createParents,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !created.OK || created.Action != "create" {
		t.Fatalf("unexpected create result: %+v", created)
	}
	target := filepath.Join(root, "nested", "example.txt")
	assertFileContent(t, target, "hello world\n")

	if _, err := server.create(mcpCreateArgs{
		Path:    "nested/example.txt",
		Content: "must not overwrite\n",
	}); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("expected overwrite refusal, got %v", err)
	}
	assertFileContent(t, target, "hello world\n")

	preview, err := server.previewEdits(mcpPreviewArgs{
		Path: target,
		Operations: []EditOperation{{
			Type:    "replace_block",
			Find:    "hello world",
			Replace: "hello MCP",
		}},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !preview.OK || preview.ChangeCount != 1 || len(preview.Operations) != 1 || preview.Operations[0].AppliedMatches != 1 {
		t.Fatalf("unexpected preview result: %+v", preview)
	}
	assertFileContent(t, target, "hello world\n")

	applied, err := server.commitPreview(mcpCommitArgs{PreviewID: preview.PreviewID})
	if err != nil {
		t.Fatalf("apply preview: %v", err)
	}
	if !applied.OK || applied.ChangeCount != 1 {
		t.Fatalf("unexpected apply result: %+v", applied)
	}
	t.Cleanup(func() { _ = os.Remove(applied.BackupPath) })
	assertFileContent(t, target, "hello MCP\n")

	rolledBack, err := server.rollback(target)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !rolledBack.OK {
		t.Fatalf("unexpected rollback result: %+v", rolledBack)
	}
	assertFileContent(t, target, "hello world\n")
}

func TestSecureMCPTargetRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := resolveMCPRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secureMCPTarget(resolvedRoot, "../outside.txt", false); err == nil {
		t.Fatal("expected parent traversal to be rejected")
	}

	if runtime.GOOS == "windows" {
		t.Skip("symlink setup is not reliable without developer mode on Windows")
	}
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := secureMCPTarget(resolvedRoot, filepath.Join(link, "bad.txt"), false); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestServeMCPProtocolAndCreateTool(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := resolveMCPRoot(root)
	if err != nil {
		t.Fatal(err)
	}

	requests := []map[string]any{
		{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "initialize",
			"params": map[string]any{
				"protocolVersion": "2025-06-18",
				"clientInfo":      map[string]any{"name": "test", "version": "1"},
				"capabilities":    map[string]any{},
			},
		},
		{
			"jsonrpc": "2.0",
			"method":  "notifications/initialized",
		},
		{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "tools/list",
			"params":  map[string]any{},
		},
		{
			"jsonrpc": "2.0",
			"id":      3,
			"method":  "tools/call",
			"params": map[string]any{
				"name": "clipfit_create",
				"arguments": map[string]any{
					"path":    "created-by-mcp.txt",
					"content": "from protocol test\n",
				},
			},
		},
		{
			"jsonrpc": "2.0",
			"id":      4,
			"method":  "tools/call",
			"params": map[string]any{
				"name": "clipfit_edit",
				"arguments": map[string]any{
					"path": "created-by-mcp.txt",
					"operations": []map[string]any{{
						"type":    "replace",
						"find":    "from protocol test",
						"replace": "edited directly",
					}},
				},
			},
		},
	}

	var input bytes.Buffer
	encoder := json.NewEncoder(&input)
	for _, request := range requests {
		if err := encoder.Encode(request); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := serveMCP(&input, &output, resolvedRoot); err != nil {
		t.Fatalf("serve MCP: %v", err)
	}

	decoder := json.NewDecoder(&output)
	var responses []map[string]any
	for decoder.More() {
		var response map[string]any
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response)
	}
	if len(responses) != 4 {
		t.Fatalf("expected 4 responses, got %d: %s", len(responses), output.String())
	}

	initializeResult := responses[0]["result"].(map[string]any)
	if initializeResult["protocolVersion"] != "2025-06-18" {
		t.Fatalf("unexpected initialize result: %#v", initializeResult)
	}

	listResult := responses[1]["result"].(map[string]any)
	tools := listResult["tools"].([]any)
	if len(tools) != 5 {
		t.Fatalf("expected 5 tools, got %d", len(tools))
	}
	hasDirectEdit := false
	for _, rawTool := range tools {
		tool := rawTool.(map[string]any)
		if tool["name"] == "clipfit_edit" {
			hasDirectEdit = true
		}
	}
	if !hasDirectEdit {
		t.Fatal("tools/list is missing clipfit_edit")
	}

	callResult := responses[2]["result"].(map[string]any)
	if isError, _ := callResult["isError"].(bool); isError {
		t.Fatalf("create tool returned error: %#v", callResult)
	}
	structured := callResult["structuredContent"].(map[string]any)
	if ok, _ := structured["ok"].(bool); !ok {
		t.Fatalf("unexpected structured result: %#v", structured)
	}
	directCallResult := responses[3]["result"].(map[string]any)
	if isError, _ := directCallResult["isError"].(bool); isError {
		t.Fatalf("direct edit tool returned error: %#v", directCallResult)
	}
	directStructured := directCallResult["structuredContent"].(map[string]any)
	if directStructured["action"] != "edit" || directStructured["preview_id"] != nil {
		t.Fatalf("unexpected direct edit receipt: %#v", directStructured)
	}
	if _, exists := directStructured["hunks"]; exists {
		t.Fatalf("direct edit receipt must be compact: %#v", directStructured)
	}
	if backup, _ := directStructured["backup_path"].(string); backup != "" {
		t.Cleanup(func() { _ = os.Remove(backup) })
	}
	assertFileContent(t, filepath.Join(root, "created-by-mcp.txt"), "edited directly\n")
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != want {
		t.Fatalf("file content = %q, want %q", got, want)
	}
}
