package main

func mcpToolDefinitions() []map[string]any {
	previewAnnotations := map[string]any{
		"readOnlyHint":    true,
		"destructiveHint": false,
		"idempotentHint":  false,
		"openWorldHint":   false,
	}
	writeAnnotations := map[string]any{
		"readOnlyHint":    false,
		"destructiveHint": true,
		"idempotentHint":  false,
		"openWorldHint":   false,
	}
	pathProperty := map[string]any{
		"type":        "string",
		"description": "Absolute path within the configured root, or a path relative to that root.",
	}
	operationSchema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"type", "find", "replace"},
		"properties": map[string]any{
			"type": map[string]any{
				"type":        "string",
				"enum":        []string{"replace_block", "replace", "swap_name"},
				"description": "Use replace_block for normal code edits, replace for exact literal text, and swap_name only for an atomic two-way identifier swap.",
			},
			"anchor": map[string]any{
				"type":        "string",
				"description": "Optional for replace_block only. Copy one unique verbatim line or contiguous block above find. The server searches for the first find after this anchor and rejects a missing or non-unique anchor.",
			},
			"find": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Verbatim existing source text. For replace, this must be one line with no CR/LF. For replace_block, preserve the relative shape; indentation is normalized.",
			},
			"replace": map[string]any{
				"type":        "string",
				"description": "Replacement text. Empty is allowed to delete the matched text. For replace, this must be one line with no CR/LF; use replace_block for multi-line edits.",
			},
			"expected_matches": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Optional exact match count. Unanchored replace_block and replace default to exactly 1. Anchored replace_block always applies exactly 1. swap_name defaults to one-or-more.",
			},
		},
	}

	return []map[string]any{
		{
			"name":        "clipfit_preview",
			"description": "FIRST STEP for every existing-file edit. Validate structured operations against the current file and return localized hunks plus a short-lived preview_id. Does not write. Distant edits remain separate hunks, so normal multi-operation previews do not need arbitrary splitting. Fails closed if any find is missing or ambiguous; split into smaller previews only when the server returns an explicit response safety-limit error. Prefer replace_block with a unique verbatim anchor above find.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"path", "operations"},
				"properties": map[string]any{
					"path": pathProperty,
					"operations": map[string]any{
						"type":        "array",
						"minItems":    1,
						"maxItems":    100,
						"description": "Edits applied sequentially during preview; each operation sees the result of the previous one.",
						"items":       operationSchema,
					},
				},
			},
			"annotations": previewAnnotations,
		},
		{
			"name":        "clipfit_apply",
			"description": "SECOND STEP after clipfit_preview. Apply exactly the reviewed preview by preview_id. Refuses to write if the file changed, the token expired, was already used, or came from a previous MCP server session.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"preview_id"},
				"properties": map[string]any{
					"preview_id": map[string]any{
						"type":        "string",
						"minLength":   1,
						"description": "Opaque preview_id returned by clipfit_preview after inspecting all hunks.",
					},
				},
			},
			"annotations": writeAnnotations,
		},
		{
			"name":        "clipfit_edit",
			"description": "DIRECT MODE for an existing file. Applies structured operations in one call without returning preview hunks. It keeps fail-closed match validation, root and symlink checks, a same-call content hash check, backup creation, and atomic write. Use clipfit_preview plus clipfit_apply when the diff should be reviewed before writing.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"path", "operations"},
				"properties": map[string]any{
					"path": pathProperty,
					"operations": map[string]any{
						"type":        "array",
						"minItems":    1,
						"maxItems":    100,
						"description": "Edits applied sequentially; each operation sees the result of the previous one.",
						"items":       operationSchema,
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
					"path": pathProperty,
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
					"path": pathProperty,
				},
			},
			"annotations": writeAnnotations,
		},
	}
}
