// correlation_wire_test.go — S1-2 header dual-send coverage.
//
// These tests pin the contract that applyStokeCorrelationHeaders sets
// BOTH the canonical X-R1-* header family AND the legacy X-Stoke-*
// family on the outbound request during the 30-day dual-send window
// (through 2026-05-23). RelayGate accepts either and prefers
// canonical when both are present (router-core commit a1ca514).
//
// When S6-1 fires on 2026-05-23 these tests flip to "canonical only,
// legacy absent" — that is the mechanical trigger for the drop.

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
		"X-R1-Session-ID":    "sess-1",
		"X-R1-Agent-ID":      "agent-a",
		"X-R1-Task-ID":       "task-1",
		"X-Stoke-Session-ID": "sess-1",
		"X-Stoke-Agent-ID":   "agent-a",
		"X-Stoke-Task-ID":    "task-1",
	} {
		if got := req.Header.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestApplyStokeCorrelationHeaders_OmitsEmpty(t *testing.T) {
	req := httptest.NewRequest("POST", "http://x", nil)
	// Only session-id populated; agent + task must skip on BOTH
	// header families rather than emit empty strings.
	applyStokeCorrelationHeaders(req, map[string]string{
		MetaSessionID: "sess-only",
	})

	if got := req.Header.Get("X-R1-Session-ID"); got != "sess-only" {
		t.Errorf("X-R1-Session-ID = %q", got)
	}
	if got := req.Header.Get("X-Stoke-Session-ID"); got != "sess-only" {
		t.Errorf("X-Stoke-Session-ID = %q", got)
	}
	for _, key := range []string{
		"X-R1-Agent-ID", "X-R1-Task-ID",
		"X-Stoke-Agent-ID", "X-Stoke-Task-ID",
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
		"X-Stoke-Session-ID", "X-Stoke-Agent-ID", "X-Stoke-Task-ID",
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

// TestApplyStokeCorrelationHeaders_DualSendR1AndStoke is the provider-
// side mirror of correlation.TestApplyHeaders_DualSendR1AndStoke —
// both the ChatRequest.Metadata path (anthropic + openai-compat
// providers) and the context-ID path (apiclient, agentloop) must
// satisfy the S1-2 dual-send contract.
func TestApplyStokeCorrelationHeaders_DualSendR1AndStoke(t *testing.T) {
	req := httptest.NewRequest("POST", "http://x", nil)
	applyStokeCorrelationHeaders(req, map[string]string{
		MetaSessionID: "sess-dual",
		MetaAgentID:   "agent-dual",
		MetaTaskID:    "task-dual",
	})

	pairs := []struct {
		canonical string
		legacy    string
		want      string
	}{
		{"X-R1-Session-ID", "X-Stoke-Session-ID", "sess-dual"},
		{"X-R1-Agent-ID", "X-Stoke-Agent-ID", "agent-dual"},
		{"X-R1-Task-ID", "X-Stoke-Task-ID", "task-dual"},
	}
	for _, p := range pairs {
		canonicalGot := req.Header.Get(p.canonical)
		legacyGot := req.Header.Get(p.legacy)
		if canonicalGot != p.want {
			t.Errorf("canonical %s = %q, want %q", p.canonical, canonicalGot, p.want)
		}
		if legacyGot != p.want {
			t.Errorf("legacy %s = %q, want %q", p.legacy, legacyGot, p.want)
		}
		if canonicalGot != legacyGot {
			t.Errorf("dual-send values must be identical: %s=%q vs %s=%q",
				p.canonical, canonicalGot, p.legacy, legacyGot)
		}
	}
}
