// Package compat — veritize.go
//
// C7 T4d: Veritize verification-flow adapter.
//
// Veritize ships a /v1/verify endpoint whose VerifyRequest carries a
// Claim + optional Context + EnforcementMode, and whose
// VerifyResponse returns Verdict + Confidence + Sources +
// Reasoning. See /home/eric/repos/veritize/relayone/veritize/
// internal/api/dto.go (VerifyRequest / VerifyResponse).
//
// The mapping:
//
//   - R1 inputSchema.properties.context  -> Veritize VerifyRequest.Context
//   - R1 outputSchema.guidance           -> Veritize VerifyResponse
//                                           (mapped as a Source +
//                                           Reasoning by the consumer
//                                           per the spec's Findings shape)
//   - pack-signature failure             -> Veritize "subject untrusted"
//                                           verdict on the
//                                           VerifyResponse.
package compat

import (
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/skill"
)

func init() {
	Register("veritize", AdaptVeritize)
}

// veritizeAllowedAssertions enumerates the closed-set tokens the
// Veritize adapter understands.
var veritizeAllowedAssertions = map[string]struct{}{
	"enforcement_advisory": {},
	"enforcement_block":    {},
	"enforcement_warn":     {},
	"sources_required":     {},
	"reasoning_required":   {},
}

// VeritizeDescriptor mirrors the verification-flow shape consumer
// teams import from veritize/relayone/veritize/internal/api/dto.go.
type VeritizeDescriptor struct {
	Name               string            `json:"name"`
	Description        string            `json:"description,omitempty"`
	EnforcementMode    string            `json:"enforcement_mode,omitempty"`
	RequiresSources    bool              `json:"requires_sources"`
	RequiresReasoning  bool              `json:"requires_reasoning"`
	ArgShape           veritizeArgShape  `json:"arg_shape"`
	ReturnShape        veritizeRetShape  `json:"return_shape"`
	ErrorMap           map[string]string `json:"error_map"`
	SignatureAuthority string            `json:"signature_authority,omitempty"`
}

type veritizeArgShape struct {
	R1Source       string `json:"r1_source"`
	VeritizeTarget string `json:"veritize_target"`
}

type veritizeRetShape struct {
	R1Source       string `json:"r1_source"`
	VeritizeTarget string `json:"veritize_target"`
}

// AdaptVeritize maps an R1 v2 manifest into a Veritize verification
// step descriptor.
func AdaptVeritize(manifest *skill.ManifestV2) ([]byte, error) {
	if err := manifest.CheckCompat("veritize"); err != nil {
		return nil, fmt.Errorf("compat veritize: %w", err)
	}
	if err := validateAssertions(manifest, "veritize", veritizeAllowedAssertions); err != nil {
		return nil, err
	}

	mode := "advisory"
	requiresSources := false
	requiresReasoning := false
	for _, token := range manifest.AssertionsFor("veritize") {
		switch token {
		case "enforcement_advisory":
			mode = "advisory"
		case "enforcement_block":
			mode = "block"
		case "enforcement_warn":
			mode = "warn"
		case "sources_required":
			requiresSources = true
		case "reasoning_required":
			requiresReasoning = true
		}
	}

	desc := VeritizeDescriptor{
		Name:              manifest.Name,
		Description:       manifest.Description,
		EnforcementMode:   mode,
		RequiresSources:   requiresSources,
		RequiresReasoning: requiresReasoning,
		ArgShape: veritizeArgShape{
			R1Source:       "inputSchema.properties.context",
			VeritizeTarget: "VerifyRequest.Context",
		},
		ReturnShape: veritizeRetShape{
			R1Source:       "outputSchema.guidance",
			VeritizeTarget: "VerifyResponse.Findings",
		},
		ErrorMap: map[string]string{
			"ErrPackSignatureInvalid": "subject_untrusted",
		},
		SignatureAuthority: string(manifest.SignatureAuthority),
	}
	return json.MarshalIndent(desc, "", "  ")
}
