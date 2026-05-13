package compat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/skill"
)

func TestAdaptVeritize_Happy(t *testing.T) {
	t.Parallel()
	m := &skill.ManifestV2{
		SchemaVersion: skill.ManifestSchemaVersionV2,
		Name:          "vp", Version: "1.0",
		Description: "verifier",
		Compat:      []string{"veritize"},
		RuntimeAssertions: map[string][]string{
			"veritize": {"enforcement_block", "sources_required"},
		},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	out, err := AdaptVeritize(m)
	if err != nil {
		t.Fatalf("AdaptVeritize: %v", err)
	}
	var desc VeritizeDescriptor
	if err := json.Unmarshal(out, &desc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if desc.EnforcementMode != "block" {
		t.Fatalf("EnforcementMode = %q", desc.EnforcementMode)
	}
	if !desc.RequiresSources {
		t.Fatalf("RequiresSources = false")
	}
	if desc.ArgShape.R1Source != "inputSchema.properties.context" ||
		desc.ArgShape.VeritizeTarget != "VerifyRequest.Context" {
		t.Fatalf("ArgShape = %+v", desc.ArgShape)
	}
	if desc.ErrorMap["ErrPackSignatureInvalid"] != "subject_untrusted" {
		t.Fatalf("ErrorMap = %+v", desc.ErrorMap)
	}
}

func TestAdaptVeritize_RefusesWithoutCompat(t *testing.T) {
	t.Parallel()
	m := &skill.ManifestV2{Name: "x", Version: "1", Compat: []string{"r1"}}
	if _, err := AdaptVeritize(m); err == nil {
		t.Fatalf("want err, got nil")
	}
}

func TestAdaptVeritize_RejectsUnknownAssertion(t *testing.T) {
	t.Parallel()
	m := &skill.ManifestV2{
		Name: "x", Version: "1",
		Compat:            []string{"veritize"},
		RuntimeAssertions: map[string][]string{"veritize": {"bogus"}},
	}
	if _, err := AdaptVeritize(m); err == nil || !strings.Contains(err.Error(), "unknown token") {
		t.Fatalf("want unknown err, got %v", err)
	}
}
