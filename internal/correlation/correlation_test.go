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
	// S6-1 (2026-05-23): canonical X-R1-* only. Legacy X-Stoke-*
	// emission dropped after the 30d S1-2 dual-send window elapsed.
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

func TestApplyHeaders_OmitsEmpty(t *testing.T) {
	req := httptest.NewRequest("POST", "http://x", nil)
	ctx := WithIDs(context.Background(), IDs{SessionID: "sess-only"})
	ApplyHeaders(ctx, req)

	if got := req.Header.Get("X-R1-Session-ID"); got != "sess-only" {
		t.Errorf("X-R1-Session-ID = %q", got)
	}
	for _, key := range []string{
		"X-R1-Agent-ID", "X-R1-Task-ID",
	} {
		if _, has := req.Header[key]; has {
			t.Errorf("%s should be absent when empty (not present as empty string)", key)
		}
	}
}

func TestApplyHeaders_NoIDs_NoHeaders(t *testing.T) {
	req := httptest.NewRequest("POST", "http://x", nil)
	ApplyHeaders(context.Background(), req)
	for _, key := range []string{
		"X-R1-Session-ID", "X-R1-Agent-ID", "X-R1-Task-ID",
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

// TestApplyHeaders_S61_NoLegacyStokeHeaders is the S6-1 regression
// guard: after the 30d dual-send window elapsed 2026-05-23, the
// legacy X-Stoke-* family must NOT appear on outbound requests. This
// test fails loudly if anyone re-adds legacy emission by mistake.
func TestApplyHeaders_S61_NoLegacyStokeHeaders(t *testing.T) {
	req := httptest.NewRequest("POST", "http://x", nil)
	ctx := WithIDs(context.Background(), IDs{
		SessionID: "sess-x",
		AgentID:   "agent-x",
		TaskID:    "task-x",
	})
	ApplyHeaders(ctx, req)

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
