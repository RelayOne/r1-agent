package a2a

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuild_Defaults(t *testing.T) {
	card := Build(Options{
		Name:    "stoke",
		Version: "0.1.0",
		URL:     "https://example.com/a2a",
		Capabilities: []CapabilityRef{
			{Name: "code_review", Version: "1"},
		},
	})
	if card.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocolVersion=%q want %q", card.ProtocolVersion, ProtocolVersion)
	}
	if card.IssuedAt.IsZero() {
		t.Error("issuedAt should default to now, got zero")
	}
	if card.ExpiresAt != nil {
		t.Errorf("expiresAt should be nil when TTL=0, got %v", card.ExpiresAt)
	}
	if len(card.DefaultInputModes) == 0 {
		t.Error("defaultInputModes should fall back to [text/plain, application/json]")
	}
}

func TestBuild_TTL(t *testing.T) {
	issued := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	card := Build(Options{
		Name:     "x",
		Version:  "1",
		IssuedAt: issued,
		TTL:      24 * time.Hour,
	})
	if card.ExpiresAt == nil {
		t.Fatal("expiresAt should be set when TTL>0")
	}
	if card.ExpiresAt.Sub(issued) != 24*time.Hour {
		t.Errorf("expiresAt off by %v", card.ExpiresAt.Sub(issued)-24*time.Hour)
	}
}

func TestAgentCard_JSONStructure(t *testing.T) {
	card := Build(Options{
		Name:        "stoke-reviewer",
		Description: "cross-model reviewer",
		Version:     "0.2.0",
		URL:         "https://example.com/a2a",
		Identity: AgentIdentity{
			PublicKeyPEM: "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA...\n-----END PUBLIC KEY-----\n",
			DID:          "did:tp:stoke-001",
			StanceRole:   "reviewer",
		},
		Capabilities: []CapabilityRef{
			{Name: "review", Version: "1", ManifestHash: "sha256:abc"},
		},
		Endpoints: AgentEndpoints{JSONRPC: "https://example.com/rpc"},
	})
	b, err := card.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"protocolVersion"`,
		`"name": "stoke-reviewer"`,
		`"did": "did:tp:stoke-001"`,
		`"_stoke.dev/stance_role": "reviewer"`,
		`"jsonrpc": "https://example.com/rpc"`,
		`"manifestHash": "sha256:abc"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing substring %q; got:\n%s", want, s)
		}
	}
	// Round-trip parse so we know the JSON is well-formed.
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
}

func TestAgentCard_WriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.json")
	card := Build(Options{Name: "x", Version: "1"})
	if err := card.WriteFile(path); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode=%v want 0644", info.Mode().Perm())
	}
	// Confirm the .tmp sidecar was renamed (atomic semantics).
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected no .tmp sidecar, got err=%v", err)
	}
}
