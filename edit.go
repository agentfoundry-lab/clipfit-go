package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	previewTTL      = 10 * time.Minute
	maxPreviewPlans = 64
)

type EditOperation struct {
	Type            string `json:"type"`
	Anchor          string `json:"anchor,omitempty"`
	Find            string `json:"find"`
	Replace         string `json:"replace"`
	ExpectedMatches *int   `json:"expected_matches,omitempty"`
}

type mcpPreviewArgs struct {
	Path       string          `json:"path"`
	Operations []EditOperation `json:"operations"`
}

type mcpCommitArgs struct {
	PreviewID string `json:"preview_id"`
}

type OperationStat struct {
	Index            int    `json:"index"`
	Type             string `json:"type"`
	AnchorMatches    int    `json:"anchor_matches,omitempty"`
	CandidateMatches int    `json:"candidate_matches"`
	AppliedMatches   int    `json:"applied_matches"`
	ExpectedMatches  int    `json:"expected_matches,omitempty"`
}

type EditPreviewResult struct {
	Action       string          `json:"action"`
	Target       string          `json:"target"`
	OK           bool            `json:"ok"`
	PreviewID    string          `json:"preview_id"`
	ExpiresAt    string          `json:"expires_at"`
	BeforeSHA256 string          `json:"before_sha256"`
	AfterSHA256  string          `json:"after_sha256"`
	Operations   []OperationStat `json:"operations"`
	ChangeCount  int             `json:"change_count"`
	Hunks        []Hunk          `json:"hunks"`
	Message      string          `json:"message"`
}

type EditReceipt struct {
	Action         string `json:"action"`
	Target         string `json:"target"`
	OK             bool   `json:"ok"`
	PreviewID      string `json:"preview_id,omitempty"`
	BeforeSHA256   string `json:"before_sha256"`
	AfterSHA256    string `json:"after_sha256"`
	OperationCount int    `json:"operation_count"`
	ChangeCount    int    `json:"change_count"`
	BackupPath     string `json:"backup_path"`
	Message        string `json:"message"`
}

type previewPlan struct {
	ID         string
	Target     string
	BeforeHash string
	After      []byte
	Mode       os.FileMode
	ExpiresAt  time.Time
	Operations []OperationStat
	Hunks      []Hunk
}

func (server *mcpServer) previewEdits(args mcpPreviewArgs) (EditPreviewResult, error) {
	plan, err := server.planEdits(args, "preview_phase")
	if err != nil {
		return EditPreviewResult{}, err
	}
	previewID, err := newPreviewID()
	if err != nil {
		return EditPreviewResult{}, err
	}
	expiresAt := time.Now().Add(previewTTL)
	plan.ID = previewID
	plan.ExpiresAt = expiresAt
	result := EditPreviewResult{
		Action:       "preview",
		Target:       plan.Target,
		OK:           true,
		PreviewID:    previewID,
		ExpiresAt:    expiresAt.UTC().Format(time.RFC3339),
		BeforeSHA256: plan.BeforeHash,
		AfterSHA256:  hashBytes(plan.After),
		Operations:   plan.Operations,
		ChangeCount:  len(plan.Hunks),
		Hunks:        plan.Hunks,
		Message:      "preview only: inspect every hunk, then call clipfit_apply with preview_id; the file has not been written",
	}
	toolResultData, err := json.Marshal(mcpToolSuccess(result))
	if err != nil {
		return EditPreviewResult{}, fmt.Errorf("encode preview response: %w", err)
	}
	estimatedResponseBytes := len(toolResultData) + mcpResponseEnvelopeReserve
	server.emitTelemetry("preview_ready", map[string]any{
		"estimated_response_bytes": estimatedResponseBytes,
		"hunk_count":               len(plan.Hunks),
	})
	if estimatedResponseBytes > maxMCPResponseBytes {
		server.emitTelemetry("preview_response_rejected", map[string]any{
			"estimated_response_bytes": estimatedResponseBytes,
			"max_response_bytes":       maxMCPResponseBytes,
		})
		return EditPreviewResult{}, fmt.Errorf(
			"preview response would be %d bytes, exceeding the %d-byte safety limit; split the edits into smaller previews",
			estimatedResponseBytes,
			maxMCPResponseBytes,
		)
	}
	server.storePreview(plan)
	return result, nil
}

