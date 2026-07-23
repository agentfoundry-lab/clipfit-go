package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestComputeChangesLocalizesDistantEditsAfterLineInsertion(t *testing.T) {
	beforeLines := numberedLines(400)
	afterLines := append([]string(nil), beforeLines...)
	afterLines[8] = "line-009-updated\nline-009-extra"
	afterLines[377] = "line-378-updated"

	hunks := computeChanges(
		strings.Join(beforeLines, "\n")+"\n",
		strings.Join(afterLines, "\n")+"\n",
	)
	if len(hunks) != 2 {
		t.Fatalf("expected 2 localized hunks, got %d: %#v", len(hunks), hunks)
	}
	if hunks[0].OldStart != 9 || len(hunks[0].Removed) != 1 || len(hunks[0].Added) != 2 {
		t.Fatalf("unexpected first hunk: %#v", hunks[0])
	}
	if hunks[1].OldStart != 378 || len(hunks[1].Removed) != 1 || len(hunks[1].Added) != 1 {
		t.Fatalf("unexpected distant hunk: %#v", hunks[1])
	}
}

func TestMCPPreviewDistantEditsCompletesWhileClientDrains(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "distant.txt")
	beforeLines := numberedLines(400)
	largeTopMarker := "top-marker-" + strings.Repeat("a", 40<<10)
	largeUpdatedMarker := "top-marker-updated-" + strings.Repeat("b", 40<<10)
	beforeLines[8] = largeTopMarker
	beforeLines[99] = "field-a"
	beforeLines[199] = "field-b"
	beforeLines[299] = "field-c"
	beforeLines[377] = "far-marker"
	before := strings.Join(beforeLines, "\n") + "\n"
	if err := os.WriteFile(target, []byte(before), 0644); err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := resolveMCPRoot(root)
	if err != nil {
		t.Fatal(err)
	}

	serverInput, clientWriter := io.Pipe()
	clientReader, serverOutput := io.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		err := serveMCPWithOptions(
			serverInput,
			serverOutput,
			resolvedRoot,
			noopMCPTelemetry{},
			2*time.Second,
		)
		_ = serverOutput.CloseWithError(err)
		serverDone <- err
	}()

	encoder := json.NewEncoder(clientWriter)
	decoder := json.NewDecoder(clientReader)
	call := func(request map[string]any) map[string]any {
		t.Helper()
		type decodedResponse struct {
			value map[string]any
			err   error
		}
		responseReady := make(chan decodedResponse, 1)
		go func() {
			var response map[string]any
			err := decoder.Decode(&response)
			responseReady <- decodedResponse{value: response, err: err}
		}()
		if err := encoder.Encode(request); err != nil {
			t.Fatal(err)
		}
		select {
		case decoded := <-responseReady:
			if decoded.err != nil {
				t.Fatal(decoded.err)
			}
			return decoded.value
		case <-time.After(2 * time.Second):
			t.Fatal("timed out while client was draining the MCP response")
			return nil
		}
	}

	previewResponse := call(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "clipfit_preview",
			"arguments": map[string]any{
				"path": target,
				"operations": []map[string]any{
					{"type": "replace_block", "find": largeTopMarker, "replace": largeUpdatedMarker + "\ninserted-line"},
					{"type": "replace", "find": "field-a", "replace": "field-a-updated"},
					{"type": "replace", "find": "field-b", "replace": "field-b-updated"},
					{"type": "replace", "find": "field-c", "replace": "field-c-updated"},
					{"type": "replace", "find": "far-marker", "replace": "far-marker-updated"},
				},
			},
		},
	})
	if previewResponse["error"] != nil {
		t.Fatalf("preview RPC error: %#v", previewResponse["error"])
	}
	previewToolResult := previewResponse["result"].(map[string]any)
	if isError, _ := previewToolResult["isError"].(bool); isError {
		t.Fatalf("preview tool error: %#v", previewToolResult)
	}
	previewStructured := previewToolResult["structuredContent"].(map[string]any)
	if got := int(previewStructured["change_count"].(float64)); got != 5 {
		t.Fatalf("expected 5 localized hunks, got %d", got)
	}
	hunks := previewStructured["hunks"].([]any)
	if len(hunks) != 5 {
		t.Fatalf("expected 5 hunk objects, got %d", len(hunks))
	}
	for index, rawHunk := range hunks {
		hunk := rawHunk.(map[string]any)
		removed := hunk["removed"].([]any)
		added := hunk["added"].([]any)
		if len(removed)+len(added) > 3 {
			t.Fatalf("hunk %d unexpectedly contains remote unchanged lines: %#v", index, hunk)
		}
	}
	operations := previewStructured["operations"].([]any)
	if len(operations) != 5 {
		t.Fatalf("expected 5 operation stats, got %d", len(operations))
	}
	for index, rawOperation := range operations {
		operation := rawOperation.(map[string]any)
		if operation["candidate_matches"].(float64) != 1 ||
			operation["applied_matches"].(float64) != 1 ||
			operation["expected_matches"].(float64) != 1 {
			t.Fatalf("operation %d was not uniquely applied: %#v", index, operation)
		}
	}
	content := previewToolResult["content"].([]any)
	summary := content[0].(map[string]any)["text"].(string)
	if len(summary) > 1024 {
		t.Fatalf("content text should be a compact summary, got %d bytes", len(summary))
	}
	encodedPreview, err := json.Marshal(previewResponse)
	if err != nil {
		t.Fatal(err)
	}
	if len(encodedPreview) <= 64<<10 {
		t.Fatalf("regression response must cross the common 64 KiB pipe capacity, got %d bytes", len(encodedPreview))
	}
	if len(encodedPreview)+1 > maxMCPResponseBytes {
		t.Fatalf("regression response exceeds the server safety budget: %d bytes", len(encodedPreview))
	}
	assertFileContent(t, target, before)

	previewID := previewStructured["preview_id"].(string)
	applyResponse := call(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "clipfit_apply",
			"arguments": map[string]any{"preview_id": previewID},
		},
	})
	applyToolResult := applyResponse["result"].(map[string]any)
	if isError, _ := applyToolResult["isError"].(bool); isError {
		t.Fatalf("apply tool error: %#v", applyToolResult)
	}
	applyStructured := applyToolResult["structuredContent"].(map[string]any)
	if _, exists := applyStructured["hunks"]; exists {
		t.Fatalf("apply receipt repeated preview hunks: %#v", applyStructured)
	}
	if _, exists := applyStructured["operations"]; exists {
		t.Fatalf("apply receipt repeated operation stats: %#v", applyStructured)
	}
	encodedApply, err := json.Marshal(applyResponse)
	if err != nil {
		t.Fatal(err)
	}
	if len(encodedApply) > 4<<10 {
		t.Fatalf("apply receipt is not compact: %d bytes", len(encodedApply))
	}
	if backup, _ := applyStructured["backup_path"].(string); backup != "" {
		t.Cleanup(func() { _ = os.Remove(backup) })
	}

	expected := before
	for _, replacement := range [][2]string{
		{largeTopMarker, largeUpdatedMarker + "\ninserted-line"},
		{"field-a", "field-a-updated"},
		{"field-b", "field-b-updated"},
		{"field-c", "field-c-updated"},
		{"far-marker", "far-marker-updated"},
	} {
		expected = strings.Replace(expected, replacement[0], replacement[1], 1)
	}
	assertFileContent(t, target, expected)

	_ = clientWriter.Close()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("MCP server shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MCP server did not stop after client EOF")
	}
}

