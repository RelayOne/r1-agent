package desktopapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/RelayOne/r1-agent/internal/stokerr"
)

// TestErrNotImplementedShape verifies the sentinel error carries the
// right stokerr code and message shape so the JSON-RPC layer can
// translate it into -32010 + data.stoke_code="not_implemented" per
// desktop/IPC-CONTRACT.md §3.2.
func TestErrNotImplementedShape(t *testing.T) {
	t.Parallel()

	var se *stokerr.Error
	if !errors.As(ErrNotImplemented, &se) {
		t.Fatalf("ErrNotImplemented is not a *stokerr.Error (got %T)", ErrNotImplemented)
	}
	if got, want := string(se.Code), "not_implemented"; got != want {
		t.Errorf("code = %q, want %q", got, want)
	}
	if se.Message == "" {
		t.Error("message must be non-empty for operator diagnosis")
	}
}

// TestIsNotImplemented_Direct confirms the sentinel round-trips through
// both errors.Is and the IsNotImplemented helper.
func TestIsNotImplemented_Direct(t *testing.T) {
	t.Parallel()

	if !errors.Is(ErrNotImplemented, ErrNotImplemented) {
		t.Error("errors.Is reflexive check failed")
	}
	if !IsNotImplemented(ErrNotImplemented) {
		t.Error("IsNotImplemented(sentinel) must be true")
	}
}

// TestIsNotImplemented_Wrapped confirms wrapped errors still match.
// Implementations that ever add context (fmt.Errorf("...: %w", err))
// must not break the sentinel check.
func TestIsNotImplemented_Wrapped(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("session.start dispatch: %w", ErrNotImplemented)
	if !IsNotImplemented(wrapped) {
		t.Error("IsNotImplemented must see through fmt.Errorf wrap")
	}
	var se *stokerr.Error
	if !errors.As(wrapped, &se) {
		t.Fatal("errors.As must unwrap through to *stokerr.Error")
	}
	if string(se.Code) != "not_implemented" {
		t.Errorf("unwrapped code = %q, want not_implemented", se.Code)
	}
}

// TestIsNotImplemented_OtherError confirms the helper rejects
// unrelated errors — a different stokerr code must not match, and a
// plain error must not match.
func TestIsNotImplemented_OtherError(t *testing.T) {
	t.Parallel()

	if IsNotImplemented(errors.New("unrelated")) {
		t.Error("plain error must not match sentinel")
	}
	if IsNotImplemented(stokerr.Validationf("bad input")) {
		t.Error("different stokerr code must not match sentinel")
	}
	if IsNotImplemented(nil) {
		t.Error("nil must not match sentinel")
	}
}

// TestNotImplemented_SatisfiesHandler is a compile-time + runtime
// check that every Handler method exists on NotImplemented. The
// compile-time assertion in desktopapi.go (`var _ Handler =
// (*NotImplemented)(nil)`) already enforces the shape; this test
// additionally verifies runtime dispatch works for each method.
func TestNotImplemented_SatisfiesHandler(t *testing.T) {
	t.Parallel()

	var h Handler = NotImplemented{}
	ctx := context.Background()

	// Each closure calls one Handler method; the shared assertion
	// block below runs against every error it returns.
	cases := []struct {
		name string
		call func() error
	}{
		{"SessionStart", func() error {
			_, err := h.SessionStart(ctx, SessionStartRequest{Prompt: "hi"})
			return err
		}},
		{"SessionPause", func() error {
			_, err := h.SessionPause(ctx, SessionIDRequest{SessionID: "s1"})
			return err
		}},
		{"SessionResume", func() error {
			_, err := h.SessionResume(ctx, SessionIDRequest{SessionID: "s1"})
			return err
		}},
		{"LedgerGetNode", func() error {
			_, err := h.LedgerGetNode(ctx, LedgerGetNodeRequest{Hash: "abc"})
			return err
		}},
		{"LedgerListEvents", func() error {
			_, err := h.LedgerListEvents(ctx, LedgerListEventsRequest{Limit: 10})
			return err
		}},
		{"MemoryListScopes", func() error {
			_, err := h.MemoryListScopes(ctx)
			return err
		}},
		{"MemoryQuery", func() error {
			_, err := h.MemoryQuery(ctx, MemoryQueryRequest{Scope: MemoryScopeSession})
			return err
		}},
		{"CostGetCurrent", func() error {
			_, err := h.CostGetCurrent(ctx, CostGetCurrentRequest{})
			return err
		}},
		{"CostGetHistory", func() error {
			_, err := h.CostGetHistory(ctx, CostGetHistoryRequest{})
			return err
		}},
		{"DescentCurrentTier", func() error {
			_, err := h.DescentCurrentTier(ctx, DescentCurrentTierRequest{SessionID: "s1"})
			return err
		}},
		{"DescentTierHistory", func() error {
			_, err := h.DescentTierHistory(ctx, DescentTierHistoryRequest{SessionID: "s1", ACID: "ac1"})
			return err
		}},
	}

	// The surface is the 11 subprocess-bound methods (IPC-CONTRACT.md
	// §2 summary). Lock that count here so a future signature change
	// trips a test, not a silent drift.
	if got, want := len(cases), 11; got != want {
		t.Fatalf("handler surface method count = %d, want %d (see desktop/IPC-CONTRACT.md §2.6)", got, want)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatalf("%s: stub returned nil, expected ErrNotImplemented", tc.name)
			}
			if !IsNotImplemented(err) {
				t.Errorf("%s: err = %v, want ErrNotImplemented", tc.name, err)
			}
		})
	}
}

