package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	mcpServerVersion           = "0.3.1"
	maxMCPResponseBytes        = 256 << 10
	mcpResponseEnvelopeReserve = 1024
	defaultMCPWriteTimeout     = 15 * time.Second
)

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

type mcpServer struct {
	root         string
	decoder      *json.Decoder
	output       io.Writer
	writer       *bufio.Writer
	telemetry    mcpTelemetry
	writeTimeout time.Duration
	previews     map[string]previewPlan
	requestSeq   uint64
	trace        *mcpRequestTrace
}

type mcpApplyArgs struct {
	Path   string `json:"path"`
	Patch  string `json:"patch"`
	DryRun bool   `json:"dry_run,omitempty"`
}

type mcpCreateArgs struct {
	Path          string `json:"path"`
	Content       string `json:"content"`
	CreateParents *bool  `json:"create_parents,omitempty"`
}

type mcpRollbackArgs struct {
	Path string `json:"path"`
}

func runMCP(root string) int {
	telemetry := newJSONLineMCPTelemetry(os.Stderr)
	defer telemetry.Close()

	resolvedRoot, err := resolveMCPRoot(root)
	if err != nil {
		telemetry.Emit("server_root_error", map[string]any{"error": err.Error()})
		fmt.Fprintf(os.Stderr, "clipfit MCP root error: %v\n", err)
		return 2
	}
	telemetry.Emit("server_start", map[string]any{
		"root":                 resolvedRoot,
		"max_response_bytes":   maxMCPResponseBytes,
		"write_timeout_millis": defaultMCPWriteTimeout.Milliseconds(),
	})
	if err := serveMCPWithOptions(os.Stdin, os.Stdout, resolvedRoot, telemetry, defaultMCPWriteTimeout); err != nil {
		telemetry.Emit("server_error", map[string]any{"error": err.Error()})
		fmt.Fprintf(os.Stderr, "clipfit MCP server error: %v\n", err)
		return 2
	}
	telemetry.Emit("server_stop", map[string]any{"status": "eof"})
	return 0
}

func resolveMCPRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("root must not be empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("root is not a directory: %s", resolved)
	}
	return filepath.Clean(resolved), nil
}

func serveMCP(in io.Reader, out io.Writer, resolvedRoot string) error {
	return serveMCPWithOptions(in, out, resolvedRoot, noopMCPTelemetry{}, defaultMCPWriteTimeout)
}

func serveMCPWithOptions(in io.Reader, out io.Writer, resolvedRoot string, telemetry mcpTelemetry, writeTimeout time.Duration) error {
	if telemetry == nil {
		telemetry = noopMCPTelemetry{}
	}
	if writeTimeout <= 0 {
		writeTimeout = defaultMCPWriteTimeout
	}
	server := &mcpServer{
		root:         resolvedRoot,
		decoder:      json.NewDecoder(in),
		output:       out,
		writer:       bufio.NewWriter(out),
		telemetry:    telemetry,
		writeTimeout: writeTimeout,
		previews:     make(map[string]previewPlan),
	}

	for {
		decodeStarted := time.Now()
		var request mcpRequest
		if err := server.decoder.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) {
				server.emitTelemetry("transport_eof", nil)
				return nil
			}
			server.emitTelemetry("request_decode_error", map[string]any{
				"duration_ms": durationMilliseconds(decodeStarted),
				"error":       err.Error(),
			})
			return fmt.Errorf("decode JSON-RPC request: %w", err)
		}

		server.requestSeq++
		server.trace = &mcpRequestTrace{
			Sequence:    server.requestSeq,
			RequestID:   mcpRequestIDForTelemetry(request.ID),
			Method:      request.Method,
			Started:     decodeStarted,
			ParamsBytes: len(request.Params),
		}
		server.emitTelemetry("request_decoded", map[string]any{
			"decode_duration_ms": durationMilliseconds(decodeStarted),
		})

		var handleErr error
		if request.JSONRPC != "" && request.JSONRPC != "2.0" {
			if hasMCPRequestID(request.ID) {
				handleErr = server.sendError(request.ID, -32600, "invalid JSON-RPC version")
			}
		} else {
			handleErr = server.handle(request)
		}
		if handleErr != nil {
			server.emitTelemetry("request_complete", map[string]any{
				"duration_ms": durationMilliseconds(server.trace.Started),
				"status":      "error",
				"error":       handleErr.Error(),
			})
			server.trace = nil
			return handleErr
		}
		server.emitTelemetry("request_complete", map[string]any{
			"duration_ms": durationMilliseconds(server.trace.Started),
			"status":      "ok",
		})
		server.trace = nil
	}
}

func hasMCPRequestID(id json.RawMessage) bool {
	return len(id) != 0 && string(id) != "null"
}