func TestPreviewResponseBudgetFailsClosedWithoutToken(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "oversized.txt")
	original := strings.Repeat("a", 140<<10)
	if err := os.WriteFile(target, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	server := &mcpServer{root: root}

	_, err := server.previewEdits(mcpPreviewArgs{
		Path: target,
		Operations: []EditOperation{{
			Type:    "replace",
			Find:    original,
			Replace: strings.Repeat("b", 140<<10),
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "safety limit") {
		t.Fatalf("expected response budget rejection, got %v", err)
	}
	if len(server.previews) != 0 {
		t.Fatal("oversized preview must not issue or retain a preview token")
	}
	assertFileContent(t, target, original)
}

func TestMCPResponseBudgetReturnsCompactRPCError(t *testing.T) {
	var output bytes.Buffer
	server := &mcpServer{
		output:       &output,
		writer:       bufio.NewWriter(&output),
		telemetry:    noopMCPTelemetry{},
		writeTimeout: time.Second,
	}
	err := server.sendMCPResponse(mcpResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage("7"),
		Result:  map[string]any{"huge": strings.Repeat("x", maxMCPResponseBytes)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Len() >= maxMCPResponseBytes {
		t.Fatalf("fallback response still exceeds budget: %d", output.Len())
	}
	var response mcpResponse
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != -32001 {
		t.Fatalf("expected oversized-response RPC error, got %#v", response)
	}
}

func TestMCPResponseWriteTimeoutClosesBlockedTransport(t *testing.T) {
	output := newBlockingWriteCloser()
	server := &mcpServer{
		output:       output,
		writer:       bufio.NewWriter(output),
		telemetry:    noopMCPTelemetry{},
		writeTimeout: 25 * time.Millisecond,
	}
	started := time.Now()
	err := server.sendMCPResponse(mcpResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage("9"),
		Result:  map[string]any{"ok": true},
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected write timeout, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("write timeout returned too slowly: %s", time.Since(started))
	}
	select {
	case <-output.started:
	default:
		t.Fatal("blocking writer was never reached")
	}
}

func numberedLines(count int) []string {
	lines := make([]string, count)
	for index := range lines {
		lines[index] = fmt.Sprintf("line-%03d", index+1)
	}
	return lines
}

type blockingWriteCloser struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingWriteCloser() *blockingWriteCloser {
	return &blockingWriteCloser{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (writer *blockingWriteCloser) Write([]byte) (int, error) {
	writer.once.Do(func() { close(writer.started) })
	<-writer.release
	return 0, io.ErrClosedPipe
}

func (writer *blockingWriteCloser) Close() error {
	select {
	case <-writer.release:
	default:
		close(writer.release)
	}
	return nil
}
