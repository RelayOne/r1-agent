package hitl

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/streamjson"
)

// TestHITLWireFormat_OutboundRequest_LocksShape freezes the exact keys
// of the hitl_required line emitted on the critical lane. CloudSwarm's
// Temporal-signal parser keys off these field names; if any rename
// breaks this test, it's a coordinated cross-repo change.
//
// Field contract (per RT-CLOUDSWARM-MAP §3 + hitl.go:101-113):
//   - top-level `type` = "hitl_required"
//   - top-level `session_id` present (set by streamjson)
//   - top-level `uuid` present (set by streamjson)
//   - nested `reason` + `approval_type` strings
//   - optional `file` string (present only when Request.File non-empty)
//   - optional `_stoke.dev/context` map (present only when Request.Context non-empty)
func TestHITLWireFormat_OutboundRequest_LocksShape(t *testing.T) {
	var out bytes.Buffer
	tl := streamjson.NewTwoLane(&out, true)
	svc := New(tl, nil, 10*time.Millisecond)

	go svc.RequestApproval(context.Background(), Request{
		Reason:       "ac_softpass_descent",
		ApprovalType: "soft_pass",
		File:         "foo/bar.go",
		Context:      map[string]any{"tier": "T3"},
	})
	// Let the emit happen; the request will time out a moment later.
	time.Sleep(50 * time.Millisecond)

	line := firstLine(out.Bytes())
	if len(line) == 0 {
		t.Fatal("no hitl_required line emitted")
	}
	var got map[string]any
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("decode: %v\nline: %s", err, line)
	}
	if got["type"] != "hitl_required" {
		t.Errorf("type = %v, want hitl_required", got["type"])
	}
	if _, ok := got["session_id"].(string); !ok {
		t.Errorf("session_id missing or not string: %v", got["session_id"])
	}
	if _, ok := got["uuid"].(string); !ok {
		t.Errorf("uuid missing or not string: %v", got["uuid"])
	}
	if got["reason"] != "ac_softpass_descent" {
		t.Errorf("reason = %v", got["reason"])
	}
	if got["approval_type"] != "soft_pass" {
		t.Errorf("approval_type = %v", got["approval_type"])
	}
	if got["file"] != "foo/bar.go" {
		t.Errorf("file = %v", got["file"])
	}
	if ctx, ok := got["_stoke.dev/context"].(map[string]any); !ok || ctx["tier"] != "T3" {
		t.Errorf("_stoke.dev/context tier missing: %v", got["_stoke.dev/context"])
	}
}

// TestHITLWireFormat_OutboundRequest_OmitsEmptyOptionals asserts that
// File and Context are omitted when empty (not emitted as "" / null).
// Matches the explicit `if req.File != ""` guards in hitl.go:107-112.
func TestHITLWireFormat_OutboundRequest_OmitsEmptyOptionals(t *testing.T) {
	var out bytes.Buffer
	tl := streamjson.NewTwoLane(&out, true)
	svc := New(tl, nil, 10*time.Millisecond)

	go svc.RequestApproval(context.Background(), Request{
		Reason:       "x",
		ApprovalType: "soft_pass",
	})
	time.Sleep(50 * time.Millisecond)

	line := firstLine(out.Bytes())
	var got map[string]any
	_ = json.Unmarshal(line, &got)

	if _, has := got["file"]; has {
		t.Errorf("file should be omitted when empty; got %v", got["file"])
	}
	if _, has := got["_stoke.dev/context"]; has {
		t.Errorf("_stoke.dev/context should be omitted when empty")
	}
}

// TestHITLWireFormat_InboundDecision_LocksShape freezes the field
// names Decision marshals from (hitl.go:49-53). If CloudSwarm ever
// sends {"decision":true,"reason":"...","decided_by":"..."} and the
// Go field renames, this test catches it.
func TestHITLWireFormat_InboundDecision_LocksShape(t *testing.T) {
	wire := []byte(`{"decision":true,"reason":"operator_approved","decided_by":"operator@acme"}`)
	var d Decision
	if err := json.Unmarshal(wire, &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !d.Approved {
		t.Errorf("Approved = false (want true) — 'decision' field may have been renamed")
	}
	if d.Reason != "operator_approved" {
		t.Errorf("Reason = %q", d.Reason)
	}
	if d.DecidedBy != "operator@acme" {
		t.Errorf("DecidedBy = %q", d.DecidedBy)
	}
}

// TestHITLWireFormat_InboundDecision_FalseDecision freezes the "no"
// path: decision:false is parsed as Approved=false.
func TestHITLWireFormat_InboundDecision_FalseDecision(t *testing.T) {
	wire := []byte(`{"decision":false,"reason":"denied","decided_by":"op"}`)
	var d Decision
	_ = json.Unmarshal(wire, &d)
	if d.Approved {
		t.Error("decision:false should produce Approved=false")
	}
	if d.Reason != "denied" {
		t.Errorf("Reason = %q", d.Reason)
	}
}

// firstLine returns the first newline-terminated line in buf, with the
// terminator stripped. Returns nil if none present.
func firstLine(buf []byte) []byte {
	idx := bytes.IndexByte(buf, '\n')
	if idx < 0 {
		return nil
	}
	return bytes.TrimRight(buf[:idx], "\r\n")
}

// Silence "unused import strings" when the test is trimmed — strings
// stays as a future-proofing import for anyone who adds substring
// assertions. Removing when no longer needed is a 1-line change.
var _ = strings.Contains
