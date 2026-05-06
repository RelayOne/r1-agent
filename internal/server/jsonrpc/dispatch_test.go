package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/RelayOne/r1/internal/desktopapi"
	"github.com/RelayOne/r1/internal/stokerr"
)

// TestJSONRPC_RequestResponseEnvelope round-trips the success path:
// decode -> dispatch -> encode and assert the wire shape matches the
// IPC-CONTRACT §1 contract.
func TestJSONRPC_RequestResponseEnvelope(t *testing.T) {
	d := NewDispatcher()
	d.Register("ping", func(ctx context.Context, params json.RawMessage) (any, error) {
		return map[string]any{"pong": true}, nil
	})

	in := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`)
	req, err := DecodeRequest(in)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.JSONRPC != "2.0" {
		t.Fatalf("expected jsonrpc=2.0, got %q", req.JSONRPC)
	}
	if req.Method != "ping" {
		t.Fatalf("method: %q", req.Method)
	}

	resp := d.Dispatch(context.Background(), req)
	if resp == nil {
		t.Fatal("nil response for non-notification request")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if !bytes.Equal(resp.ID, []byte("1")) {
		t.Fatalf("id echo: got %s want 1", resp.ID)
	}

	out, err := EncodeResponse(resp)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Result must round-trip the handler's payload.
	var v struct {
		Result struct {
			Pong bool `json:"pong"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if !v.Result.Pong {
		t.Fatal("result.pong did not round-trip")
	}
}

// TestJSONRPC_ErrorCodes asserts the IPC-CONTRACT §3 mapping table:
// every stokerr.Code maps to its documented numeric code AND carries the
// stoke_code mirror in data.
func TestJSONRPC_ErrorCodes(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		wantCode    int
		wantStoke   string
	}{
		{"validation", stokerr.New(stokerr.ErrValidation, "x"), CodeValidation, "validation"},
		{"not_found", stokerr.New(stokerr.ErrNotFound, "x"), CodeNotFound, "not_found"},
		{"conflict", stokerr.New(stokerr.ErrConflict, "x"), CodeConflict, "conflict"},
		{"append_only", stokerr.New(stokerr.ErrAppendOnly, "x"), CodeAppendOnly, "append_only_violation"},
		{"permission", stokerr.New(stokerr.ErrPermission, "x"), CodePermissionDenied, "permission_denied"},
		{"budget", stokerr.New(stokerr.ErrBudgetExceeded, "x"), CodeBudgetExceeded, "budget_exceeded"},
		{"timeout", stokerr.New(stokerr.ErrTimeout, "x"), CodeTimeout, "timeout"},
		{"crash_recovery", stokerr.New(stokerr.ErrCrashRecovery, "x"), CodeCrashRecovery, "crash_recovery"},
		{"schema_version", stokerr.New(stokerr.ErrSchemaVersion, "x"), CodeSchemaVersion, "schema_version"},
		{"internal", stokerr.New(stokerr.ErrInternal, "x"), CodeInternalTaxonomy, "internal"},
		{"not_implemented", desktopapi.ErrNotImplemented, CodeNotImplemented, "not_implemented"},
		{"plain_error", errors.New("boom"), CodeInternalError, "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := ErrorFromGo(tc.err)
			if e == nil {
				t.Fatal("nil error")
			}
			if e.Code != tc.wantCode {
				t.Fatalf("code: got %d want %d", e.Code, tc.wantCode)
			}
			if got := e.Data["stoke_code"]; got != tc.wantStoke {
				t.Fatalf("stoke_code: got %v want %s", got, tc.wantStoke)
			}
		})
	}
	if got := ErrorFromGo(nil); got != nil {
		t.Fatalf("nil err must produce nil Error, got %+v", got)
	}
}

