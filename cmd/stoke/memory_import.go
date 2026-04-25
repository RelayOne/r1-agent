// memory_import.go — CS-4 --import-memory flag support.
//
// Loads a JSON snapshot of memory rows into the membus.Bus before the
// main SOW loop starts. CloudSwarm's supervisor uses this to preseed
// per-task memory across sessions.
//
// Snapshot format (matches the ExportDelta output shape on the other
// side — import + export form a symmetric pair):
//
//	[
//	  {
//	    "scope":        "session",        // required — one of the membus.Scope values
//	    "scope_target": "s1",             // optional; empty allowed
//	    "key":          "k1",             // required; non-empty
//	    "content":      "...",            // required; the payload
//	    "memory_type":  "ephemeral",      // optional; defaults to ephemeral
//	    "session_id":   "sess-xyz",       // optional
//	    "author":       "cloudswarm",     // optional
//	    "tags":         ["k8s"],          // optional
//	    "metadata":     {"foo":"bar"}     // optional
//	  },
//	  ...
//	]
//
// Spec reference: specs/work-stoke-alignment.md CS-4.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/RelayOne/r1-agent/internal/memory/membus"
)

// importedMemoryRow is the wire format for one row in the snapshot.
// Only Scope / Key / Content are required. Every other field maps
// through to membus.RememberRequest.
type importedMemoryRow struct {
	Scope       string            `json:"scope"`
	ScopeTarget string            `json:"scope_target,omitempty"`
	Key         string            `json:"key"`
	Content     string            `json:"content"`
	MemoryType  string            `json:"memory_type,omitempty"`
	SessionID   string            `json:"session_id,omitempty"`
	StepID      string            `json:"step_id,omitempty"`
	TaskID      string            `json:"task_id,omitempty"`
	Author      string            `json:"author,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// importMemoryFromFile reads a JSON array from path and calls
// bus.Remember for each row. Failures on individual rows abort the
// import — partial imports are worse than no import because operators
// can't easily reason about which rows landed vs didn't.
//
// Returns the number of rows imported on success. On failure the
// count reflects rows committed before the failure (the bus's writer
// goroutine may have durably written them).
func importMemoryFromFile(ctx context.Context, bus *membus.Bus, path string) (int, error) {
	if bus == nil {
		return 0, fmt.Errorf("memory_import: nil bus")
	}
	if path == "" {
		return 0, fmt.Errorf("memory_import: empty path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("memory_import: read %s: %w", path, err)
	}
	var rows []importedMemoryRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return 0, fmt.Errorf("memory_import: parse %s: %w", path, err)
	}

	imported := 0
	for i, r := range rows {
		req := membus.RememberRequest{
			Scope:       membus.Scope(r.Scope),
			ScopeTarget: r.ScopeTarget,
			Key:         r.Key,
			Content:     r.Content,
			SessionID:   r.SessionID,
			StepID:      r.StepID,
			TaskID:      r.TaskID,
			Author:      r.Author,
			Tags:        r.Tags,
			Metadata:    r.Metadata,
		}
		// MemoryType is retained in the snapshot for round-trip symmetry
		// with ExportDelta output, but RememberRequest exposes no explicit
		// field for it — the bus derives type from scope at flush time.
		// See internal/memory/membus/bus.go:106-118. Accepting the value
		// from JSON and ignoring it keeps older snapshots importable.
		_ = r.MemoryType

		if err := bus.Remember(ctx, req); err != nil {
			return imported, fmt.Errorf("memory_import: row %d (key=%q): %w", i, r.Key, err)
		}
		imported++
	}
	return imported, nil
}
