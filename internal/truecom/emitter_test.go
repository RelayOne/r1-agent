package truecom

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestEmitSOWReceipt_RejectsEmptyTaskID verifies that EmitSOWReceipt
// fails fast when no task_id is provided (client-side validation).
func TestEmitSOWReceipt_RejectsEmptyTaskID(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not reach server for empty task_id")
	}))
	_, err := c.EmitSOWReceipt(context.Background(), SOWReceipt{
		Outcome: "pass",
	})
	if err == nil {
		t.Fatal("expected error for empty task_id, got nil")
	}
}

// TestEmitSOWReceipt_RejectsEmptyOutcome verifies that EmitSOWReceipt
// fails fast when no outcome is provided.
func TestEmitSOWReceipt_RejectsEmptyOutcome(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not reach server for empty outcome")
	}))
	_, err := c.EmitSOWReceipt(context.Background(), SOWReceipt{
		TaskID: "t-abc123",
	})
	if err == nil {
		t.Fatal("expected error for empty outcome, got nil")
	}
}

// TestEmitSOWReceipt_ForcesSourceR1 verifies that the client always
// sets source="r1" on the wire, overriding any caller-supplied value.
func TestEmitSOWReceipt_ForcesSourceR1(t *testing.T) {
	var seenBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &seenBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"receipt_id":"r-1","task_id":"t-abc","status":"stored","stored_at":"2026-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.Config.Handler)
	// Caller accidentally sets source to something else
	_, err := c.EmitSOWReceipt(context.Background(), SOWReceipt{
		TaskID:  "t-abc",
		Outcome: "pass",
		Source:  "wrong-caller-set",
	})
	if err != nil {
		t.Fatalf("EmitSOWReceipt: %v", err)
	}
	if src, _ := seenBody["source"].(string); src != "r1" {
		t.Errorf("source on wire = %q, want \"r1\"", src)
	}
}

// TestEmitSOWReceipt_SetsEmittedAtWhenZero verifies that the client
// fills EmittedAt when the caller leaves it at zero value.
func TestEmitSOWReceipt_SetsEmittedAtWhenZero(t *testing.T) {
	var seenBody map[string]any
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &seenBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"receipt_id":"r-2","task_id":"t-zero","status":"stored","stored_at":"2026-01-01T00:00:00Z"}`))
	})
	c, _ := newTestClient(t, handler)
	before := time.Now().UTC()
	_, err := c.EmitSOWReceipt(context.Background(), SOWReceipt{
		TaskID:  "t-zero",
		Outcome: "fail",
		// EmittedAt intentionally zero
	})
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("EmitSOWReceipt: %v", err)
	}
	emittedStr, _ := seenBody["emitted_at"].(string)
	if emittedStr == "" {
		t.Fatal("emitted_at missing from wire payload")
	}
	emitted, err := time.Parse(time.RFC3339, emittedStr)
	if err != nil {
		t.Fatalf("emitted_at parse: %v", err)
	}
	if emitted.Before(before) || emitted.After(after.Add(time.Second)) {
		t.Errorf("emitted_at = %v is outside the test window [%v, %v]", emitted, before, after)
	}
}

// TestEmitSOWReceipt_PostsToCorrectPath verifies the HTTP method and path.
func TestEmitSOWReceipt_PostsToCorrectPath(t *testing.T) {
	var seenMethod, seenPath string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMethod = r.Method
		seenPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"receipt_id":"r-3","task_id":"t-path","status":"stored","stored_at":"2026-01-01T00:00:00Z"}`))
	})
	c, _ := newTestClient(t, handler)
	_, err := c.EmitSOWReceipt(context.Background(), SOWReceipt{
		TaskID:  "t-path",
		Outcome: "pass",
	})
	if err != nil {
		t.Fatalf("EmitSOWReceipt: %v", err)
	}
	if seenMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", seenMethod)
	}
	if seenPath != "/v1/r1/receipt" {
		t.Errorf("path = %s, want /v1/r1/receipt", seenPath)
	}
}

// TestEmitSOWReceipt_ReturnsMappedError verifies that a 5xx response
// from TrustPlane is returned as an error (not silently dropped).
func TestEmitSOWReceipt_ReturnsMappedError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
	})
	c, _ := newTestClient(t, handler)
	_, err := c.EmitSOWReceipt(context.Background(), SOWReceipt{
		TaskID:  "t-5xx",
		Outcome: "pass",
	})
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}

// TestEmitSOWReceipt_DegradesWhenServerDown verifies that a connection
// failure (server closed) returns an error, not a panic.
func TestEmitSOWReceipt_DegradesWhenServerDown(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	c, srv := newTestClient(t, handler)
	// Close the server before the call to simulate the gateway being down.
	srv.Close()
	_, err := c.EmitSOWReceipt(context.Background(), SOWReceipt{
		TaskID:  "t-down",
		Outcome: "pass",
	})
	if err == nil {
		t.Fatal("expected error when server is down, got nil")
	}
}

// TestListSOWReceipts_BuildsCorrectQueryPath verifies that
// ListSOWReceipts correctly appends query parameters.
func TestListSOWReceipts_BuildsCorrectQueryPath(t *testing.T) {
	var seenURL string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenURL = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"receipts":[],"total":0}`))
	})
	c, _ := newTestClient(t, handler)
	_, err := c.ListSOWReceipts(context.Background(), "r1", "did:tp:test-agent", "")
	if err != nil {
		t.Fatalf("ListSOWReceipts: %v", err)
	}
	if seenURL == "" {
		t.Fatal("expected a request to be made")
	}
	if seenURL != "/v1/r1/receipts?source=r1&agent_did=did:tp:test-agent" {
		t.Errorf("URL = %q, want /v1/r1/receipts?source=r1&agent_did=did:tp:test-agent", seenURL)
	}
}

// TestListSOWReceipts_ParsesResponse verifies the response decodes correctly.
func TestListSOWReceipts_ParsesResponse(t *testing.T) {
	now := time.Now().UTC().Round(time.Second)
	payload := map[string]any{
		"receipts": []any{
			map[string]any{
				"receipt_id": "r-42",
				"task_id":    "t-hello",
				"outcome":    "pass",
				"source":     "r1",
				"emitted_at": now.Format(time.RFC3339),
				"stored_at":  now.Format(time.RFC3339),
			},
		},
		"total": 1,
	}
	buf, _ := json.Marshal(payload)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buf)
	})
	c, _ := newTestClient(t, handler)
	resp, err := c.ListSOWReceipts(context.Background(), "r1", "", "")
	if err != nil {
		t.Fatalf("ListSOWReceipts: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1", resp.Total)
	}
	if len(resp.Receipts) != 1 {
		t.Fatalf("len(receipts) = %d, want 1", len(resp.Receipts))
	}
	if resp.Receipts[0].ReceiptID != "r-42" {
		t.Errorf("receipt_id = %q, want \"r-42\"", resp.Receipts[0].ReceiptID)
	}
	if resp.Receipts[0].Source != "r1" {
		t.Errorf("source = %q, want \"r1\"", resp.Receipts[0].Source)
	}
}
