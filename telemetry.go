package main

import (
	"encoding/json"
	"io"
	"os"
	"sync/atomic"
	"time"
)

type mcpTelemetry interface {
	Emit(event string, fields map[string]any)
	Close()
}

type noopMCPTelemetry struct{}

func (noopMCPTelemetry) Emit(string, map[string]any) {}
func (noopMCPTelemetry) Close()                      {}

type jsonLineMCPTelemetry struct {
	records chan map[string]any
	stop    chan struct{}
	done    chan struct{}
	stopped atomic.Bool
	dropped atomic.Uint64
}

func newJSONLineMCPTelemetry(writer io.Writer) mcpTelemetry {
	if writer == nil {
		return noopMCPTelemetry{}
	}
	telemetry := &jsonLineMCPTelemetry{
		records: make(chan map[string]any, 256),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go telemetry.run(writer)
	return telemetry
}

func (telemetry *jsonLineMCPTelemetry) Emit(event string, fields map[string]any) {
	if telemetry == nil || telemetry.stopped.Load() {
		return
	}
	record := make(map[string]any, len(fields)+4)
	record["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	record["event"] = event
	record["pid"] = os.Getpid()
	record["version"] = mcpServerVersion
	for key, value := range fields {
		record[key] = value
	}
	select {
	case telemetry.records <- record:
	default:
		telemetry.dropped.Add(1)
	}
}

func (telemetry *jsonLineMCPTelemetry) Close() {
	if telemetry == nil || !telemetry.stopped.CompareAndSwap(false, true) {
		return
	}
	close(telemetry.stop)
	select {
	case <-telemetry.done:
	case <-time.After(100 * time.Millisecond):
	}
}

func (telemetry *jsonLineMCPTelemetry) run(writer io.Writer) {
	defer close(telemetry.done)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	writeRecord := func(record map[string]any) {
		if dropped := telemetry.dropped.Swap(0); dropped != 0 {
			record["telemetry_dropped"] = dropped
		}
		// Diagnostics must never be written to stdout. A blocked stderr only
		// stalls this background goroutine; the bounded producer drops events.
		_ = encoder.Encode(record)
	}

	for {
		select {
		case record := <-telemetry.records:
			writeRecord(record)
		case <-telemetry.stop:
			for {
				select {
				case record := <-telemetry.records:
					writeRecord(record)
				default:
					return
				}
			}
		}
	}
}

type mcpRequestTrace struct {
	Sequence    uint64
	RequestID   string
	Method      string
	Tool        string
	Started     time.Time
	ParamsBytes int
	ArgsBytes   int
	Operations  int
}

func (server *mcpServer) telemetryFields(extra map[string]any) map[string]any {
	size := len(extra) + 7
	fields := make(map[string]any, size)
	if server != nil && server.trace != nil {
		fields["request_seq"] = server.trace.Sequence
		fields["request_id"] = server.trace.RequestID
		fields["method"] = server.trace.Method
		if server.trace.Tool != "" {
			fields["tool"] = server.trace.Tool
		}
		if server.trace.ParamsBytes != 0 {
			fields["params_bytes"] = server.trace.ParamsBytes
		}
		if server.trace.ArgsBytes != 0 {
			fields["args_bytes"] = server.trace.ArgsBytes
		}
		if server.trace.Operations != 0 {
			fields["operation_count"] = server.trace.Operations
		}
	}
	for key, value := range extra {
		fields[key] = value
	}
	return fields
}

func (server *mcpServer) emitTelemetry(event string, fields map[string]any) {
	if server == nil || server.telemetry == nil {
		return
	}
	server.telemetry.Emit(event, server.telemetryFields(fields))
}

func durationMilliseconds(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}

func hunkTelemetryFields(hunks []Hunk) map[string]any {
	removed, added := 0, 0
	for _, hunk := range hunks {
		removed += len(hunk.Removed)
		added += len(hunk.Added)
	}
	return map[string]any{
		"hunk_count":    len(hunks),
		"removed_lines": removed,
		"added_lines":   added,
	}
}
