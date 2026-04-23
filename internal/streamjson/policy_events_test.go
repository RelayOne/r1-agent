package streamjson

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEmitPolicyCheck_PopulatesFourKeys(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf, true)

	e.EmitPolicyCheck("Allow", 7, 2, "cedar-http")

	raw := strings.TrimSpace(buf.String())
	var evt map[string]any
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		t.Fatalf("unmarshal: %v (raw=%q)", err, raw)
	}
	if evt["type"] != "system" {
		t.Errorf("type=%v, want system", evt["type"])
	}
	if evt["subtype"] != "policy.check" {
		t.Errorf("subtype=%v, want policy.check", evt["subtype"])
	}
	if evt["_stoke.dev/policy.decision"] != "Allow" {
		t.Errorf("policy.decision=%v, want Allow", evt["_stoke.dev/policy.decision"])
	}
	// JSON numbers come back as float64.
	if got, ok := evt["_stoke.dev/policy.latency_ms"].(float64); !ok || got != 7 {
		t.Errorf("policy.latency_ms=%v, want 7", evt["_stoke.dev/policy.latency_ms"])
	}
	if got, ok := evt["_stoke.dev/policy.reasons_count"].(float64); !ok || got != 2 {
		t.Errorf("policy.reasons_count=%v, want 2", evt["_stoke.dev/policy.reasons_count"])
	}
	if evt["_stoke.dev/policy.backend"] != "cedar-http" {
		t.Errorf("policy.backend=%v, want cedar-http", evt["_stoke.dev/policy.backend"])
	}
	// Claude-Code-compat required fields.
	for _, key := range []string{"uuid", "session_id"} {
		if evt[key] == nil || evt[key] == "" {
			t.Errorf("%s missing from policy.check event", key)
		}
	}
}

func TestEmitPolicyDenied_PopulatesPARCKeys(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf, true)

	reasons := []string{"rule:forbid-prod-writes", "default-deny"}
	e.EmitPolicyDenied(reasons, "Stoke::sess-abc", "file_write", "/etc/passwd")

	raw := strings.TrimSpace(buf.String())
	var evt map[string]any
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		t.Fatalf("unmarshal: %v (raw=%q)", err, raw)
	}
	if evt["type"] != "system" {
		t.Errorf("type=%v, want system", evt["type"])
	}
	if evt["subtype"] != "policy.denied" {
		t.Errorf("subtype=%v, want policy.denied", evt["subtype"])
	}
	gotReasons, ok := evt["_stoke.dev/policy.reasons"].([]any)
	if !ok {
		t.Fatalf("policy.reasons is not a JSON array: %T %v", evt["_stoke.dev/policy.reasons"], evt["_stoke.dev/policy.reasons"])
	}
	if len(gotReasons) != 2 || gotReasons[0] != "rule:forbid-prod-writes" || gotReasons[1] != "default-deny" {
		t.Errorf("policy.reasons=%v, want [rule:forbid-prod-writes default-deny]", gotReasons)
	}
	if evt["_stoke.dev/policy.principal"] != "Stoke::sess-abc" {
		t.Errorf("policy.principal=%v, want Stoke::sess-abc", evt["_stoke.dev/policy.principal"])
	}
	if evt["_stoke.dev/policy.action"] != "file_write" {
		t.Errorf("policy.action=%v, want file_write", evt["_stoke.dev/policy.action"])
	}
	if evt["_stoke.dev/policy.resource"] != "/etc/passwd" {
		t.Errorf("policy.resource=%v, want /etc/passwd", evt["_stoke.dev/policy.resource"])
	}
	for _, key := range []string{"uuid", "session_id"} {
		if evt[key] == nil || evt[key] == "" {
			t.Errorf("%s missing from policy.denied event", key)
		}
	}
}

func TestEmitPolicy_DisabledIsNoop(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf, false)

	e.EmitPolicyCheck("Allow", 4, 1, "null")
	e.EmitPolicyDenied([]string{"x"}, "p", "a", "r")

	if buf.Len() != 0 {
		t.Errorf("disabled emitter wrote %d bytes; want 0 (content=%q)", buf.Len(), buf.String())
	}
}
