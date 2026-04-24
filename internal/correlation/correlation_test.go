package correlation

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestWithIDs_Roundtrip(t *testing.T) {
	ctx := context.Background()
	ctx = WithIDs(ctx, IDs{SessionID: "s1", AgentID: "a1", TaskID: "t1"})
	got := FromContext(ctx)
	if got.SessionID != "s1" || got.AgentID != "a1" || got.TaskID != "t1" {
		t.Fatalf("roundtrip lost fields: %+v", got)
	}
}

func TestFromContext_Empty(t *testing.T) {
	got := FromContext(context.Background())
	if got != (IDs{}) {
		t.Fatalf("expected zero IDs, got %+v", got)
	}
}

func TestFromContext_NilCtx(t *testing.T) {
	got := FromContext(nil)
	if got != (IDs{}) {
		t.Fatalf("nil ctx should yield zero IDs, got %+v", got)
	}
}

func TestWithIDs_AllEmpty_NoAllocation(t *testing.T) {
	base := context.Background()
	got := WithIDs(base, IDs{})
	if got != base {
		t.Error("WithIDs with zero IDs should return ctx unchanged")
	}
}

func TestApplyHeaders_Full(t *testing.T) {
	req := httptest.NewRequest("POST", "http://x", nil)
	ctx := WithIDs(context.Background(), IDs{
		SessionID: "sess-1", AgentID: "agent-a", TaskID: "task-1",
	})
	ApplyHeaders(ctx, req)
	// S1-2 dual-send: both canonical X-R1-* AND legacy X-Stoke-*
	// must fire with identical values for every non-empty ID.
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

func TestApplyHeaders_OmitsEmpty(t *testing.T) {
	req := httptest.NewRequest("POST", "http://x", nil)
	ctx := WithIDs(context.Background(), IDs{SessionID: "sess-only"})
	ApplyHeaders(ctx, req)

	// S1-2 dual-send: non-empty SessionID sets BOTH families;
	// empty Agent/Task fields skip on BOTH families.
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
			t.Errorf("%s should be absent when empty (not present as empty string)", key)
		}
	}
}

func TestApplyHeaders_NoIDs_NoHeaders(t *testing.T) {
	req := httptest.NewRequest("POST", "http://x", nil)
	ApplyHeaders(context.Background(), req)
	// Neither canonical nor legacy family fires when ctx has zero IDs.
	for _, key := range []string{
		"X-R1-Session-ID", "X-R1-Agent-ID", "X-R1-Task-ID",
		"X-Stoke-Session-ID", "X-Stoke-Agent-ID", "X-Stoke-Task-ID",
	} {
		if _, has := req.Header[key]; has {
			t.Errorf("%s should be absent on empty ctx", key)
		}
	}
}

func TestApplyHeaders_NilRequest_NoOp(t *testing.T) {
	// Must not panic.
	ctx := WithIDs(context.Background(), IDs{SessionID: "s"})
	ApplyHeaders(ctx, nil)
}

// TestApplyHeaders_DualSendR1AndStoke asserts the S1-2 contract
// explicitly: the canonical X-R1-* family and the legacy X-Stoke-*
// family both fire on the same outbound request with IDENTICAL
// values. This is the 30-day dual-send window (through 2026-05-23)
// that unblocks RelayGate's dual-accept ingress (router-core
// commit a1ca514). After the window closes the legacy X-Stoke-*
// emission is dropped per S6-1; flipping this test at that time is
// the mechanical trigger for the drop.
func TestApplyHeaders_DualSendR1AndStoke(t *testing.T) {
	req := httptest.NewRequest("POST", "http://x", nil)
	ctx := WithIDs(context.Background(), IDs{
		SessionID: "sess-dual",
		AgentID:   "agent-dual",
		TaskID:    "task-dual",
	})
	ApplyHeaders(ctx, req)

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
