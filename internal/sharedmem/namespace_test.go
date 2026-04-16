package sharedmem

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func makeNSBlock(id, ns string) *Block {
	return &Block{
		ID:        BlockID(id),
		Type:      "scalar",
		Label:     id,
		Namespace: ns,
		Value:     "x",
		Provenance: []ProvenanceEntry{{
			AgentID:   "agent-a",
			Action:    "create",
			Timestamp: time.Now().UTC(),
		}},
	}
}

func TestNewAllowList_NormalizesEmpty(t *testing.T) {
	al := NewAllowList("")
	if !al.Allows("default") {
		t.Error(`empty namespace input should normalize to "default"`)
	}
	if !al.Allows("") {
		t.Error(`Allows("") should match when "default" is in list`)
	}
}

func TestNamespacedStore_GetRespectsAllowList(t *testing.T) {
	inner := NewMemoryStore()
	n := NewNamespacedStore(inner, nil)
	ctx := context.Background()
	_ = inner.Create(ctx, makeNSBlock("b1", "team-a"))

	// Caller with team-a access: sees the block.
	if _, err := n.Get(ctx, "b1", "caller", NewAllowList("team-a")); err != nil {
		t.Errorf("Get with matching allow-list: %v", err)
	}
	// Caller with team-b only: denied.
	_, err := n.Get(ctx, "b1", "caller", NewAllowList("team-b"))
	if !errors.Is(err, ErrNamespaceDenied) {
		t.Errorf("want ErrNamespaceDenied, got %v", err)
	}
}

func TestNamespacedStore_ApplyDeniedForWrongNamespace(t *testing.T) {
	inner := NewMemoryStore()
	n := NewNamespacedStore(inner, nil)
	ctx := context.Background()
	_ = inner.Create(ctx, makeNSBlock("b1", "team-a"))

	_, err := n.Apply(ctx, Write{
		BlockID:    "b1",
		Semantic:   SemanticRethink,
		Value:      "y",
		Provenance: ProvenanceEntry{AgentID: "x", Action: "rethink", Timestamp: time.Now().UTC()},
	}, "caller", NewAllowList("team-b"))
	if !errors.Is(err, ErrNamespaceDenied) {
		t.Errorf("want ErrNamespaceDenied on cross-namespace write, got %v", err)
	}
}

func TestNamespacedStore_SubscribeDenied(t *testing.T) {
	inner := NewMemoryStore()
	n := NewNamespacedStore(inner, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = inner.Create(ctx, makeNSBlock("b1", "team-a"))

	_, err := n.Subscribe(ctx, "b1", "caller", NewAllowList("team-b"))
	if !errors.Is(err, ErrNamespaceDenied) {
		t.Errorf("want ErrNamespaceDenied on subscribe, got %v", err)
	}
}

type fakeMonitor struct {
	reads int
	deny  bool
}

func (f *fakeMonitor) RecordRead(_ context.Context, _ string, _ *Block) error {
	f.reads++
	if f.deny {
		return fmt.Errorf("inference accumulation exceeds policy")
	}
	return nil
}

func TestNamespacedStore_InferenceMonitor(t *testing.T) {
	inner := NewMemoryStore()
	mon := &fakeMonitor{}
	n := NewNamespacedStore(inner, mon)
	ctx := context.Background()
	_ = inner.Create(ctx, makeNSBlock("b1", "team-a"))

	if _, err := n.Get(ctx, "b1", "caller", NewAllowList("team-a")); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if mon.reads != 1 {
		t.Errorf("monitor.reads=%d want 1", mon.reads)
	}

	// Deny path: monitor returns an error.
	mon.deny = true
	if _, err := n.Get(ctx, "b1", "caller", NewAllowList("team-a")); err == nil {
		t.Error("expected error when monitor denies")
	}
}

func TestNamespacedStore_DeniedBlockHidesExistence(t *testing.T) {
	// When the caller doesn't have namespace access, the error
	// should be ErrNamespaceDenied — NOT ErrNotFound — so the
	// store never reveals "this block exists but you can't see
	// it" vs "this block doesn't exist".
	inner := NewMemoryStore()
	n := NewNamespacedStore(inner, nil)
	ctx := context.Background()
	_ = inner.Create(ctx, makeNSBlock("b1", "team-a"))

	_, err := n.Get(ctx, "b1", "caller", NewAllowList("team-b"))
	if !errors.Is(err, ErrNamespaceDenied) {
		t.Errorf("want ErrNamespaceDenied (not ErrNotFound), got %v", err)
	}
}