// TestJSONRPC_Notification asserts that a request without an id field is
// recognised as a notification AND that Dispatch returns nil for it
// (no response wire bytes — JSON-RPC 2.0 §4.1).
func TestJSONRPC_Notification(t *testing.T) {
	d := NewDispatcher()
	called := 0
	d.Register("ping", func(ctx context.Context, params json.RawMessage) (any, error) {
		called++
		return "ok", nil
	})

	in := []byte(`{"jsonrpc":"2.0","method":"ping","params":{}}`)
	req, err := DecodeRequest(in)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !req.IsNotification() {
		t.Fatal("expected notification (no id)")
	}
	resp := d.Dispatch(context.Background(), req)
	if resp != nil {
		t.Fatalf("notification must yield nil response, got %+v", resp)
	}
	if called != 1 {
		t.Fatalf("handler call count: got %d want 1", called)
	}

	// Also verify a notification for an unknown method drops silently.
	in2 := []byte(`{"jsonrpc":"2.0","method":"unknown","params":{}}`)
	r2, _ := DecodeRequest(in2)
	if got := d.Dispatch(context.Background(), r2); got != nil {
		t.Fatalf("unknown notification: got %+v want nil", got)
	}
}

// TestJSONRPC_Batch asserts batch dispatch: mixed notifications +
// requests yield only the request-side responses, in order.
func TestJSONRPC_Batch(t *testing.T) {
	d := NewDispatcher()
	d.Register("a", func(ctx context.Context, params json.RawMessage) (any, error) {
		return "A", nil
	})
	d.Register("b", func(ctx context.Context, params json.RawMessage) (any, error) {
		return "B", nil
	})

	in := []byte(`[
		{"jsonrpc":"2.0","id":1,"method":"a"},
		{"jsonrpc":"2.0","method":"a"},
		{"jsonrpc":"2.0","id":"two","method":"b"}
	]`)
	single, batch, err := DecodeBatchOrSingle(in)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if single != nil || len(batch) != 3 {
		t.Fatalf("expected 3-batch, got single=%v batch=%d", single != nil, len(batch))
	}

	resps := d.DispatchBatch(context.Background(), batch)
	// One notification dropped — expect 2 responses.
	if len(resps) != 2 {
		t.Fatalf("response count: got %d want 2", len(resps))
	}
	if !bytes.Equal(resps[0].ID, []byte("1")) {
		t.Fatalf("first id: got %s want 1", resps[0].ID)
	}
	if !bytes.Equal(resps[1].ID, []byte(`"two"`)) {
		t.Fatalf("second id: got %s want \"two\"", resps[1].ID)
	}

	out, err := EncodeBatch(resps)
	if err != nil {
		t.Fatalf("encode batch: %v", err)
	}
	if out[0] != '[' {
		t.Fatalf("expected JSON array, got %s", out)
	}

	// Empty batch is invalid per spec.
	if _, _, err := DecodeBatchOrSingle([]byte(`[]`)); err == nil {
		t.Fatal("empty batch must be an error")
	}
}

// TestJSONRPC_ParseAndInvalidRequest covers the transport-side errors
// that the dispatcher emits without invoking a handler.
func TestJSONRPC_ParseAndInvalidRequest(t *testing.T) {
	d := NewDispatcher()
	// Wrong jsonrpc version.
	req := &Request{JSONRPC: "1.0", ID: json.RawMessage("1"), Method: "x"}
	resp := d.Dispatch(context.Background(), req)
	if resp == nil || resp.Error == nil || resp.Error.Code != CodeInvalidRequest {
		t.Fatalf("expected invalid request error, got %+v", resp)
	}
	// Empty method.
	req2 := &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: ""}
	resp = d.Dispatch(context.Background(), req2)
	if resp.Error.Code != CodeInvalidRequest {
		t.Fatalf("expected invalid request, got code=%d", resp.Error.Code)
	}
	// Unknown method.
	req3 := &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "nope"}
	resp = d.Dispatch(context.Background(), req3)
	if resp.Error.Code != CodeMethodNotFound {
		t.Fatalf("expected method not found, got code=%d", resp.Error.Code)
	}
}

// TestJSONRPC_HandlerPanicRecovered asserts that a panicking handler is
// turned into CodeInternalError instead of crashing the dispatcher.
func TestJSONRPC_HandlerPanicRecovered(t *testing.T) {
	d := NewDispatcher()
	d.Register("boom", func(ctx context.Context, params json.RawMessage) (any, error) {
		panic("kaboom")
	})
	req := &Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "boom"}
	resp := d.Dispatch(context.Background(), req)
	if resp.Error == nil || resp.Error.Code != CodeInternalError {
		t.Fatalf("expected internal error, got %+v", resp)
	}
}
