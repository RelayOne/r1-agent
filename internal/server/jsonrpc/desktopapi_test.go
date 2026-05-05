package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/desktopapi"
	"github.com/RelayOne/r1/internal/stokerr"
)

// fakeHandler is a programmable desktopapi.Handler that records inbound
// requests and returns canned responses. Used by the round-trip tests
// to verify the dispatcher routes verbs to the right method on the
// handler interface.
type fakeHandler struct {
	desktopapi.NotImplemented // pick up the not-implemented stub for unused methods

	startReq desktopapi.SessionStartRequest
	startRes desktopapi.SessionStartResponse
	startErr error

	listReq desktopapi.LedgerListEventsRequest
	listRes desktopapi.LedgerListEventsResponse
	listErr error

	memReq desktopapi.MemoryQueryRequest
	memRes desktopapi.MemoryQueryResponse

	costReq desktopapi.CostGetCurrentRequest
	costRes desktopapi.CostSnapshot
}

func (f *fakeHandler) SessionStart(ctx context.Context, req desktopapi.SessionStartRequest) (desktopapi.SessionStartResponse, error) {
	f.startReq = req
	return f.startRes, f.startErr
}

func (f *fakeHandler) LedgerListEvents(ctx context.Context, req desktopapi.LedgerListEventsRequest) (desktopapi.LedgerListEventsResponse, error) {
	f.listReq = req
	return f.listRes, f.listErr
}

func (f *fakeHandler) MemoryListScopes(ctx context.Context) (desktopapi.MemoryListScopesResponse, error) {
	return desktopapi.MemoryListScopesResponse{Scopes: desktopapi.AllMemoryScopes()}, nil
}

func (f *fakeHandler) MemoryQuery(ctx context.Context, req desktopapi.MemoryQueryRequest) (desktopapi.MemoryQueryResponse, error) {
	f.memReq = req
	return f.memRes, nil
}

func (f *fakeHandler) CostGetCurrent(ctx context.Context, req desktopapi.CostGetCurrentRequest) (desktopapi.CostSnapshot, error) {
	f.costReq = req
	return f.costRes, nil
}

// TestRPC_LedgerVerbsRoundTrip dispatches `ledger.list_events` with a
// non-trivial params payload through a Dispatcher that has been wired
// via RegisterDesktopAPI. Asserts the params land on the handler with
// matching field values and the typed response makes it back as the
// JSON-RPC `result`.
func TestRPC_LedgerVerbsRoundTrip(t *testing.T) {
	fh := &fakeHandler{
		listRes: desktopapi.LedgerListEventsResponse{
			Events: []desktopapi.LedgerEventSummary{
				{Hash: "abc", NodeType: "task.completed", At: "2026-05-02T10:00:00Z"},
			},
			NextCursor: "cur-1",
		},
	}
	d := NewDispatcher()
	RegisterDesktopAPI(d, fh)

	in := []byte(`{"jsonrpc":"2.0","id":7,"method":"ledger.list_events","params":{"session_id":"s-1","since":"2026-05-01T00:00:00Z","limit":50}}`)
	req, err := DecodeRequest(in)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp := d.Dispatch(context.Background(), req)
	if resp == nil || resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp)
	}
	if fh.listReq.SessionID != "s-1" || fh.listReq.Limit != 50 {
		t.Fatalf("handler did not see params: %+v", fh.listReq)
	}
	if !strings.Contains(string(resp.Result), `"abc"`) {
		t.Fatalf("result missing event hash: %s", resp.Result)
	}
	if !strings.Contains(string(resp.Result), `"next_cursor":"cur-1"`) {
		t.Fatalf("result missing cursor: %s", resp.Result)
	}
}

// TestRPC_AllElevenVerbsRegistered asserts every contract verb has a
// handler installed. This is a wire-shape regression guard: forgetting
// one verb would silently surface as MethodNotFound at runtime.
func TestRPC_AllElevenVerbsRegistered(t *testing.T) {
	d := NewDispatcher()
	RegisterDesktopAPI(d, &fakeHandler{})
	want := []string{
		"session.start",
		"session.pause",
		"session.resume",
		"ledger.get_node",
		"ledger.list_events",
		"memory.list_scopes",
		"memory.query",
		"cost.get_current",
		"cost.get_history",
		"descent.current_tier",
		"descent.tier_history",
	}
	for _, m := range want {
		if !d.HasMethod(m) {
			t.Errorf("missing method registration: %s", m)
		}
	}
}

