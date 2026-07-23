package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

const (
	responseStageWrite int32 = iota + 1
	responseStageFlush
	responseStageDone
)

type responseWriteOutcome struct {
	written       int
	writeDuration time.Duration
	flushDuration time.Duration
	err           error
}

func (server *mcpServer) sendMCPResponse(response mcpResponse) error {
	encodeStarted := time.Now()
	server.emitTelemetry("response_encode_start", nil)
	payload, err := json.Marshal(response)
	if err != nil {
		server.emitTelemetry("response_encode_end", map[string]any{
			"duration_ms": durationMilliseconds(encodeStarted),
			"status":      "error",
			"error":       err.Error(),
		})
		return fmt.Errorf("encode JSON-RPC response: %w", err)
	}

	if len(payload)+1 > maxMCPResponseBytes && response.Error == nil {
		server.emitTelemetry("response_rejected", map[string]any{
			"response_bytes":     len(payload) + 1,
			"max_response_bytes": maxMCPResponseBytes,
		})
		response = mcpResponse{
			JSONRPC: "2.0",
			ID:      response.ID,
			Error: &mcpRPCError{
				Code:    -32001,
				Message: fmt.Sprintf("response exceeds the %d-byte safety limit; split the request into smaller operations", maxMCPResponseBytes),
			},
		}
		payload, err = json.Marshal(response)
		if err != nil {
			return fmt.Errorf("encode oversized-response error: %w", err)
		}
	}
	payload = append(payload, byte(10))
	server.emitTelemetry("response_encode_end", map[string]any{
		"duration_ms":    durationMilliseconds(encodeStarted),
		"response_bytes": len(payload),
		"status":         "ok",
	})
	return server.writeMCPResponse(payload)
}

func (server *mcpServer) writeMCPResponse(payload []byte) error {
	timeout := server.writeTimeout
	if timeout <= 0 {
		timeout = defaultMCPWriteTimeout
	}

	baseFields := server.telemetryFields(map[string]any{
		"response_bytes": len(payload),
	})
	emit := func(event string, extra map[string]any) {
		if server.telemetry == nil {
			return
		}
		fields := make(map[string]any, len(baseFields)+len(extra))
		for key, value := range baseFields {
			fields[key] = value
		}
		for key, value := range extra {
			fields[key] = value
		}
		server.telemetry.Emit(event, fields)
	}

	var stage atomic.Int32
	stage.Store(responseStageWrite)
	emit("response_write_start", nil)
	done := make(chan responseWriteOutcome, 1)
	go func() {
		outcome := responseWriteOutcome{}
		writeStarted := time.Now()
		outcome.written, outcome.err = server.writer.Write(payload)
		outcome.writeDuration = time.Since(writeStarted)
		writeFields := map[string]any{
			"duration_ms": float64(outcome.writeDuration.Microseconds()) / 1000,
			"written":     outcome.written,
		}
		if outcome.err != nil {
			writeFields["status"] = "error"
			writeFields["error"] = outcome.err.Error()
			emit("response_write_end", writeFields)
			done <- outcome
			return
		}
		if outcome.written != len(payload) {
			outcome.err = io.ErrShortWrite
			writeFields["status"] = "error"
			writeFields["error"] = outcome.err.Error()
			emit("response_write_end", writeFields)
			done <- outcome
			return
		}
		writeFields["status"] = "ok"
		emit("response_write_end", writeFields)

		stage.Store(responseStageFlush)
		emit("response_flush_start", nil)
		flushStarted := time.Now()
		outcome.err = server.writer.Flush()
		outcome.flushDuration = time.Since(flushStarted)
		flushFields := map[string]any{
			"duration_ms": float64(outcome.flushDuration.Microseconds()) / 1000,
			"status":      "ok",
		}
		if outcome.err != nil {
			flushFields["status"] = "error"
			flushFields["error"] = outcome.err.Error()
		}
		emit("response_flush_end", flushFields)
		stage.Store(responseStageDone)
		done <- outcome
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case outcome := <-done:
		if outcome.err != nil {
			return fmt.Errorf("write JSON-RPC response: %w", outcome.err)
		}
		return nil
	case <-timer.C:
		blockedPhase := "write"
		if stage.Load() == responseStageFlush {
			blockedPhase = "flush"
		}
		emit("response_write_timeout", map[string]any{
			"blocked_phase":  blockedPhase,
			"timeout_millis": timeout.Milliseconds(),
		})
		if closer, ok := server.output.(io.Closer); ok {
			_ = closer.Close()
		}
		return fmt.Errorf("write JSON-RPC response timed out after %s during %s", timeout, blockedPhase)
	}
}
