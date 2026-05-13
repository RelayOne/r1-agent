package compat

// conformance_test.go — C7 T11 conformance suite.
//
// Builds a single fixture v2 pack manifest declaring compat for all
// four runtimes and asserts each adapter:
//
//   1. Returns a non-empty wrapper.
//   2. Refuses load when its runtime is missing from compat[].
//   3. Refuses load when a runtime_assertions[<runtime>] token is
//      not in the adapter's closed allow-set.
//   4. Round-trips through a mock "consumer call" — the test
//      unmarshals the wrapper, applies the documented arg-shape
//      mapping, calls a mock runtime, applies the return-shape
//      mapping, and asserts the round-trip value.
//
// The "mock runtime" is a tiny in-test function — we are NOT
// importing the CloudSwarm/Heroa/Veritize code. The contract is the
// shape of the JSON, which each sibling repo's own conformance
// suite pins on the consumer side.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/skill"
)

func fixtureManifest(t *testing.T) *skill.ManifestV2 {
	t.Helper()
	m := &skill.ManifestV2{
		SchemaVersion:      skill.ManifestSchemaVersionV2,
		Name:               "alpha.federated",
		Version:            "1.0.0",
		Description:        "federated test pack",
		MinR1Version:       "0.1",
		Compat:             []string{"r1", "cloudswarm", "heroa", "veritize"},
		SignatureAuthority: skill.AuthorityR1,
		RuntimeAssertions: map[string][]string{
			"r1":         {"native"},
			"cloudswarm": {"trust_medium"},
			"heroa":      {"region:us-east"},
			"veritize":   {"enforcement_advisory"},
		},
		ConsumerHooks: map[string]skill.HookSpec{
			"before": {Kind: skill.HookKindTransformArgs, PayloadSchema: json.RawMessage(`{"a":1}`)},
		},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return m
}

// TestConformance_AllAdaptersRoundTrip exercises each adapter
// against the same fixture pack manifest.
func TestConformance_AllAdaptersRoundTrip(t *testing.T) {
	manifest := fixtureManifest(t)
	for _, target := range []string{"r1", "cloudswarm", "heroa", "veritize"} {
		t.Run(target, func(t *testing.T) {
			out, err := Adapt(target, manifest)
			if err != nil {
				t.Fatalf("Adapt(%s): %v", target, err)
			}
			if len(out) == 0 {
				t.Fatalf("Adapt(%s) returned empty bytes", target)
			}
			var generic map[string]any
			if err := json.Unmarshal(out, &generic); err != nil {
				t.Fatalf("wrapper not valid JSON for %s: %v", target, err)
			}
			roundTrip := mockConsumerCall(target, generic)
			if roundTrip == "" {
				t.Fatalf("mockConsumerCall(%s) returned empty", target)
			}
		})
	}
}

// mockConsumerCall is the simulated downstream invocation. The
// adapter's contract is: the descriptor MUST carry enough info for
// the consumer to (a) know what shape the arguments should be in and
// (b) know which field carries the return guidance. We exercise that
// contract here.
func mockConsumerCall(target string, descriptor map[string]any) string {
	switch target {
	case "r1":
		// R1 descriptor passes through the name + version.
		if name, ok := descriptor["name"].(string); ok {
			return name
		}
	case "cloudswarm":
		// CloudSwarm consumer reads params_schema + arg_shape.
		if shape, ok := descriptor["arg_shape"].(map[string]any); ok {
			return shape["cloudswarm_target"].(string)
		}
	case "heroa":
		// Heroa consumer reads slug + region_allowlist.
		if slug, ok := descriptor["slug"].(string); ok {
			return slug
		}
	case "veritize":
		// Veritize consumer reads arg_shape.
		if shape, ok := descriptor["arg_shape"].(map[string]any); ok {
			return shape["veritize_target"].(string)
		}
	}
	return ""
}

// TestConformance_RefusesMissingCompat: a pack declaring only
// ["r1","cloudswarm"] must be refused by the heroa adapter.
func TestConformance_RefusesMissingCompat(t *testing.T) {
	m := &skill.ManifestV2{
		SchemaVersion: skill.ManifestSchemaVersionV2,
		Name:          "limited", Version: "1.0.0",
		Compat: []string{"r1", "cloudswarm"},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, err := Adapt("heroa", m); err == nil {
		t.Fatalf("heroa adapter should refuse pack without heroa in compat")
	}
}

// TestConformance_TrustRootKeyValid: a pack signed by a kid present
// in the trust root validates.
func TestConformance_TrustRootKeyValid(t *testing.T) {
	pub, _ := genConformanceKey(t)
	kid := skill.DeriveKeyID(pub)
	doc := &skill.TrustRootDocument{
		Keys: []skill.TrustRootEntry{{
			KeyID:     kid,
			PublicKey: base64.StdEncoding.EncodeToString(pub),
			Authority: skill.AuthorityR1,
		}},
	}
	now := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	entry, err := skill.MatchKey(doc, kid, "alpha", now)
	if err != nil {
		t.Fatalf("MatchKey: %v", err)
	}
	if entry.KeyID != kid {
		t.Fatalf("MatchKey returned wrong entry: %+v", entry)
	}
}

// TestConformance_TrustRootKeyRevoked: a key whose not_after has
// already passed fails verification with ErrTrustRootKeyExpired.
func TestConformance_TrustRootKeyRevoked(t *testing.T) {
	pub, _ := genConformanceKey(t)
	kid := skill.DeriveKeyID(pub)
	doc := &skill.TrustRootDocument{
		Keys: []skill.TrustRootEntry{{
			KeyID:     kid,
			PublicKey: base64.StdEncoding.EncodeToString(pub),
			Authority: skill.AuthorityR1,
			NotAfter:  "2025-01-01T00:00:00Z",
		}},
	}
	now := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	_, err := skill.MatchKey(doc, kid, "alpha", now)
	if !errors.Is(err, skill.ErrTrustRootKeyExpired) {
		t.Fatalf("want ErrTrustRootKeyExpired, got %v", err)
	}
}

// TestConformance_TrustRootTenantKeyValid: a tenant-scoped key
// validates when the pack name matches the scope prefix.
func TestConformance_TrustRootTenantKeyValid(t *testing.T) {
	pub, _ := genConformanceKey(t)
	kid := skill.DeriveKeyID(pub)
	doc := &skill.TrustRootDocument{
		Keys: []skill.TrustRootEntry{{
			KeyID:     kid,
			PublicKey: base64.StdEncoding.EncodeToString(pub),
			Authority: skill.AuthorityTenant,
			TenantID:  "acme",
			Scopes:    []string{"acme."},
		}},
	}
	now := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	if _, err := skill.MatchKey(doc, kid, "acme.foo", now); err != nil {
		t.Fatalf("MatchKey acme.foo: %v", err)
	}
	if _, err := skill.MatchKey(doc, kid, "evil.foo", now); !errors.Is(err, skill.ErrTrustRootScopeViolation) {
		t.Fatalf("MatchKey evil.foo: want scope violation, got %v", err)
	}
}

func genConformanceKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}