func (server *mcpServer) planEdits(args mcpPreviewArgs, telemetryEvent string) (previewPlan, error) {
	if strings.TrimSpace(args.Path) == "" {
		return previewPlan{}, errors.New("path is required")
	}
	if len(args.Operations) == 0 {
		return previewPlan{}, errors.New("operations must contain at least one edit")
	}
	if len(args.Operations) > 100 {
		return previewPlan{}, errors.New("operations exceeds the maximum of 100 edits per request")
	}

	resolveStarted := time.Now()
	target, err := secureMCPTarget(server.root, args.Path, true)
	if err != nil {
		return previewPlan{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return previewPlan{}, fmt.Errorf("stat target: %w", err)
	}
	if !info.Mode().IsRegular() {
		return previewPlan{}, fmt.Errorf("target is not a regular file: %s", target)
	}
	server.emitTelemetry(telemetryEvent, map[string]any{
		"phase":       "resolve_target",
		"duration_ms": durationMilliseconds(resolveStarted),
	})

	readStarted := time.Now()
	rawData, err := os.ReadFile(target)
	if err != nil {
		return previewPlan{}, fmt.Errorf("read target: %w", err)
	}
	content, meta := decodeTarget(rawData, target)
	server.emitTelemetry(telemetryEvent, map[string]any{
		"phase":       "read_file",
		"duration_ms": durationMilliseconds(readStarted),
		"input_bytes": len(rawData),
	})

	applyStarted := time.Now()
	updated, stats, err := applyStructuredOperations(content, args.Operations)
	if err != nil {
		return previewPlan{}, err
	}
	server.emitTelemetry(telemetryEvent, map[string]any{
		"phase":       "apply_operations",
		"duration_ms": durationMilliseconds(applyStarted),
	})

	diffStarted := time.Now()
	hunks := computeChanges(content, updated)
	diffFields := hunkTelemetryFields(hunks)
	diffFields["phase"] = "compute_changes"
	diffFields["duration_ms"] = durationMilliseconds(diffStarted)
	server.emitTelemetry(telemetryEvent, diffFields)
	if len(hunks) == 0 {
		return previewPlan{}, errors.New("operations produced no content change")
	}
	return previewPlan{
		Target:     target,
		BeforeHash: hashBytes(rawData),
		After:      renderOutput(updated, meta),
		Mode:       meta.mode,
		Operations: stats,
		Hunks:      hunks,
	}, nil
}

func (server *mcpServer) commitPreview(args mcpCommitArgs) (EditReceipt, error) {
	previewID := strings.TrimSpace(args.PreviewID)
	if previewID == "" {
		return EditReceipt{}, errors.New("preview_id is required")
	}
	server.prunePreviews()
	plan, ok := server.previews[previewID]
	if !ok {
		return EditReceipt{}, errors.New("preview_id is unknown, expired, already applied, or belongs to a previous server session; run clipfit_preview again")
	}
	if time.Now().After(plan.ExpiresAt) {
		delete(server.previews, previewID)
		return EditReceipt{}, errors.New("preview_id expired; run clipfit_preview again")
	}

	backup, err := server.writePlannedEdit(plan, "preview")
	if err != nil {
		delete(server.previews, previewID)
		return EditReceipt{}, err
	}
	delete(server.previews, previewID)
	return editReceipt(
		"apply",
		previewID,
		plan,
		backup,
		"applied exactly the reviewed preview; rollback remains available for this file",
	), nil
}

func (server *mcpServer) editDirect(args mcpPreviewArgs) (EditReceipt, error) {
	plan, err := server.planEdits(args, "edit_phase")
	if err != nil {
		return EditReceipt{}, err
	}
	backup, err := server.writePlannedEdit(plan, "direct edit planning")
	if err != nil {
		return EditReceipt{}, err
	}
	return editReceipt(
		"edit",
		"",
		plan,
		backup,
		"applied directly after server-side match validation; rollback remains available for this file",
	), nil
}

func (server *mcpServer) writePlannedEdit(plan previewPlan, plannedFrom string) (string, error) {
	target, err := secureMCPTarget(server.root, plan.Target, true)
	if err != nil {
		return "", err
	}
	if target != plan.Target {
		return "", fmt.Errorf("target path changed since %s; no write occurred", plannedFrom)
	}
	current, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("read target before apply: %w", err)
	}
	if hashBytes(current) != plan.BeforeHash {
		return "", fmt.Errorf("target content changed since %s; no write occurred", plannedFrom)
	}
	backup, err := writeBackup(target, current)
	if err != nil {
		return "", fmt.Errorf("create backup: %w", err)
	}
	if err := writeAtomic(target, plan.After, plan.Mode); err != nil {
		return "", fmt.Errorf("write target: %w", err)
	}
	return backup, nil
}