func mcpRequestIDForTelemetry(id json.RawMessage) string {
	if len(id) == 0 {
		return ""
	}
	if len(id) > 128 {
		return "<oversized>"
	}
	return string(id)
}

func (server *mcpServer) handle(request mcpRequest) error {
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(request.Params) != 0 {
			_ = json.Unmarshal(request.Params, &params)
		}
		version := params.ProtocolVersion
		if version == "" {
			version = "2025-06-18"
		}
		return server.sendResult(request.ID, map[string]any{
			"protocolVersion": version,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name":    "clipfit",
				"version": mcpServerVersion,
			},
			"instructions": "For existing files, ALWAYS call clipfit_preview with structured operations first, inspect every returned hunk, then call clipfit_apply with its preview_id. Prefer replace_block with a unique verbatim anchor above find. Preview fails closed on missing or ambiguous matches and apply refuses if the file changed. Use clipfit_create only for new files. All paths must stay within " + server.root + ".",
		})
	case "ping":
		return server.sendResult(request.ID, map[string]any{})
	case "tools/list":
		return server.sendResult(request.ID, map[string]any{"tools": mcpToolDefinitions()})
	case "tools/call":
		return server.handleToolCall(request)
	case "notifications/initialized", "notifications/cancelled":
		return nil
	default:
		if !hasMCPRequestID(request.ID) {
			return nil
		}
		return server.sendError(request.ID, -32601, "method not found: "+request.Method)
	}
}

func (server *mcpServer) handleToolCall(request mcpRequest) error {
	if !hasMCPRequestID(request.ID) {
		return nil
	}
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		return server.sendError(request.ID, -32602, "invalid tools/call params: "+err.Error())
	}
	if len(params.Arguments) == 0 {
		params.Arguments = json.RawMessage("{}")
	}
	if server.trace != nil {
		server.trace.Tool = params.Name
		server.trace.ArgsBytes = len(params.Arguments)
	}
	toolStarted := time.Now()
	server.emitTelemetry("tool_start", nil)

	var result any
	switch params.Name {
	case "clipfit_preview":
		var args mcpPreviewArgs
		if err := decodeMCPArgs(params.Arguments, &args); err != nil {
			result = mcpToolFailure(err)
			break
		}
		if server.trace != nil {
			server.trace.Operations = len(args.Operations)
		}
		res, err := server.previewEdits(args)
		if err != nil {
			result = mcpToolFailure(err)
		} else {
			result = mcpToolSuccess(res)
		}
	case "clipfit_apply":
		var args mcpCommitArgs
		if err := decodeMCPArgs(params.Arguments, &args); err != nil {
			result = mcpToolFailure(err)
			break
		}
		res, err := server.commitPreview(args)
		if err != nil {
			result = mcpToolFailure(err)
		} else {
			result = mcpToolSuccess(res)
		}
	case "clipfit_edit":
		var args mcpPreviewArgs
		if err := decodeMCPArgs(params.Arguments, &args); err != nil {
			result = mcpToolFailure(err)
			break
		}
		if server.trace != nil {
			server.trace.Operations = len(args.Operations)
		}
		res, err := server.editDirect(args)
		if err != nil {
			result = mcpToolFailure(err)
		} else {
			result = mcpToolSuccess(res)
		}
	case "clipfit_create":
		var args mcpCreateArgs
		if err := decodeMCPArgs(params.Arguments, &args); err != nil {
			result = mcpToolFailure(err)
			break
		}
		if strings.TrimSpace(args.Path) == "" {
			result = mcpToolFailure(errors.New("path is required"))
			break
		}
		res, err := server.create(args)
		if err != nil {
			result = mcpToolFailure(err)
		} else {
			result = mcpToolSuccess(res)
		}
	case "clipfit_rollback":
		var args mcpRollbackArgs
		if err := decodeMCPArgs(params.Arguments, &args); err != nil {
			result = mcpToolFailure(err)
			break
		}
		if strings.TrimSpace(args.Path) == "" {
			result = mcpToolFailure(errors.New("path is required"))
			break
		}
		res, err := server.rollback(args.Path)
		if err != nil {
			result = mcpToolFailure(err)
		} else {
			result = mcpToolSuccess(res)
		}
	default:
		result = mcpToolFailure(fmt.Errorf("unknown tool: %s", params.Name))
	}
	failed := false
	if toolResult, ok := result.(map[string]any); ok {
		failed, _ = toolResult["isError"].(bool)
	}
	status := "ok"
	if failed {
		status = "tool_error"
	}
	server.emitTelemetry("tool_complete", map[string]any{
		"duration_ms": durationMilliseconds(toolStarted),
		"status":      status,
	})
	return server.sendResult(request.ID, result)
}

