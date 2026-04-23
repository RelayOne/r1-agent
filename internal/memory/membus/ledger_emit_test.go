package membus

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// fakeLedger captures EmitNode calls for assertion.
type fakeLedger struct {
	mu    sync.Mutex
	calls []fakeLedgerCall
}

type fakeLedgerCall struct {
	NodeType      string
	SchemaVersion int
	CreatedBy     string
	Content       json.RawMessage
}

func (f *fakeLedger) EmitNode(_ context.Context, nodeType string, schemaVersion int, createdBy string, content any) error {
	raw, err := json.Marshal(content)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeLedgerCall{
		NodeType:      nodeType,
		SchemaVersion: schemaVersion,
		CreatedBy:     createdBy,
		Content:       raw,
	})
	return nil
}

func (f *fakeLedger) snapshot() []fakeLedgerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeLedgerCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func TestRememberEmitsMemoryStoredNode(t *testing.T) {
	ctx := context.Background()
	fl := &fakeLedger{}
	b, err := NewBus(openTestDB(t), Options{Ledger: fl})
	if err != nil {
		t.Fatalf("NewBus: %v", err)
	}
	if err := b.Remember(ctx, RememberRequest{
		Scope:     ScopeSession,
		SessionID: "s-1",
		TaskID:    "t-1",
		Author:    "worker:w1",
		Key:       "note",
		Content:   "hello world",
		Metadata:  map[string]string{"memory_type": "semantic"},
	}); err != nil {
		t.Fatal(err)
	}
	calls := fl.snapshot()
	if len(calls) != 1 {
		t.Fatalf("ledger EmitNode call count = %d, want 1", len(calls))
	}
	got := calls[0]
	if got.NodeType != "memory_stored" {
		t.Errorf("NodeType = %q, want memory_stored", got.NodeType)
	}
	if got.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", got.SchemaVersion)
	}
	if got.CreatedBy != "worker:w1" {
		t.Errorf("CreatedBy = %q, want worker:w1", got.CreatedBy)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Content, &payload); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if payload["scope"] != "session" {
		t.Errorf("payload.scope = %v, want session", payload["scope"])
	}
	if payload["key"] != "note" {
		t.Errorf("payload.key = %v, want note", payload["key"])
	}
	if payload["written_by"] != "worker:w1" {
		t.Errorf("payload.written_by = %v, want worker:w1", payload["written_by"])
	}
	if payload["memory_type"] != "semantic" {
		t.Errorf("payload.memory_type = %v, want semantic", payload["memory_type"])
	}
	if hash, _ := payload["content_hash"].(string); hash == "" {
		t.Errorf("payload.content_hash is empty")
	}
}

func TestRecallEmitsMemoryRecalledNode(t *testing.T) {
	ctx := context.Background()
	fl := &fakeLedger{}
	b, err := NewBus(openTestDB(t), Options{Ledger: fl})
	if err != nil {
		t.Fatalf("NewBus: %v", err)
	}
	if err := b.Remember(ctx, RememberRequest{
		Scope:     ScopeSession,
		SessionID: "s-1",
		Author:    "worker:w1",
		Key:       "k",
		Content:   "payload",
	}); err != nil {
		t.Fatal(err)
	}
	// One EmitNode so far: memory_stored.
	if got := len(fl.snapshot()); got != 1 {
		t.Fatalf("after Remember, calls = %d, want 1", got)
	}
	rows, err := b.Recall(ctx, RecallRequest{Scope: ScopeSession})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Recall rows = %d, want 1", len(rows))
	}
	calls := fl.snapshot()
	// memory_stored + one memory_recalled per row.
	if len(calls) != 2 {
		t.Fatalf("ledger calls = %d, want 2 (1 stored + 1 recalled)", len(calls))
	}
	last := calls[1]
	if last.NodeType != "memory_recalled" {
		t.Errorf("last NodeType = %q, want memory_recalled", last.NodeType)
	}
	var payload map[string]any
	if err := json.Unmarshal(last.Content, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["scope"] != "session" {
		t.Errorf("payload.scope = %v, want session", payload["scope"])
	}
	if payload["recalled_by"] != "worker:w1" {
		t.Errorf("payload.recalled_by = %v, want worker:w1", payload["recalled_by"])
	}
	if hash, _ := payload["content_hash"].(string); hash == "" {
		t.Errorf("payload.content_hash is empty")
	}
}

func TestRememberNilLedgerNoOp(t *testing.T) {
	// Regression guard: the no-op path must not panic and must leave
	// existing behavior (successful write, nil ledger) intact.
	ctx := context.Background()
	b, err := NewBus(openTestDB(t), Options{}) // no Ledger
	if err != nil {
		t.Fatalf("NewBus: %v", err)
	}
	if err := b.Remember(ctx, RememberRequest{
		Scope:   ScopeSession,
		Author:  "system",
		Key:     "k",
		Content: "payload",
	}); err != nil {
		t.Fatalf("Remember without ledger: %v", err)
	}
	if _, err := b.Recall(ctx, RecallRequest{Scope: ScopeSession}); err != nil {
		t.Fatalf("Recall without ledger: %v", err)
	}
}
