package compat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/skill"
)

func TestAdaptCloudSwarm_Happy(t *testing.T) {
	t.Parallel()
	m := &skill.ManifestV2{
		SchemaVersion: skill.ManifestSchemaVersionV2,
		Name:          "alpha", Version: "1.0",
		Description: "alpha pack",
		Compat:      []string{"r1", "cloudswarm"},
		RuntimeAssertions: map[string][]string{
			"cloudswarm": {"trust_high"},
		},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	out, err := AdaptCloudSwarm(m)
	if err != nil {
		t.Fatalf("AdaptCloudSwarm: %v", err)
	}
	var desc CloudSwarmDescriptor
	if err := json.Unmarshal(out, &desc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if desc.TrustLevelRequired != 3 {
		t.Fatalf("TrustLevelRequired = %d, want 3", desc.TrustLevelRequired)
	}
	if desc.ArgShape.R1Source != "inputSchema.properties.context" ||
		desc.ArgShape.CloudSwarmTarget != "params.context" {
		t.Fatalf("ArgShape = %+v", desc.ArgShape)
	}
	if desc.ReturnShape.R1Source != "outputSchema.guidance" ||
		desc.ReturnShape.CloudSwarmTarget != "result.guidance" {
		t.Fatalf("ReturnShape = %+v", desc.ReturnShape)
	}
	if desc.ErrorMap["ErrPackSignatureInvalid"] != "trust_level_violation" {
		t.Fatalf("ErrorMap = %+v", desc.ErrorMap)
	}
	if len(desc.ParamsSchema) != 1 || desc.ParamsSchema[0].Name != "context" {
		t.Fatalf("ParamsSchema = %+v", desc.ParamsSchema)
	}
}

func TestAdaptCloudSwarm_RefusesWithoutCompat(t *testing.T) {
	t.Parallel()
	m := &skill.ManifestV2{Name: "x", Version: "1", Compat: []string{"r1"}}
	if _, err := AdaptCloudSwarm(m); err == nil {
		t.Fatalf("want err, got nil")
	}
}

func TestAdaptCloudSwarm_RejectsUnknownAssertion(t *testing.T) {
	t.Parallel()
	m := &skill.ManifestV2{
		Name: "x", Version: "1",
		Compat:            []string{"cloudswarm"},
		RuntimeAssertions: map[string][]string{"cloudswarm": {"bogus_token"}},
	}
	if _, err := AdaptCloudSwarm(m); err == nil || !strings.Contains(err.Error(), "unknown token") {
		t.Fatalf("want unknown err, got %v", err)
	}
}

func TestAdaptCloudSwarm_ArgShapeTransform(t *testing.T) {
	t.Parallel()
	// The argument-shape transform is documented in the descriptor;
	// asserting it matches the spec's mapping is the contract test.
	m := &skill.ManifestV2{Name: "shape", Version: "1", Compat: []string{"cloudswarm"}}
	out, err := AdaptCloudSwarm(m)
	if err != nil {
		t.Fatalf("AdaptCloudSwarm: %v", err)
	}
	if !strings.Contains(string(out), `"params.context"`) {
		t.Fatalf("output missing params.context: %s", out)
	}
	if !strings.Contains(string(out), `"result.guidance"`) {
		t.Fatalf("output missing result.guidance: %s", out)
	}
}