func editReceipt(action, previewID string, plan previewPlan, backup, message string) EditReceipt {
	return EditReceipt{
		Action:         action,
		Target:         plan.Target,
		OK:             true,
		PreviewID:      previewID,
		BeforeSHA256:   plan.BeforeHash,
		AfterSHA256:    hashBytes(plan.After),
		OperationCount: len(plan.Operations),
		ChangeCount:    len(plan.Hunks),
		BackupPath:     backup,
		Message:        message,
	}
}

func applyStructuredOperations(original string, operations []EditOperation) (string, []OperationStat, error) {
	current := original
	stats := make([]OperationStat, 0, len(operations))

	for index, operation := range operations {
		kind := strings.ToLower(strings.TrimSpace(operation.Type))
		if kind == "" {
			kind = "replace_block"
		}
		if operation.Find == "" {
			return "", nil, fmt.Errorf("operation #%d: find must not be empty", index+1)
		}
		if operation.ExpectedMatches != nil && *operation.ExpectedMatches < 1 {
			return "", nil, fmt.Errorf("operation #%d: expected_matches must be at least 1", index+1)
		}

		command := Command{FindStr: operation.Find, ReplaceStr: operation.Replace}
		stat := OperationStat{Index: index, Type: kind}
		switch kind {
		case "replace_block":
			command.Type = "REPLACE_BLOCK"
			command.AnchorStr = operation.Anchor
			cleanFind := dedent(operation.Find)
			if cleanFind == "" {
				return "", nil, fmt.Errorf("operation #%d: find becomes empty after normalization", index+1)
			}
			targetRE, err := regexp.Compile(buildFlexPattern(cleanFind))
			if err != nil {
				return "", nil, fmt.Errorf("operation #%d: compile find matcher: %w", index+1, err)
			}

			if strings.TrimSpace(operation.Anchor) != "" {
				if operation.ExpectedMatches != nil && *operation.ExpectedMatches != 1 {
					return "", nil, fmt.Errorf("operation #%d: anchored replace_block always applies one target; expected_matches must be 1 or omitted", index+1)
				}
				cleanAnchor := dedent(operation.Anchor)
				anchorRE, err := regexp.Compile(buildFlexPattern(cleanAnchor))
				if err != nil {
					return "", nil, fmt.Errorf("operation #%d: compile anchor matcher: %w", index+1, err)
				}
				anchors := anchorRE.FindAllStringSubmatchIndex(current, -1)
				stat.AnchorMatches = len(anchors)
				if len(anchors) != 1 {
					return "", nil, fmt.Errorf("operation #%d: anchor matched %d locations; copy a unique verbatim line or block above the target", index+1, len(anchors))
				}
				searchStart := anchors[0][1]
				candidates := targetRE.FindAllStringSubmatchIndex(current[searchStart:], -1)
				stat.CandidateMatches = len(candidates)
				stat.ExpectedMatches = 1
				if len(candidates) == 0 {
					return "", nil, fmt.Errorf("operation #%d: anchor matched once but find did not occur after it", index+1)
				}
				stat.AppliedMatches = 1
			} else {
				candidates := targetRE.FindAllStringSubmatchIndex(current, -1)
				stat.CandidateMatches = len(candidates)
				expected := expectedOrDefault(operation.ExpectedMatches, 1)
				stat.ExpectedMatches = expected
				if len(candidates) != expected {
					return "", nil, fmt.Errorf("operation #%d: unanchored find matched %d locations, expected %d; add a unique anchor or set expected_matches deliberately", index+1, len(candidates), expected)
				}
				stat.AppliedMatches = len(candidates)
			}

		case "replace":
			if strings.TrimSpace(operation.Anchor) != "" {
				return "", nil, fmt.Errorf("operation #%d: anchor is supported only by replace_block", index+1)
			}
			if err := validateSingleLineReplace(operation.Find, operation.Replace); err != nil {
				return "", nil, fmt.Errorf("operation #%d: %w", index+1, err)
			}
			command.Type = "REPLACE"
			cleanFind := strings.ReplaceAll(operation.Find, "\u00A0", " ")
			count := strings.Count(current, cleanFind)
			expected := expectedOrDefault(operation.ExpectedMatches, 1)
			stat.CandidateMatches = count
			stat.ExpectedMatches = expected
			stat.AppliedMatches = count
			if count != expected {
				return "", nil, fmt.Errorf("operation #%d: literal find matched %d locations, expected %d", index+1, count, expected)
			}

		case "swap_name":
			if strings.TrimSpace(operation.Anchor) != "" {
				return "", nil, fmt.Errorf("operation #%d: anchor is not valid for swap_name", index+1)
			}
			command.Type = "SWAP_NAME"
			nameA := strings.TrimSpace(strings.ReplaceAll(operation.Find, "\u00A0", " "))
			nameB := strings.TrimSpace(strings.ReplaceAll(operation.Replace, "\u00A0", " "))
			if nameA == "" || nameB == "" || nameA == nameB {
				return "", nil, fmt.Errorf("operation #%d: swap_name requires two different non-empty identifiers", index+1)
			}
			swapRE, err := regexp.Compile("\\b(" + regexp.QuoteMeta(nameA) + "|" + regexp.QuoteMeta(nameB) + ")\\b")
			if err != nil {
				return "", nil, fmt.Errorf("operation #%d: compile swap matcher: %w", index+1, err)
			}
			count := len(swapRE.FindAllString(current, -1))
			stat.CandidateMatches = count
			stat.AppliedMatches = count
			if operation.ExpectedMatches == nil {
				if count == 0 {
					return "", nil, fmt.Errorf("operation #%d: swap_name matched zero identifiers", index+1)
				}
			} else {
				stat.ExpectedMatches = *operation.ExpectedMatches
				if count != *operation.ExpectedMatches {
					return "", nil, fmt.Errorf("operation #%d: swap_name matched %d identifiers, expected %d", index+1, count, *operation.ExpectedMatches)
				}
			}

		default:
			return "", nil, fmt.Errorf("operation #%d: unsupported type %q; use replace_block, replace, or swap_name", index+1, operation.Type)
		}

		updated, commandStats := applyCommands(current, []Command{command})
		if len(commandStats) != 1 || commandStats[0].Matches != stat.AppliedMatches {
			return "", nil, fmt.Errorf("operation #%d: internal match count changed during application", index+1)
		}
		if updated == current {
			return "", nil, fmt.Errorf("operation #%d: matched text but produced no content change", index+1)
		}
		current = updated
		stats = append(stats, stat)
	}
	return current, stats, nil
}

func expectedOrDefault(expected *int, fallback int) int {
	if expected == nil {
		return fallback
	}
	return *expected
}

func newPreviewID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate preview id: %w", err)
	}
	return hex.EncodeToString(random), nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (server *mcpServer) storePreview(plan previewPlan) {
	if server.previews == nil {
		server.previews = make(map[string]previewPlan)
	}
	server.prunePreviews()
	if len(server.previews) >= maxPreviewPlans {
		var oldestID string
		var oldestTime time.Time
		for id, existing := range server.previews {
			if oldestID == "" || existing.ExpiresAt.Before(oldestTime) {
				oldestID = id
				oldestTime = existing.ExpiresAt
			}
		}
		delete(server.previews, oldestID)
	}
	server.previews[plan.ID] = plan
}

func (server *mcpServer) prunePreviews() {
	now := time.Now()
	for id, plan := range server.previews {
		if now.After(plan.ExpiresAt) {
			delete(server.previews, id)
		}
	}
}