func decodeMCPArgs(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("invalid tool arguments: multiple JSON values")
		}
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	return nil
}

func (server *mcpServer) apply(args mcpApplyArgs) (Result, error) {
	target, err := secureMCPTarget(server.root, args.Path, true)
	if err != nil {
		return Result{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return Result{}, fmt.Errorf("stat target: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Result{}, fmt.Errorf("target is not a regular file: %s", target)
	}

	rawData, err := os.ReadFile(target)
	if err != nil {
		return Result{}, fmt.Errorf("read target: %w", err)
	}
	content, meta := decodeTarget(rawData, target)
	commands := parseKissCommands(preprocessSpec(args.Patch))
	if len(commands) == 0 {
		return Result{}, errors.New("patch contains no valid ClipFit commands")
	}

	updated, stats := applyCommands(content, commands)
	hunks := computeChanges(content, updated)
	missed := 0
	for _, stat := range stats {
		if stat.Matches == 0 {
			missed++
		}
	}
	result := Result{
		Action:      "apply",
		Target:      target,
		OK:          missed == 0,
		Commands:    len(commands),
		Stats:       stats,
		ChangeCount: len(hunks),
		Hunks:       hunks,
	}
	if args.DryRun {
		result.Message = "dry-run: file and backup were not changed"
		return result, nil
	}

	backup, err := writeBackup(target, rawData)
	if err != nil {
		return Result{}, fmt.Errorf("create backup: %w", err)
	}
	result.BackupPath = backup
	if err := writeAtomic(target, renderOutput(updated, meta), meta.mode); err != nil {
		return Result{}, fmt.Errorf("write target: %w", err)
	}
	if missed != 0 {
		result.Message = fmt.Sprintf("%d command(s) matched zero locations; inspect stats and retry", missed)
	}
	return result, nil
}

func (server *mcpServer) create(args mcpCreateArgs) (Result, error) {
	target, err := secureMCPTarget(server.root, args.Path, false)
	if err != nil {
		return Result{}, err
	}
	if _, err := os.Lstat(target); err == nil {
		return Result{}, fmt.Errorf("refusing to overwrite existing path: %s", target)
	} else if !os.IsNotExist(err) {
		return Result{}, fmt.Errorf("inspect target: %w", err)
	}

	createParents := true
	if args.CreateParents != nil {
		createParents = *args.CreateParents
	}
	parent := filepath.Dir(target)
	if createParents {
		if err := os.MkdirAll(parent, 0755); err != nil {
			return Result{}, fmt.Errorf("create parent directories: %w", err)
		}
		// Check again after creating directories so a symlinked parent cannot
		// redirect a write outside the configured root.
		target, err = secureMCPTarget(server.root, target, false)
		if err != nil {
			return Result{}, err
		}
	} else if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		if err != nil {
			return Result{}, fmt.Errorf("parent directory does not exist: %w", err)
		}
		return Result{}, fmt.Errorf("parent path is not a directory: %s", parent)
	}

	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return Result{}, fmt.Errorf("create file: %w", err)
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(target)
		}
	}()
	if _, err := io.WriteString(file, args.Content); err != nil {
		return Result{}, fmt.Errorf("write new file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return Result{}, fmt.Errorf("sync new file: %w", err)
	}
	if err := file.Close(); err != nil {
		return Result{}, fmt.Errorf("close new file: %w", err)
	}
	keep = true

	return Result{
		Action:      "create",
		Target:      target,
		OK:          true,
		ChangeCount: 1,
		Message: fmt.Sprintf(
			"created new file (%d bytes); existing files are never overwritten by clipfit_create",
			len(args.Content),
		),
	}, nil
}

func (server *mcpServer) rollback(path string) (Result, error) {
	target, err := secureMCPTarget(server.root, path, true)
	if err != nil {
		return Result{}, err
	}
	backup := backupPath(target)
	data, err := os.ReadFile(backup)
	if err != nil {
		return Result{}, fmt.Errorf("backup not found (it may have expired): %w", err)
	}
	mode := os.FileMode(0644)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode()
	}
	if err := writeAtomic(target, data, mode); err != nil {
		return Result{}, fmt.Errorf("restore backup: %w", err)
	}
	return Result{
		Action:     "rollback",
		Target:     target,
		OK:         true,
		BackupPath: backup,
		Message:    "restored from the most recent ClipFit backup",
	}, nil
}

