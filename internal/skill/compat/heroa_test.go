package compat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/skill"
)

func TestAdaptHeroa_Happy(t *testing.T) {
	t.Parallel()
	m := &skill.ManifestV2{
		SchemaVersion: skill.ManifestSchemaVersionV2,
		Name:          "deploy-pack", Version: "1.0", Description: "deploy",
		MinR1Version: "0.5",
		Compat:       []string{"heroa"},
		RuntimeAssertions: map[string][]string{
			"heroa": {"region:us-east", "region:eu-west", "public"},
		},
		ConsumerHooks: map[string]skill.HookSpec{
			"prep": {Kind: skill.HookKindTransformArgs, PayloadSchema: json.RawMessage(`{"a":1}`)},
		},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	out, err := AdaptHeroa(m)
	if err != nil {
		t.Fatalf("AdaptHeroa: %v", err)
	}
	var desc HeroaTemplateDescriptor
	if err := json.Unmarshal(out, &desc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if desc.Slug != "deploy-pack" {
		t.Fatalf("Slug = %q", desc.Slug)
	}
	if desc.SubstrateMinVersion != "0.5" {
		t.Fatalf("SubstrateMinVersion = %q", desc.SubstrateMinVersion)
	}
	if !desc.PublicTemplate {
		t.Fatalf("PublicTemplate = false")
	}
	if len(desc.RegionAllowlist) != 2 {
		t.Fatalf("RegionAllowlist = %v", desc.RegionAllowlist)
	}
	if len(desc.Parameters) != 1 || desc.Parameters[0].HookName != "prep" {
		t.Fatalf("Parameters = %+v", desc.Parameters)
	}
	if desc.ErrorTypes["region_excluded"] != "TemplateRegionExcludedError" {
		t.Fatalf("ErrorTypes = %+v", desc.ErrorTypes)
	}
}

func TestAdaptHeroa_RefusesWithoutCompat(t *testing.T) {
	t.Parallel()
	m := &skill.ManifestV2{Name: "x", Version: "1", Compat: []string{"r1"}}
	if _, err := AdaptHeroa(m); err == nil {
		t.Fatalf("want err, got nil")
	}
}

func TestAdaptHeroa_RejectsUnknownAssertion(t *testing.T) {
	t.Parallel()
	m := &skill.ManifestV2{
		Name: "x", Version: "1",
		Compat:            []string{"heroa"},
		RuntimeAssertions: map[string][]string{"heroa": {"bogus_token"}},
	}
	if _, err := AdaptHeroa(m); err == nil || !strings.Contains(err.Error(), "unknown token") {
		t.Fatalf("want unknown err, got %v", err)
	}
}
