package skillmfr

import (
	"encoding/json"
	"strings"
	"testing"
)

// Tests for the RecommendedFor field added per
// CLOUDSWARM-R1-INTEGRATION spec §2.9 / §5.6. Kept in a separate
// file from the main manifest_test.go so the existing test
// fixture (validManifest) stays untouched — these tests consume
// the fixture without altering it.

func TestManifest_RecommendedFor_RoundTripsJSON(t *testing.T) {
	m := validManifest()
	m.RecommendedFor = []string{"landing-page", "cold-email"}

	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got Manifest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.RecommendedFor) != 2 {
		t.Fatalf("RecommendedFor len=%d want 2", len(got.RecommendedFor))
	}
	if got.RecommendedFor[0] != "landing-page" || got.RecommendedFor[1] != "cold-email" {
		t.Errorf("RecommendedFor=%v want [landing-page cold-email]", got.RecommendedFor)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate after round-trip: %v", err)
	}
}

func TestManifest_RecommendedFor_EmptyOmittedFromJSON(t *testing.T) {
	// omitempty: manifests without RecommendedFor should not
	// serialize the field. Verifies backward-compatibility with
	// fixtures that predate the field.
	m := validManifest()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), "recommendedFor") {
		t.Errorf("empty RecommendedFor should be omitted, got: %s", string(raw))
	}
}

func TestManifest_RecommendedFor_BackwardCompatUnmarshalMissing(t *testing.T) {
	// Fixture-shape JSON without the recommendedFor key (as
	// existing registered manifests do) must unmarshal to an
	// empty slice and pass Validate.
	legacy := `{
		"name":"code-search",
		"version":"1.0.0",
		"description":"Search the codebase for symbols",
		"inputSchema":{"type":"object","properties":{"query":{"type":"string"}}},
		"outputSchema":{"type":"object","properties":{"results":{"type":"array"}}},
		"whenToUse":["find a function by name"],
		"whenNotToUse":["modify source files","execute code"],
		"behaviorFlags":{"mutatesState":false,"requiresNetwork":false}
	}`
	var m Manifest
	if err := json.Unmarshal([]byte(legacy), &m); err != nil {
		t.Fatalf("Unmarshal legacy: %v", err)
	}
	if m.RecommendedFor != nil {
		t.Errorf("want nil RecommendedFor on legacy manifest, got %v", m.RecommendedFor)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("legacy manifest should Validate: %v", err)
	}
}

func TestComputeHash_ChangesOnRecommendedFor(t *testing.T) {
	m := validManifest()
	h1, err := m.ComputeHash()
	if err != nil {
		t.Fatalf("ComputeHash: %v", err)
	}
	m.RecommendedFor = []string{"landing-page"}
	h2, _ := m.ComputeHash()
	if h1 == h2 {
		t.Error("hash should change when RecommendedFor is populated")
	}
	m.RecommendedFor = []string{"landing-page", "cold-email"}
	h3, _ := m.ComputeHash()
	if h2 == h3 {
		t.Error("hash should change when RecommendedFor entries change")
	}
}

func TestComputeHash_EmptyRecommendedForMatchesLegacy(t *testing.T) {
	// A fresh manifest with RecommendedFor left nil should hash
	// identically regardless of whether the field exists in the
	// struct — omitempty keeps it out of the canonical JSON so
	// legacy (pre-field) manifest hashes remain stable.
	m1 := validManifest()
	m2 := validManifest()
	m2.RecommendedFor = nil
	h1, _ := m1.ComputeHash()
	h2, _ := m2.ComputeHash()
	if h1 != h2 {
		t.Errorf("nil vs unset RecommendedFor produced different hashes: %q vs %q", h1, h2)
	}
}
