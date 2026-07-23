package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMCPStructuredTelemetryCoversRequestLifecycle(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "telemetry.txt")
	if err := os.WriteFile(target, []byte("alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := resolveMCPRoot(root)
	if err != nil {
		t.Fatal(err)
	}

	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      77,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "clipfit_preview",
			"arguments": map[string]any{
				"path": target,
				"operations": []map[string]any{{
					"type":    "replace",
					"find":    "alpha",
					"replace": "beta",
				}},
			},
		},
	}
	requestData, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	requestData = append(requestData, byte(10))

	var protocolOutput bytes.Buffer
	var diagnosticOutput bytes.Buffer
	telemetry := newJSONLineMCPTelemetry(&diagnosticOutput)
	err = serveMCPWithOptions(
		bytes.NewReader(requestData),
		&protocolOutput,
		resolvedRoot,
		telemetry,
		time.Second,
	)
	telemetry.Close()
	if err != nil {
		t.Fatal(err)
	}

	var response mcpResponse
	protocolDecoder := json.NewDecoder(bytes.NewReader(protocolOutput.Bytes()))
	if err := protocolDecoder.Decode(&response); err != nil {
		t.Fatalf("stdout did not contain a valid JSON-RPC response: %v", err)
	}
	if response.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %#v", response.Error)
	}
	var extraProtocolValue any
	if err := protocolDecoder.Decode(&extraProtocolValue); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout contained data after the JSON-RPC response: %v", err)
	}

	records := make(map[string][]map[string]any)
	diagnosticDecoder := json.NewDecoder(bytes.NewReader(diagnosticOutput.Bytes()))
	for {
		var record map[string]any
		err := diagnosticDecoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("stderr telemetry is not JSONL: %v", err)
		}
		event, _ := record["event"].(string)
		records[event] = append(records[event], record)
	}
	for _, event := range []string{
		"request_decoded",
		"tool_start",
		"preview_phase",
		"preview_ready",
		"tool_complete",
		"response_encode_start",
		"response_encode_end",
		"response_write_start",
		"response_write_end",
		"response_flush_start",
		"response_flush_end",
		"request_complete",
		"transport_eof",
	} {
		if len(records[event]) == 0 {
			t.Errorf("missing telemetry event %q", event)
		}
	}

	toolComplete := records["tool_complete"][0]
	if toolComplete["request_id"] != "77" ||
		toolComplete["method"] != "tools/call" ||
		toolComplete["tool"] != "clipfit_preview" ||
		toolComplete["operation_count"] != float64(1) {
		t.Fatalf("tool telemetry is missing request context: %#v", toolComplete)
	}
	for _, field := range []string{"params_bytes", "args_bytes", "duration_ms"} {
		if value, ok := toolComplete[field].(float64); !ok || value < 0 {
			t.Fatalf("tool telemetry field %q is missing or invalid: %#v", field, toolComplete)
		}
	}

	phases := make(map[string]map[string]any)
	for _, record := range records["preview_phase"] {
		if phase, ok := record["phase"].(string); ok {
			phases[phase] = record
		}
	}
	for _, phase := range []string{"resolve_target", "read_file", "apply_operations", "compute_changes"} {
		if phases[phase] == nil {
			t.Errorf("missing preview phase %q", phase)
		}
	}
	if phases["read_file"]["input_bytes"] != float64(len("alpha\n")) {
		t.Fatalf("read phase has wrong input size: %#v", phases["read_file"])
	}
	diffPhase := phases["compute_changes"]
	if diffPhase["hunk_count"] != float64(1) ||
		diffPhase["removed_lines"] != float64(1) ||
		diffPhase["added_lines"] != float64(1) {
		t.Fatalf("diff phase is missing hunk statistics: %#v", diffPhase)
	}

	encodeEnd := records["response_encode_end"][0]
	if responseBytes, ok := encodeEnd["response_bytes"].(float64); !ok || responseBytes <= 0 {
		t.Fatalf("encode telemetry is missing response bytes: %#v", encodeEnd)
	}
}
