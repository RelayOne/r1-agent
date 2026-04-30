package nodes

import (
	"testing"
	"time"
)

var zeroTime time.Time

func TestMemoryStored_RoundtripRegistered(t *testing.T) {
	n, err := New("memory_stored")
	if err != nil {
		t.Fatalf("New(\"memory_stored\"): %v", err)
	}
	if got := n.NodeType(); got != "memory_stored" {
		t.Errorf("NodeType() = %q, want %q", got, "memory_stored")
	}
	if got := n.SchemaVersion(); got != 1 {
		t.Errorf("SchemaVersion() = %d, want 1", got)
	}
	// Zero value must fail validation.
	if err := n.Validate(); err == nil {
		t.Error("zero-value MemoryStored.Validate() must return error")
	}
	var _ NodeTyper = &MemoryStored{}
}

func TestMemoryRecalled_RoundtripRegistered(t *testing.T) {
	n, err := New("memory_recalled")
	if err != nil {
		t.Fatalf("New(\"memory_recalled\"): %v", err)
	}
	if got := n.NodeType(); got != "memory_recalled" {
		t.Errorf("NodeType() = %q, want %q", got, "memory_recalled")
	}
	if got := n.SchemaVersion(); got != 1 {
		t.Errorf("SchemaVersion() = %d, want 1", got)
	}
	if err := n.Validate(); err == nil {
		t.Error("zero-value MemoryRecalled.Validate() must return error")
	}
	var _ NodeTyper = &MemoryRecalled{}
}

func TestMemoryStored_ValidateHappyPath(t *testing.T) {
	m := &MemoryStored{
		Scope:       "session",
		ScopeTarget: "sess-1",
		Key:         "k-1",
		ContentHash: "deadbeef",
		MemoryType:  "note",
		WrittenBy:   "worker:1",
		CreatedAt:   now,
		Version:     1,
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("valid MemoryStored.Validate() = %v", err)
	}
	for _, tc := range []struct {
		name  string
		clear func(*MemoryStored)
	}{
		{"scope", func(x *MemoryStored) { x.Scope = "" }},
		{"key", func(x *MemoryStored) { x.Key = "" }},
		{"content_hash", func(x *MemoryStored) { x.ContentHash = "" }},
		{"written_by", func(x *MemoryStored) { x.WrittenBy = "" }},
		{"created_at", func(x *MemoryStored) { x.CreatedAt = zeroTime }},
	} {
		bad := *m
		tc.clear(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("MemoryStored without %s should fail", tc.name)
		}
	}
}

func TestMemoryRecalled_ValidateHappyPath(t *testing.T) {
	m := &MemoryRecalled{
		Scope:       "session",
		Key:         "k-1",
		ContentHash: "deadbeef",
		RecalledBy:  "worker:2",
		CreatedAt:   now,
		Version:     1,
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("valid MemoryRecalled.Validate() = %v", err)
	}
	for _, tc := range []struct {
		name  string
		clear func(*MemoryRecalled)
	}{
		{"scope", func(x *MemoryRecalled) { x.Scope = "" }},
		{"key", func(x *MemoryRecalled) { x.Key = "" }},
		{"content_hash", func(x *MemoryRecalled) { x.ContentHash = "" }},
		{"recalled_by", func(x *MemoryRecalled) { x.RecalledBy = "" }},
		{"created_at", func(x *MemoryRecalled) { x.CreatedAt = zeroTime }},
	} {
		bad := *m
		tc.clear(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("MemoryRecalled without %s should fail", tc.name)
		}
	}
}

// TestRegisteredNodeCount is the invariant requested by the memory-bus spec
// (work-stoke T11): adding memory_stored + memory_recalled brought the total
// registered node-type count to 30. Artifact parity adds two more node types,
// so the current invariant is 32. If a future node type is added, bump
// this number in the same commit that adds it so the guard actually blocks
// accidental registrations.
func TestRegisteredNodeCount(t *testing.T) {
	const want = 32
	got := len(All())
	if got != want {
		t.Errorf("len(All()) = %d, want %d (update TestRegisteredNodeCount when adding a node type)", got, want)
	}
}
