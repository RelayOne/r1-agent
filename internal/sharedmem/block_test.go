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
	got, ok := out.([]any)
	if !ok {
		t.Fatalf("out: unexpected type: %T", out)
	}
	if len(got) != 3 {
		t.Errorf("len=%d want 3", len(got))
	}
}

func TestUnionReducer_Dedupes(t *testing.T) {
	out, err := UnionReducer([]any{"a", "b"}, []any{"b", "c"})
	if err != nil {
		t.Fatalf("UnionReducer: %v", err)
	}
	got, ok := out.([]any)
	if !ok {
		t.Fatalf("out: unexpected type: %T", out)
	}
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

func TestMaxReducer_LargeInt64Precision(t *testing.T) {
	// Values above 2^53 can't all be distinguished as
	// float64. Without int64 fast-path, (A, A+1) where both
	// are > 2^53 would compare as equal. Test two timestamps
	// that differ by 1 ns and make sure MaxReducer picks
	// the larger.
	a := int64(1_744_704_000_000_000_000) // 2026-04-15T12:00:00Z in ns
	b := a + 1
	out, err := MaxReducer(a, b)
	if err != nil {
		t.Fatalf("MaxReducer: %v", err)
	}
	outI64, ok := out.(int64)
	if !ok {
		t.Fatalf("out: unexpected type: %T", out)
	}
	if outI64 != b {
		t.Errorf("MaxReducer picked %v want %v (int64 precision regression)", out, b)
	}
	// Reverse order.
	out2, _ := MaxReducer(b, a)
	out2I64, ok := out2.(int64)
	if !ok {
		t.Fatalf("out2: unexpected type: %T", out2)
	}
	if out2I64 != b {
		t.Errorf("MaxReducer reverse picked %v want %v", out2, b)
	}
}

func TestMaxReducer_MixedIntTypes(t *testing.T) {
	// Accept mixed integer types via toInt64.
	out, err := MaxReducer(int32(5), int64(10))
	if err != nil {
		t.Fatalf("MaxReducer: %v", err)
	}
	outI64, ok := out.(int64)
	if !ok {
		t.Fatalf("out: unexpected type: %T", out)
	}
	if outI64 != 10 {
		t.Errorf("got %v want 10", out)
	}
}

func TestMaxReducer_MixedIntAndFloat(t *testing.T) {
	// int-vs-float falls through to float comparison.
	out, err := MaxReducer(3, 5.5)
	if err != nil {
		t.Fatalf("MaxReducer: %v", err)
	}
	outF64, ok := out.(float64)
	if !ok {
		t.Fatalf("out: unexpected type: %T", out)
	}
	if outF64 != 5.5 {
		t.Errorf("got %v want 5.5", out)
	}
}

// TestMaxReducer_MixedNarrowIntsAndFloat: the P2 codex
// finding — MaxReducer claimed to accept int8/int16/uint*
// but toFloat only handled int/int32/int64, so mixed-type
// calls incorrectly returned "not numeric".
func TestMaxReducer_MixedNarrowIntsAndFloat(t *testing.T) {
	cases := []struct {
		a, b any
		want float64
	}{
		{int8(1), 1.5, 1.5},
		{int16(1), 1.5, 1.5},
		{uint8(1), 1.5, 1.5},
		{uint16(3), 2.0, 3.0},
		{uint32(7), 6.5, 7.0},
	}
	for _, c := range cases {
		out, err := MaxReducer(c.a, c.b)
		if err != nil {
			t.Errorf("MaxReducer(%v, %v): %v", c.a, c.b, err)
			continue
		}
		// Accept either int-typed or float-typed result
		// depending on which side won.
		switch x := out.(type) {
		case float64:
			if x != c.want {
				t.Errorf("MaxReducer(%v, %v)=%v want %v", c.a, c.b, x, c.want)
			}
		case int8:
			if float64(x) != c.want {
				t.Errorf("int8 result %v want %v", x, c.want)
			}
		case int16:
			if float64(x) != c.want {
				t.Errorf("int16 result %v want %v", x, c.want)
			}
		case uint8:
			if float64(x) != c.want {
				t.Errorf("uint8 result %v want %v", x, c.want)
			}
		case uint16:
			if float64(x) != c.want {
				t.Errorf("uint16 result %v want %v", x, c.want)
			}
		case uint32:
			if float64(x) != c.want {
				t.Errorf("uint32 result %v want %v", x, c.want)
			}
		}
	}
}

func TestCloneBlock_DeepCopiesValue(t *testing.T) {
	// Caller mutating the returned Value slice must NOT
	// affect the stored block.
	s := NewMemoryStore()
	ctx := context.Background()
	s.RegisterReducer("list", AddReducer)
	_ = s.Create(ctx, &Block{
		ID: "b1", Type: "list",
		Value:      []any{"a", "b"},
		Provenance: []ProvenanceEntry{baseProv("agent-a")},
	})

	got, err := s.Get(ctx, "b1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Tamper with the returned slice.
	val, ok := got.Value.([]any)
	if !ok {
		t.Fatalf("got.Value: unexpected type: %T", got.Value)
	}
	val[0] = "TAMPERED"

	// Re-read — the store's copy should be unaffected.
	got2, _ := s.Get(ctx, "b1")
	val2, ok := got2.Value.([]any)
	if !ok {
		t.Fatalf("got2.Value: unexpected type: %T", got2.Value)
	}
	if val2[0] == "TAMPERED" {
		t.Errorf("store leaked Value aliasing: %v", val2)
	}
}

func TestCloneBlock_DeepCopiesMapValue(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.Create(ctx, &Block{
		ID: "b1", Type: "map",
		Value:      map[string]any{"k": "v"},
		Provenance: []ProvenanceEntry{baseProv("agent-a")},
	})
	got, _ := s.Get(ctx, "b1")
	m, ok := got.Value.(map[string]any)
	if !ok {
		t.Fatalf("got.Value: unexpected type: %T", got.Value)
	}
	m["k"] = "TAMPERED"
	got2, _ := s.Get(ctx, "b1")
	m2, ok := got2.Value.(map[string]any)
	if !ok {
		t.Fatalf("got2.Value: unexpected type: %T", got2.Value)
	}
	if m2["k"] == "TAMPERED" {
		t.Errorf("map aliasing leaked: %v", m2)
	}
}

// TestCloneBlock_PreservesTypedSliceValue: the P1 fix.
// Before: []string stored as Value was rewritten to []any
// by the JSON fallback. Now the reflect-based deep copy
// preserves the original element type so type assertions
// keep working.
func TestCloneBlock_PreservesTypedSliceValue(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	original := []string{"a", "b", "c"}
	_ = s.Create(ctx, &Block{
		ID: "b1", Type: "typed-list",
		Value:      original,
		Provenance: []ProvenanceEntry{baseProv("agent")},
	})
	got, _ := s.Get(ctx, "b1")
	// Type-assert as []string — must not panic and must
	// return the expected values.
	ss, ok := got.Value.([]string)
	if !ok {
		t.Fatalf("Value type=%T want []string (deep-copy preserved type)", got.Value)
	}
	if len(ss) != 3 || ss[0] != "a" || ss[2] != "c" {
		t.Errorf("content mismatch: %v", ss)
	}
	// Mutation independence.
	ss[0] = "TAMPERED"
	got2, _ := s.Get(ctx, "b1")
	ss2, ok := got2.Value.([]string)
	if !ok {
		t.Fatalf("got2.Value: unexpected type: %T", got2.Value)
	}
	if ss2[0] == "TAMPERED" {
		t.Error("mutation leaked through to store")
	}
}

func TestCloneBlock_PreservesTypedMapValue(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.Create(ctx, &Block{
		ID: "b1", Type: "typed-map",
		Value:      map[string]int{"a": 1, "b": 2},
		Provenance: []ProvenanceEntry{baseProv("agent")},
	})
	got, _ := s.Get(ctx, "b1")
	m, ok := got.Value.(map[string]int)
	if !ok {
		t.Fatalf("Value type=%T want map[string]int", got.Value)
	}
	if m["a"] != 1 || m["b"] != 2 {
		t.Errorf("content mismatch: %v", m)
	}
}

func TestCloneBlock_DeepCopiesProvenanceSources(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	prov := ProvenanceEntry{
		AgentID: "a", Action: "create", Timestamp: ts(),
		Sources: []string{"src-1", "src-2"},
	}
	_ = s.Create(ctx, &Block{
		ID: "b1", Type: "scalar", Value: "x",
		Provenance: []ProvenanceEntry{prov},
	})
	got, _ := s.Get(ctx, "b1")
	got.Provenance[0].Sources[0] = "TAMPERED"

	got2, _ := s.Get(ctx, "b1")
	if got2.Provenance[0].Sources[0] == "TAMPERED" {
		t.Errorf("provenance.Sources aliasing leaked: %v", got2.Provenance[0].Sources)
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
