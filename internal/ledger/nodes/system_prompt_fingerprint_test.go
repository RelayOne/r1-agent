package nodes

import (
	"testing"
	"time"
)

// TestSystemPromptFPNode_ContentHashStable asserts that the
// content-hash digest is deterministic across two calls with the same
// inputs and that mutating one load-bearing field produces a
// different hash.
func TestSystemPromptFPNode_ContentHashStable(t *testing.T) {
	n1 := &SystemPromptFingerprint{
		PromptSHA256:   "abc123",
		KeyFingerprint: "ffeeddcc",
		Signature:      "deadbeef",
		SignedAt:       time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
		Version:        1,
	}
	n2 := &SystemPromptFingerprint{
		PromptSHA256:   "abc123",
		KeyFingerprint: "ffeeddcc",
		Signature:      "deadbeef",
		SignedAt:       time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
		Version:        1,
	}
	if n1.ContentHash() != n2.ContentHash() {
		t.Errorf("ContentHash not stable: %s vs %s", n1.ContentHash(), n2.ContentHash())
	}
	// Mutate prompt SHA → expect different hash.
	n3 := *n1
	n3.PromptSHA256 = "abc124"
	if n1.ContentHash() == n3.ContentHash() {
		t.Errorf("ContentHash should differ after PromptSHA256 mutation")
	}
}

// TestSystemPromptFPNode_RegisteredInFactory asserts the node type
// surfaced through the package-level registry so ledger consumers
// that range over node types pick it up.
func TestSystemPromptFPNode_RegisteredInFactory(t *testing.T) {
	got, err := New("system_prompt_fingerprint")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got.NodeType() != "system_prompt_fingerprint" {
		t.Errorf("NodeType = %q, want system_prompt_fingerprint", got.NodeType())
	}
	if got.SchemaVersion() != 1 {
		t.Errorf("SchemaVersion = %d, want 1", got.SchemaVersion())
	}
}

// TestSystemPromptFPNode_ValidateRequiresFields asserts the operator-
// facing required-field guards fire as documented.
func TestSystemPromptFPNode_ValidateRequiresFields(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*SystemPromptFingerprint)
	}{
		{"missing_sha", func(n *SystemPromptFingerprint) { n.PromptSHA256 = "" }},
		{"missing_keyfp", func(n *SystemPromptFingerprint) { n.KeyFingerprint = "" }},
		{"missing_sig", func(n *SystemPromptFingerprint) { n.Signature = "" }},
		{"missing_signed_at", func(n *SystemPromptFingerprint) { n.SignedAt = time.Time{} }},
		{"version_zero", func(n *SystemPromptFingerprint) { n.Version = 0 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := &SystemPromptFingerprint{
				PromptSHA256:   "abc",
				KeyFingerprint: "fp",
				Signature:      "sig",
				SignedAt:       time.Now().UTC(),
				Version:        1,
			}
			c.mut(n)
			if err := n.Validate(); err == nil {
				t.Error("want validation error")
			}
		})
	}
}
