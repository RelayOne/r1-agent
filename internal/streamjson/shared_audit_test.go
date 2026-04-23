package streamjson

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEmitSharedAudit_AllFieldsPresent(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf, true)

	e.EmitSharedAudit(SharedAuditEvent{
		Type:      "stoke.request.start",
		SessionID: "sess-1",
		AgentID:   "agent-a",
		TaskID:    "task-1",
		Payload:   json.RawMessage(`{"note":"hi"}`),
	})

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("no event emitted")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, k := range []string{"id", "ts", "type", "session_id", "agent_id", "task_id", "payload", "severity"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing field %q in: %s", k, line)
		}
	}
}

func TestEmitSharedAudit_DefaultsApplied(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf, true)

	e.EmitSharedAudit(SharedAuditEvent{Type: "stoke.ac.result"})

	var got map[string]any
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got)

	if id, _ := got["id"].(string); id == "" {
		t.Error("ID should be auto-minted")
	}
	if ts, _ := got["ts"].(string); ts == "" {
		t.Error("Timestamp should be auto-defaulted")
	}
	if sev, _ := got["severity"].(string); sev != "info" {
		t.Errorf("Severity default = %q, want info", sev)
	}
}

func TestEmitSharedAudit_TraceParentOmittedWhenEmpty(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf, true)

	e.EmitSharedAudit(SharedAuditEvent{Type: "stoke.descent.tier"})
	if bytes.Contains(buf.Bytes(), []byte("trace_parent")) {
		t.Errorf("trace_parent should be omitted when empty; got: %s", buf.String())
	}

	buf.Reset()
	e.EmitSharedAudit(SharedAuditEvent{Type: "stoke.descent.tier", TraceParent: "00-abc-def-01"})
	if !bytes.Contains(buf.Bytes(), []byte(`"trace_parent":"00-abc-def-01"`)) {
		t.Errorf("trace_parent should be emitted when non-empty; got: %s", buf.String())
	}
}

func TestEmitSharedAudit_DisabledEmitterNoOp(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf, false)
	e.EmitSharedAudit(SharedAuditEvent{Type: "stoke.x"})
	if buf.Len() != 0 {
		t.Errorf("disabled emitter wrote %d bytes; want 0", buf.Len())
	}
}
