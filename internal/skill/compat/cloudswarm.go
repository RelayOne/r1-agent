// Package compat — cloudswarm.go
//
// C7 T4b: CloudSwarm runtime adapter.
//
// Mirrors the on-disk SkillDefinition contract from
// /home/eric/repos/CloudSwarm/platform/skills/core/base.py (fields
// name, display_name, description, params_schema,
// trust_level_required, tags, triggers, version). We don't import
// the CloudSwarm Python module — we re-state the contract in Go and
// emit JSON CloudSwarm's loader can ingest unchanged.
//
// Arg / return shape transforms (per spec T4b):
//   - R1 inputSchema.properties.context  -> CloudSwarm params.context
//   - R1 outputSchema.guidance           -> CloudSwarm result.guidance
//   - R1 ErrPackSignatureInvalid         -> CloudSwarm trust-level violation
//
// The R1 v2 manifest does NOT directly carry inputSchema /
// outputSchema (those live in the per-skill manifest.json in the
// pack root). The adapter therefore emits the manifest-level
// metadata + a documented hint about the per-skill argument-shape
// mapping consumers MUST apply at invocation time. Tests in
// cloudswarm_test.go pin the contract.
package compat

import (
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/skill"
)

func init() {
	Register("cloudswarm", AdaptCloudSwarm)
}

// cloudswarmAllowedAssertions enumerates the closed-set tokens the
// CloudSwarm adapter understands. New tokens require a new spec PR.
var cloudswarmAllowedAssertions = map[string]struct{}{
	"trust_low":    {},
	"trust_medium": {},
	"trust_high":   {},
	"async_only":   {},
	"sync_only":    {},
}

// CloudSwarmDescriptor mirrors CloudSwarm SkillDefinition (Python
// dataclass) read directly from
// /home/eric/repos/CloudSwarm/platform/skills/core/base.py. Field
// names (name, display_name, description, category, version,
// trust_level_required, tags, triggers, params_schema) match the
// upstream Python dataclass verbatim so CloudSwarm's loader ingests
// the emitted JSON unchanged. The CloudSwarm-side conformance suite
// in its own repo pins the same field set; drift on either side
// fails the round-trip.
type CloudSwarmDescriptor struct {
	Name               string            `json:"name"`
	DisplayName        string            `json:"display_name"`
	Description        string            `json:"description,omitempty"`
	Category           string            `json:"category"`
	Version            string            `json:"version"`
	TrustLevelRequired int               `json:"trust_level_required"`
	Tags               []string          `json:"tags,omitempty"`
	Triggers           []string          `json:"triggers,omitempty"`
	ParamsSchema       []cloudswarmParam `json:"params_schema"`
	ArgShape           argShapeMapping   `json:"arg_shape"`
	ReturnShape        returnShapeMapping `json:"return_shape"`
	ErrorMap           map[string]string `json:"error_map"`
	SignatureAuthority string            `json:"signature_authority,omitempty"`
}

type cloudswarmParam struct {
	Name        string `json:"name"`
	InputType   string `json:"input_type"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"`
}

type argShapeMapping struct {
	// R1 input field that maps onto CloudSwarm params.context.
	R1Source         string `json:"r1_source"`
	CloudSwarmTarget string `json:"cloudswarm_target"`
}

type returnShapeMapping struct {
	R1Source         string `json:"r1_source"`
	CloudSwarmTarget string `json:"cloudswarm_target"`
}

// AdaptCloudSwarm maps an R1 v2 manifest into a CloudSwarm
// SkillDefinition JSON descriptor.
func AdaptCloudSwarm(manifest *skill.ManifestV2) ([]byte, error) {
	if err := manifest.CheckCompat("cloudswarm"); err != nil {
		return nil, fmt.Errorf("compat cloudswarm: %w", err)
	}
	if err := validateAssertions(manifest, "cloudswarm", cloudswarmAllowedAssertions); err != nil {
		return nil, err
	}

	trustLevel := 0
	for _, token := range manifest.AssertionsFor("cloudswarm") {
		switch token {
		case "trust_low":
			trustLevel = 1
		case "trust_medium":
			trustLevel = 2
		case "trust_high":
			trustLevel = 3
		}
	}

	desc := CloudSwarmDescriptor{
		Name:               manifest.Name,
		DisplayName:        manifest.Name,
		Description:        manifest.Description,
		Category:           "r1-federated",
		Version:            manifest.Version,
		TrustLevelRequired: trustLevel,
		Tags:               []string{"r1", "federated"},
		Triggers:           []string{},
		ParamsSchema: []cloudswarmParam{
			{
				Name:        "context",
				InputType:   "text",
				Type:        "str",
				Required:    true,
				Description: "Operator request context propagated from R1 inputSchema.properties.context.",
			},
		},
		ArgShape: argShapeMapping{
			R1Source:         "inputSchema.properties.context",
			CloudSwarmTarget: "params.context",
		},
		ReturnShape: returnShapeMapping{
			R1Source:         "outputSchema.guidance",
			CloudSwarmTarget: "result.guidance",
		},
		ErrorMap: map[string]string{
			"ErrPackSignatureInvalid": "trust_level_violation",
		},
		SignatureAuthority: string(manifest.SignatureAuthority),
	}
	return json.MarshalIndent(desc, "", "  ")
}
