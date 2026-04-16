package sharedmem

import (
	"context"
	"errors"
	"testing"
	"time"
)

func ts() time.Time { return time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC) }

func baseProv(agent string) ProvenanceEntry {
	return ProvenanceEntry{AgentID: agent, Action: "create", Timestamp: ts()}
}

func TestMemoryStore_CreateAndGet(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	b := &Block{
		ID:         "b1",
		Type:       "log",
		Label:      "scratch",
		Value:      []any{"first"},
		Provenance: []ProvenanceEntry{baseProv("agent-a")},
	}
	if err := s.Create(ctx, b); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, "b1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Version != 1 {
		t.Errorf("Version=%d want 1", got.Version)
	}
	if got.Namespace != "default" {
		t.Errorf("Namespace=%q want default", got.Namespace)
	}
	// Mutating the returned clone shouldn't affect the store.
	got.Label = "tampered"
	reload, _ := s.Get(ctx, "b1")
	if reload.Label == "tampered" {
		t.Error("Get should return a clone; mutating it affected the store")
	}
}

func TestMemoryStore_CreateDuplicateFails(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	b := &Block{ID: "b1", Type: "log", Provenance: []ProvenanceEntry{baseProv("a")}}
	_ = s.Create(ctx, b)
	if err := s.Create(ctx, b); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("want ErrAlreadyExists, got %v", err)
	}
}

func TestMemoryStore_CreateRequiresProvenance(t *testing.T) {
	s := NewMemoryStore()
	b := &Block{ID: "b1", Type: "log", Provenance: nil}
	err := s.Create(context.Background(), b)
	if !errors.Is(err, ErrNoProvenance) {
		t.Errorf("want ErrNoProvenance, got %v", err)
	}
}

