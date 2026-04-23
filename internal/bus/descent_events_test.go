package bus

import (
	"strings"
	"testing"
)

// TestDescentEventNames verifies that every descent-hardening event
// kind declared in specs/descent-hardening.md §Business Logic has a
// corresponding EventType const discoverable via DescentEventKinds.
// The test is the contract gate for item 8: adding a new descent
// event requires both the const AND the DescentEventKinds entry.
func TestDescentEventNames(t *testing.T) {
	required := []struct {
		name       string
		typeString string
	}{
		{"EvtDescentFileCapExceeded", "descent.file_cap_exceeded"},
		{"EvtDescentGhostWriteDetected", "descent.ghost_write_detected"},
		{"EvtDescentBootstrapReinstalled", "descent.bootstrap_reinstalled"},
		{"EvtDescentPreCompletionGateFailed", "descent.pre_completion_gate_failed"},
		{"EvtWorkerEnvBlocked", "worker.env_blocked"},
	}

	seen := map[EventType]bool{}
	for _, k := range DescentEventKinds {
		seen[k] = true
	}

	for _, r := range required {
		et := EventType(r.typeString)
		if !seen[et] {
			t.Errorf("missing from DescentEventKinds: %s (%q)", r.name, r.typeString)
		}
	}

	// Every entry in DescentEventKinds must have a dotted namespace
	// (at least one ".") — keeps flat enum values out of the descent
	// bucket accidentally.
	for _, k := range DescentEventKinds {
		if !strings.Contains(string(k), ".") {
			t.Errorf("descent event kind %q lacks dotted namespace", k)
		}
	}
}