func secureMCPTarget(root, input string, mustExist bool) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", errors.New("path must not be empty")
	}
	if strings.IndexByte(input, 0) >= 0 {
		return "", errors.New("path contains a NUL byte")
	}
	candidate := input
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve target path: %w", err)
	}
	abs = filepath.Clean(abs)
	if !pathWithinRoot(root, abs) {
		return "", fmt.Errorf("path is outside configured root %s: %s", root, input)
	}

	ancestor := abs
	for {
		_, err = os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect path: %w", err)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("no existing ancestor for path: %s", input)
		}
		ancestor = parent
	}
	resolvedAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", fmt.Errorf("resolve path symlinks: %w", err)
	}
	if !pathWithinRoot(root, resolvedAncestor) {
		return "", fmt.Errorf("path escapes configured root through a symlink: %s", input)
	}

	if mustExist {
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", fmt.Errorf("target does not exist: %w", err)
		}
		if !pathWithinRoot(root, resolved) {
			return "", fmt.Errorf("path escapes configured root through a symlink: %s", input)
		}
		abs = resolved
	}
	return abs, nil
}

func pathWithinRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func mcpToolSuccess(result any) map[string]any {
	return map[string]any{
		"content": []map[string]any{{
			"type": "text",
			"text": mcpToolSummary(result),
		}},
		"structuredContent": result,
		"isError":           false,
	}
}

func mcpToolSummary(result any) string {
	switch value := result.(type) {
	case EditPreviewResult:
		return fmt.Sprintf(
			"preview ready: preview_id=%s target=%s operations=%d hunks=%d; inspect structuredContent.hunks before apply",
			value.PreviewID,
			value.Target,
			len(value.Operations),
			value.ChangeCount,
		)
	case EditReceipt:
		return fmt.Sprintf(
			"%s complete: target=%s operations=%d changes=%d backup=%s",
			value.Action,
			value.Target,
			value.OperationCount,
			value.ChangeCount,
			value.BackupPath,
		)
	case Result:
		return fmt.Sprintf(
			"%s complete: target=%s ok=%t changes=%d; inspect structuredContent for details",
			value.Action,
			value.Target,
			value.OK,
			value.ChangeCount,
		)
	default:
		return "tool completed; inspect structuredContent for the full result"
	}
}

func mcpToolFailure(err error) map[string]any {
	structured := map[string]any{
		"ok":    false,
		"error": err.Error(),
	}
	return map[string]any{
		"content": []map[string]any{{
			"type": "text",
			"text": "ClipFit error: " + err.Error(),
		}},
		"structuredContent": structured,
		"isError":           true,
	}
}

func (server *mcpServer) sendResult(id json.RawMessage, result any) error {
	return server.send(mcpResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (server *mcpServer) sendError(id json.RawMessage, code int, message string) error {
	return server.send(mcpResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &mcpRPCError{Code: code, Message: message},
	})
}

func (server *mcpServer) send(value any) error {
	response, ok := value.(mcpResponse)
	if !ok {
		return fmt.Errorf("internal MCP response has unexpected type %T", value)
	}
	return server.sendMCPResponse(response)
}

func legacyMCPToolDefinitions() []map[string]any {
	writeAnnotations := map[string]any{
		"readOnlyHint":    false,
		"destructiveHint": true,
		"idempotentHint":  false,
		"openWorldHint":   false,
	}
	return []map[string]any{
		{
			"name":        "clipfit_apply",
			"description": "Modify one existing file atomically using one or more ClipFit REPLACE, REPLACE_BLOCK, or SWAP_NAME blocks. Creates a short-lived rollback backup. Check ok, stats, and hunks after every call.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"path", "patch"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Absolute path within the configured root, or a path relative to that root.",
					},
					"patch": map[string]any{
						"type":        "string",
						"description": "ClipFit command text. Example: ===REPLACE_BLOCK=== newline exact old block newline ===WITH=== newline new block newline ===END_REP===.",
					},
					"dry_run": map[string]any{
						"type":        "boolean",
						"default":     false,
						"description": "Preview matches and hunks without writing or creating a backup.",
					},
				},
			},
			"annotations": writeAnnotations,
		},
		{
			"name":        "clipfit_create",
			"description": "Create a new UTF-8 file. This tool always refuses to overwrite any existing file or symlink.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"path", "content"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Absolute path within the configured root, or a path relative to that root.",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Complete content for the new file.",
					},
					"create_parents": map[string]any{
						"type":        "boolean",
						"default":     true,
						"description": "Create missing parent directories inside the configured root.",
					},
				},
			},
			"annotations": writeAnnotations,
		},
		{
			"name":        "clipfit_rollback",
			"description": "Restore one existing file from its most recent short-lived ClipFit apply backup. Use immediately after an incorrect apply because later edits will be overwritten.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"path"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Absolute path within the configured root, or a path relative to that root.",
					},
				},
			},
			"annotations": writeAnnotations,
		},
	}
}