func TestApply_SemanticInsert_WithAddReducer(t *testing.T) {
	s := NewMemoryStore()
	s.RegisterReducer("log", AddReducer)
	ctx := context.Background()
	_ = s.Create(ctx, &Block{
		ID: "b1", Type: "log", Value: []any{"a"},
		Provenance: []ProvenanceEntry{baseProv("agent-a")},
	})
	out, err := s.Apply(ctx, Write{
		BlockID:    "b1",
		Semantic:   SemanticInsert,
		Value:      []any{"b", "c"},
		Provenance: ProvenanceEntry{AgentID: "agent-a", Action: "insert", Timestamp: ts()},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, _ := out.Value.([]any)
	if len(got) != 3 {
		t.Errorf("value len=%d want 3, got %v", len(got), got)
	}
	if out.Version != 2 {
		t.Errorf("Version=%d want 2", out.Version)
	}
	if len(out.Provenance) != 2 {
		t.Errorf("Provenance len=%d want 2", len(out.Provenance))
	}
}

func TestApply_SemanticReplace_VersionMismatch(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.Create(ctx, &Block{
		ID: "b1", Type: "scalar", Value: 1,
		Provenance: []ProvenanceEntry{baseProv("a")},
	})
	_, err := s.Apply(ctx, Write{
		BlockID:         "b1",
		Semantic:        SemanticReplace,
		Value:           2,
		ExpectedVersion: 42, // wrong
		Provenance:      ProvenanceEntry{AgentID: "a", Action: "replace", Timestamp: ts()},
	})
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("want ErrVersionMismatch, got %v", err)
	}
}

func TestApply_SemanticRethink_LastWriteWins(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.Create(ctx, &Block{
		ID: "b1", Type: "scalar", Value: "old",
		Provenance: []ProvenanceEntry{baseProv("a")},
	})
	out, err := s.Apply(ctx, Write{
		BlockID:    "b1",
		Semantic:   SemanticRethink,
		Value:      "new",
		Provenance: ProvenanceEntry{AgentID: "a", Action: "rethink", Timestamp: ts()},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out.Value != "new" {
		t.Errorf("Value=%v want new", out.Value)
	}
}

func TestApply_InsertWithoutReducerFails(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.Create(ctx, &Block{
		ID: "b1", Type: "log", Value: []any{},
		Provenance: []ProvenanceEntry{baseProv("a")},
	})
	_, err := s.Apply(ctx, Write{
		BlockID:    "b1",
		Semantic:   SemanticInsert,
		Value:      []any{"x"},
		Provenance: ProvenanceEntry{AgentID: "a", Action: "insert", Timestamp: ts()},
	})
	if err == nil {
		t.Fatal("expected error when no reducer registered for type")
	}
}

func TestApply_ProvenanceRequired(t *testing.T) {
	s := NewMemoryStore()
	s.RegisterReducer("log", AddReducer)
	ctx := context.Background()
	_ = s.Create(ctx, &Block{
		ID: "b1", Type: "log", Value: []any{},
		Provenance: []ProvenanceEntry{baseProv("a")},
	})
	_, err := s.Apply(ctx, Write{
		BlockID:  "b1",
		Semantic: SemanticInsert,
		Value:    []any{"x"},
		// Provenance missing
	})
	if !errors.Is(err, ErrNoProvenance) {
		t.Errorf("want ErrNoProvenance, got %v", err)
	}
}

func TestSubscribe_ReceivesUpdates(t *testing.T) {
	s := NewMemoryStore()
	s.RegisterReducer("log", AddReducer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = s.Create(ctx, &Block{
		ID: "b1", Type: "log", Value: []any{},
		Provenance: []ProvenanceEntry{baseProv("a")},
	})
	ch, err := s.Subscribe(ctx, "b1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	// One update should arrive on the channel.
	_, _ = s.Apply(ctx, Write{
		BlockID: "b1", Semantic: SemanticInsert,
		Value:      []any{"x"},
		Provenance: ProvenanceEntry{AgentID: "a", Action: "insert", Timestamp: ts()},
	})
	select {
	case got := <-ch:
		if got == nil {
			t.Error("got nil on subscription channel")
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("subscription never received the update")
	}
}

func TestRollback_RestoresValueAndPreservesHistory(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.Create(ctx, &Block{
		ID: "b1", Type: "scalar", Value: "v1",
		Provenance: []ProvenanceEntry{baseProv("a")},
	})
	_, _ = s.Apply(ctx, Write{
		BlockID: "b1", Semantic: SemanticRethink, Value: "v2",
		Provenance: ProvenanceEntry{AgentID: "a", Action: "rethink", Timestamp: ts()},
	})
	out, err := s.Rollback(ctx, "b1", 1, ProvenanceEntry{
		AgentID:     "a",
		Timestamp:   ts(),
		ReplayValue: "v1",
	})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if out.Value != "v1" {
		t.Errorf("Value=%v want v1", out.Value)
	}
	if out.Version != 3 {
		t.Errorf("rollback should bump version (create=1 replace=2 rollback=3), got %d", out.Version)
	}
	if len(out.Provenance) != 3 {
		t.Errorf("Provenance entries=%d want 3 (history preserved)", len(out.Provenance))
	}
	last := out.Provenance[len(out.Provenance)-1]
	if last.Action != "rollback" {
		t.Errorf("last prov action=%q want rollback", last.Action)
	}
	if last.RolledBackTo != 1 {
		t.Errorf("RolledBackTo=%d want 1", last.RolledBackTo)
	}
}

func TestAddReducer(t *testing.T) {
	out, err := AddReducer([]any{"a"}, []any{"b", "c"})
	if err != nil {
		t.Fatalf("AddReducer: %v", err)
	}
	got := out.([]any)
	if len(got) != 3 {
		t.Errorf("len=%d want 3", len(got))
	}
}

func TestUnionReducer_Dedupes(t *testing.T) {
	out, err := UnionReducer([]any{"a", "b"}, []any{"b", "c"})
	if err != nil {
		t.Fatalf("UnionReducer: %v", err)
	}
	got := out.([]any)
	if len(got) != 3 {
		t.Errorf("len=%d want 3 (a,b,c) got %v", len(got), got)
	}
}

func TestMaxReducer(t *testing.T) {
	out, err := MaxReducer(3, 7)
	if err != nil {
		t.Fatalf("MaxReducer: %v", err)
	}
	if out != 7 {
		t.Errorf("got %v want 7", out)
	}
	out2, _ := MaxReducer(10.5, 2)
	if out2 != 10.5 {
		t.Errorf("got %v want 10.5", out2)
	}
}

func TestMaxReducer_RejectsNonNumeric(t *testing.T) {
	if _, err := MaxReducer("a", 1); err == nil {
		t.Error("want error on non-numeric")
	}
}

func TestProv_Validation(t *testing.T) {
	if err := validateProvEntry(ProvenanceEntry{}); err == nil {
		t.Error("empty prov should fail")
	}
	if err := validateProvEntry(ProvenanceEntry{AgentID: "a"}); err == nil {
		t.Error("prov missing timestamp should fail")
	}
	if err := validateProvEntry(ProvenanceEntry{AgentID: "a", Timestamp: ts(), Confidence: 1.5}); err == nil {
		t.Error("confidence > 1 should fail")
	}
	if err := validateProvEntry(ProvenanceEntry{AgentID: "a", Timestamp: ts()}); err != nil {
		t.Errorf("minimal valid prov failed: %v", err)
	}
}