// TestAllMemoryScopes locks the five-scope contract: the list, order,
// and exact string values.
func TestAllMemoryScopes(t *testing.T) {
	t.Parallel()

	got := AllMemoryScopes()
	want := []MemoryScope{
		MemoryScopeSession,
		MemoryScopeWorker,
		MemoryScopeAllSessions,
		MemoryScopeGlobal,
		MemoryScopeAlways,
	}
	if len(got) != len(want) {
		t.Fatalf("scope count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("scope[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Stable JSON form — the WebView pattern-matches on these strings.
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wantJSON := `["Session","Worker","AllSessions","Global","Always"]`
	if string(gotJSON) != wantJSON {
		t.Errorf("scope JSON = %s, want %s", gotJSON, wantJSON)
	}
}

// TestAllDescentTiers locks the eight-tier contract (T1..T8, in order).
func TestAllDescentTiers(t *testing.T) {
	t.Parallel()

	got := AllDescentTiers()
	if len(got) != 8 {
		t.Fatalf("tier count = %d, want 8", len(got))
	}
	for i, tier := range got {
		want := fmt.Sprintf("T%d", i+1)
		if string(tier) != want {
			t.Errorf("tier[%d] = %q, want %q", i, tier, want)
		}
	}
}

// TestDescentStatusValues pins the four canonical status strings.
func TestDescentStatusValues(t *testing.T) {
	t.Parallel()

	cases := map[DescentStatus]string{
		StatusPending: "pending",
		StatusRunning: "running",
		StatusPassed:  "passed",
		StatusFailed:  "failed",
	}
	for status, want := range cases {
		if string(status) != want {
			t.Errorf("%v = %q, want %q", status, string(status), want)
		}
	}
}

// TestRequestResponse_JSONShape spot-checks a few JSON encodings to
// guard against rename drift between the Go types and the contract
// doc / Rust structs. If a JSON tag changes, this test fails loudly.
func TestRequestResponse_JSONShape(t *testing.T) {
	t.Parallel()

	t.Run("SessionStartRequest omits empty optional fields", func(t *testing.T) {
		req := SessionStartRequest{Prompt: "hello"}
		out, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if got, want := string(out), `{"prompt":"hello"}`; got != want {
			t.Errorf("json = %s, want %s", got, want)
		}
	})

	t.Run("LedgerNode uses 'type' wire name", func(t *testing.T) {
		node := LedgerNode{Hash: "h", NodeType: "task", Payload: map[string]any{}, Edges: []LedgerEdge{}}
		out, err := json.Marshal(node)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		// Confirm the NodeType field serialises as "type", not "node_type" or "NodeType".
		var decoded map[string]any
		if err := json.Unmarshal(out, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, ok := decoded["type"]; !ok {
			t.Errorf("expected wire field 'type', got %v", decoded)
		}
		if _, ok := decoded["node_type"]; ok {
			t.Errorf("unexpected wire field 'node_type' — contract uses 'type'")
		}
	})

	t.Run("DescentTierRow serialises tier and status as strings", func(t *testing.T) {
		row := DescentTierRow{ACID: "ac1", Tier: TierT3, Status: StatusRunning}
		out, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(out, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded["tier"] != "T3" {
			t.Errorf("tier = %v, want T3", decoded["tier"])
		}
		if decoded["status"] != "running" {
			t.Errorf("status = %v, want running", decoded["status"])
		}
	})
}
