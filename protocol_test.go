package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestMCPPreviewApplyProtocolRoundTrip(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "roundtrip.txt")
	if err := os.WriteFile(target, []byte("anchor\nvalue=old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := resolveMCPRoot(root)
	if err != nil {
		t.Fatal(err)
	}

	serverInput, clientWriter := io.Pipe()
	clientReader, serverOutput := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := serveMCP(serverInput, serverOutput, resolvedRoot)
		_ = serverOutput.CloseWithError(err)
		done <- err
	}()

	encoder := json.NewEncoder(clientWriter)
	decoder := json.NewDecoder(clientReader)
	call := func(request map[string]any) map[string]any {
		t.Helper()
		if err := encoder.Encode(request); err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	call(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"clientInfo":      map[string]any{"name": "roundtrip-test", "version": "1"},
			"capabilities":    map[string]any{},
		},
	})

	previewResponse := call(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "clipfit_preview",
			"arguments": map[string]any{
				"path": target,
				"operations": []map[string]any{{
					"type":    "replace_block",
					"anchor":  "anchor",
					"find":    "value=old",
					"replace": "value=new",
				}},
			},
		},
	})
	previewToolResult := previewResponse["result"].(map[string]any)
	if previewToolResult["isError"] == true {
		t.Fatalf("preview tool error: %#v", previewToolResult)
	}
	previewStructured := previewToolResult["structuredContent"].(map[string]any)
	previewID, _ := previewStructured["preview_id"].(string)
	if previewID == "" {
		t.Fatalf("preview_id missing: %#v", previewStructured)
	}
	assertFileContent(t, target, "anchor\nvalue=old\n")

	applyResponse := call(map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "clipfit_apply",
			"arguments": map[string]any{"preview_id": previewID},
		},
	})
	applyToolResult := applyResponse["result"].(map[string]any)
	if applyToolResult["isError"] == true {
		t.Fatalf("apply tool error: %#v", applyToolResult)
	}
	applyStructured := applyToolResult["structuredContent"].(map[string]any)
	if ok, _ := applyStructured["ok"].(bool); !ok {
		t.Fatalf("apply failed: %#v", applyStructured)
	}
	if _, exists := applyStructured["hunks"]; exists {
		t.Fatalf("apply receipt must not repeat preview hunks: %#v", applyStructured)
	}
	if _, exists := applyStructured["operations"]; exists {
		t.Fatalf("apply receipt must not repeat operation stats: %#v", applyStructured)
	}
	if got := int(applyStructured["operation_count"].(float64)); got != 1 {
		t.Fatalf("apply receipt operation_count = %d, want 1", got)
	}
	if applyStructured["before_sha256"] == "" || applyStructured["after_sha256"] == "" {
		t.Fatalf("apply receipt is missing hashes: %#v", applyStructured)
	}
	if backup, _ := applyStructured["backup_path"].(string); backup != "" {
		t.Cleanup(func() { _ = os.Remove(backup) })
	}
	assertFileContent(t, target, "anchor\nvalue=new\n")

	_ = clientWriter.Close()
	if err := <-done; err != nil {
		t.Fatalf("MCP server shutdown: %v", err)
	}
}
