package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/plan"
)

// TestGhostWriteRetry exercises the descent_bridge wrapper that wires
// plan.NewGhostWriteCheck into RunSpec.ExtraMidturnCheck. A fake edit
// tool call pointing at a missing file must produce a reminder note,
// and a real file write must be silent.
func TestGhostWriteRetry(t *testing.T) {
	dir := t.TempDir()
	// Missing-file tool input: edit reports success on src/a.ts which
	// doesn't exist on disk (the classic ghost-write).
	missInput, _ := json.Marshal(map[string]string{"file_path": "src/a.ts"})

	var events []plan.GhostWriteEvent
	baseCheck := plan.NewGhostWriteCheck(dir, func(evt plan.GhostWriteEvent) {
		events = append(events, evt)
	})
	// Adapter: the descent_bridge wiring goes through
	// engine.MidturnToolCall → plan.MidturnToolCall. Exercise the
	// same translation path here so the test binds to the real wire.
	adapter := func(tools []engine.MidturnToolCall, turn int) string {
		converted := make([]plan.MidturnToolCall, 0, len(tools))
		for _, tc := range tools {
			converted = append(converted, plan.MidturnToolCall{
				Name:    tc.Name,
				Input:   tc.Input,
				Result:  tc.Result,
				IsError: tc.IsError,
			})
		}
		return baseCheck(converted, turn)
	}

	// Ghost write: note must appear and event must fire.
	note := adapter([]engine.MidturnToolCall{
		{Name: "edit", Input: missInput, Result: "ok"},
	}, 0)
	if note == "" {
		t.Fatalf("expected reminder note on ghost write")
	}
	if !strings.Contains(note, "src/a.ts") || !strings.Contains(note, "missing") {
		t.Errorf("note missing required fields: %q", note)
	}
	if len(events) != 1 {
		t.Fatalf("expected one ghost_write_detected event, got %d", len(events))
	}

	// Normal tool: silent.
	events = nil
	if n := adapter([]engine.MidturnToolCall{{Name: "bash", Input: missInput, Result: "ran"}}, 0); n != "" {
		t.Errorf("bash tool triggered ghost-write false positive: %q", n)
	}
	if len(events) != 0 {
		t.Errorf("expected no event for bash, got %d", len(events))
	}
}