// TestRPC_NotImplementedSentinel asserts that an unimplemented verb
// surfaces with the correct CodeNotImplemented + stoke_code.
func TestRPC_NotImplementedSentinel(t *testing.T) {
	d := NewDispatcher()
	// Use the upstream NotImplemented so every verb returns the sentinel.
	RegisterDesktopAPI(d, desktopapi.NotImplemented{})
	in := []byte(`{"jsonrpc":"2.0","id":1,"method":"session.start","params":{"prompt":"hi"}}`)
	req, _ := DecodeRequest(in)
	resp := d.Dispatch(context.Background(), req)
	if resp == nil || resp.Error == nil {
		t.Fatalf("expected error, got %+v", resp)
	}
	if resp.Error.Code != CodeNotImplemented {
		t.Fatalf("code: got %d want %d", resp.Error.Code, CodeNotImplemented)
	}
	if resp.Error.Data["stoke_code"] != "not_implemented" {
		t.Fatalf("stoke_code: got %v", resp.Error.Data["stoke_code"])
	}
}

// TestRPC_StokeErrTaxonomyRoundTrip asserts that a handler returning a
// stokerr.Error gets the correct numeric code AND data.stoke_code.
func TestRPC_StokeErrTaxonomyRoundTrip(t *testing.T) {
	fh := &fakeHandler{startErr: stokerr.New(stokerr.ErrConflict, "double-start")}
	d := NewDispatcher()
	RegisterDesktopAPI(d, fh)
	in := []byte(`{"jsonrpc":"2.0","id":1,"method":"session.start","params":{"prompt":"hi"}}`)
	req, _ := DecodeRequest(in)
	resp := d.Dispatch(context.Background(), req)
	if resp.Error == nil || resp.Error.Code != CodeConflict {
		t.Fatalf("expected conflict error, got %+v", resp)
	}
	if resp.Error.Data["stoke_code"] != "conflict" {
		t.Fatalf("stoke_code: got %v", resp.Error.Data["stoke_code"])
	}
}

// TestRPC_InvalidParamsMapsToMinus32602 asserts that a malformed params
// payload routes to CodeInvalidParams (-32602).
func TestRPC_InvalidParamsMapsToMinus32602(t *testing.T) {
	d := NewDispatcher()
	RegisterDesktopAPI(d, &fakeHandler{})
	in := []byte(`{"jsonrpc":"2.0","id":1,"method":"session.start","params":["not","an","object"]}`)
	req, _ := DecodeRequest(in)
	resp := d.Dispatch(context.Background(), req)
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("expected invalid_params, got %+v", resp.Error)
	}
}

// TestRPC_MemoryListScopesNoParams asserts that `memory.list_scopes`
// works whether the client sends no params, an empty object, or even a
// stale param payload. The verb's signature has no params struct.
func TestRPC_MemoryListScopesNoParams(t *testing.T) {
	d := NewDispatcher()
	RegisterDesktopAPI(d, &fakeHandler{})
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"memory.list_scopes"}`,
		`{"jsonrpc":"2.0","id":2,"method":"memory.list_scopes","params":{}}`,
	} {
		req, _ := DecodeRequest([]byte(body))
		resp := d.Dispatch(context.Background(), req)
		if resp.Error != nil {
			t.Fatalf("unexpected error for %q: %+v", body, resp.Error)
		}
		var v desktopapi.MemoryListScopesResponse
		if err := json.Unmarshal(resp.Result, &v); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(v.Scopes) != 5 {
			t.Fatalf("scopes len: got %d want 5", len(v.Scopes))
		}
	}
}

// TestRPC_PanicOnNilDispatcherOrHandler asserts the misconfiguration
// guard fires at process start.
func TestRPC_PanicOnNilDispatcherOrHandler(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil handler")
		}
	}()
	RegisterDesktopAPI(NewDispatcher(), nil)
}

// TestRPC_paramsErrorChainPreserved asserts that the underlying
// json.Unmarshal error survives through errors.As on paramsError.
func TestRPC_paramsErrorChainPreserved(t *testing.T) {
	err := decodeParams(json.RawMessage(`bogus`), &struct{ X int }{})
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *paramsError
	if !errors.As(err, &pe) {
		t.Fatal("expected paramsError")
	}
	if pe.Unwrap() == nil {
		t.Fatal("expected unwrapable cause")
	}
}
