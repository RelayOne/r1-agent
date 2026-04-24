// correlation_wire_test.go -- S6-1 canonical-only coverage.
//
// The S1-2 30d dual-send window emitted both canonical X-R1-* and
// legacy X-Stoke-* on outbound requests. The window elapsed 2026-05-23;
// S6-1 drops legacy emission at the provider layer. These tests pin
// the canonical-only contract + regression-guard legacy absence.

package provider

import (
	"net/http/httptest"
	"testing"
)

func TestApplyStokeCorrelationHeaders_Full(t *testing.T) {
	req := httptest.NewRequest("POST", "http://x", nil)
	md := map[string]string{
		MetaSessionID: "sess-1",
		MetaAgentID:   "agent-a",
		MetaTaskID:    "task-1",
	}
	applyStokeCorrelationHeaders(req, md)

	for key, want := range map[string]string{
		"X-R1-Session-ID": "sess-1",
		"X-R1-Agent-ID":   "agent-a",
		"X-R1-Task-ID":    "task-1",
	} {
		if got := req.Header.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestApplyStokeCorrelationHeaders_OmitsEmpty(t *testing.T) {
	req := httptest.NewRequest("POST", "http://x", nil)
	applyStokeCorrelationHeaders(req, map[string]string{
		MetaSessionID: "sess-only",
	})

	if got := req.Header.Get("X-R1-Session-ID"); got != "sess-only" {
		t.Errorf("X-R1-Session-ID = %q", got)
	}
	for _, key := range []string{
		"X-R1-Agent-ID", "X-R1-Task-ID",
	} {
		if _, has := req.Header[key]; has {
			t.Errorf("%s should be absent when empty", key)
		}
	}
}

func TestApplyStokeCorrelationHeaders_EmptyMetadata_NoHeaders(t *testing.T) {
	req := httptest.NewRequest("POST", "http://x", nil)
	applyStokeCorrelationHeaders(req, map[string]string{})
	for _, key := range []string{
		"X-R1-Session-ID", "X-R1-Agent-ID", "X-R1-Task-ID",
	} {
		if _, has := req.Header[key]; has {
			t.Errorf("%s should be absent on empty metadata", key)
		}
	}
}

func TestApplyStokeCorrelationHeaders_NilRequest_NoOp(t *testing.T) {
	// Must not panic.
	applyStokeCorrelationHeaders(nil, map[string]string{
		MetaSessionID: "s",
	})
}

// TestApplyStokeCorrelationHeaders_S61_NoLegacyStokeHeaders is the
// provider-layer S6-1 regression guard: after the 30d dual-send
// window elapsed 2026-05-23, the legacy X-Stoke-* family must NOT
// appear on outbound requests.
func TestApplyStokeCorrelationHeaders_S61_NoLegacyStokeHeaders(t *testing.T) {
	req := httptest.NewRequest("POST", "http://x", nil)
	applyStokeCorrelationHeaders(req, map[string]string{
		MetaSessionID: "sess-x",
		MetaAgentID:   "agent-x",
		MetaTaskID:    "task-x",
	})

	for _, legacy := range []string{
		"X-Stoke-Session-ID",
		"X-Stoke-Agent-ID",
		"X-Stoke-Task-ID",
	} {
		if _, has := req.Header[legacy]; has {
			t.Errorf("S6-1 regression: legacy header %s must not be emitted post-cutover", legacy)
		}
		if v := req.Header.Get(legacy); v != "" {
			t.Errorf("S6-1 regression: legacy header %s present with value %q", legacy, v)
		}
	}
}
