// Package compat — heroa.go
//
// C7 T4c: Heroa template runtime adapter.
//
// Heroa templates are slug-keyed deployment descriptors with a
// parameter schema, optional region allowlist, and an upstream
// substrate-version pin. We map the R1 v2 manifest onto:
//
//   - slug                = manifest.Name
//   - description         = manifest.Description
//   - substrate_min_version = manifest.MinR1Version
//   - parameters          = inferred from manifest.ConsumerHooks
//                           transform_args payload schemas
//   - region_allowlist    = manifest.RuntimeAssertions["heroa"]
//                           tokens of the form "region:<id>"
//   - errors              = R1 load errors map to Heroa-style
//                           structured errors analogous to
//                           TemplateRegionExcludedError.
//
// Source for the consumer-side template shape:
// /home/eric/repos/heroa/cmd/heroa/deploy.go +
// /home/eric/repos/heroa/internal/region/. The Go struct emitted
// here is a re-statement of those types as a JSON descriptor;
// Heroa's own loader on the consumer side is the source of truth.
package compat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/skill"
)

func init() {
	Register("heroa", AdaptHeroa)
}

// heroaAllowedAssertions enumerates the closed-set tokens the
// Heroa adapter understands. Region-prefixed tokens ("region:us-east")
// are also accepted via runtime check below (no allowlist explosion).
var heroaAllowedAssertions = map[string]struct{}{
	"public":      {},
	"private":     {},
	"region_us":   {},
	"region_eu":   {},
	"region_apac": {},
}

// heroaRegionPrefix marks a runtime_assertions token as a region
// allowlist entry. The portion after the colon is the region id.
const heroaRegionPrefix = "region:"

// HeroaTemplateDescriptor mirrors the on-disk Heroa template shape.
type HeroaTemplateDescriptor struct {
	Slug                  string            `json:"slug"`
	Description           string            `json:"description,omitempty"`
	SubstrateMinVersion   string            `json:"substrate_min_version,omitempty"`
	Parameters            []heroaParameter  `json:"parameters,omitempty"`
	RegionAllowlist       []string          `json:"region_allowlist,omitempty"`
	PublicTemplate        bool              `json:"public,omitempty"`
	PrivateTemplate       bool              `json:"private,omitempty"`
	ErrorTypes            map[string]string `json:"error_types"`
	SignatureAuthority    string            `json:"signature_authority,omitempty"`
}

type heroaParameter struct {
	Name          string          `json:"name"`
	HookName      string          `json:"hook_name"`
	PayloadSchema json.RawMessage `json:"payload_schema"`
	Optional      bool            `json:"optional"`
}

// AdaptHeroa maps an R1 v2 manifest into a Heroa template descriptor.
func AdaptHeroa(manifest *skill.ManifestV2) ([]byte, error) {
	if err := manifest.CheckCompat("heroa"); err != nil {
		return nil, fmt.Errorf("compat heroa: %w", err)
	}
	// Heroa allows two forms of assertion: closed-set tokens above
	// AND region:<id> tokens. Validate the non-region ones strictly.
	for _, token := range manifest.AssertionsFor("heroa") {
		if strings.HasPrefix(token, heroaRegionPrefix) {
			continue
		}
		if _, ok := heroaAllowedAssertions[token]; !ok {
			return nil, fmt.Errorf("compat: pack %q runtime_assertions[heroa] contains unknown token %q",
				manifest.Name, token)
		}
	}

	desc := HeroaTemplateDescriptor{
		Slug:                manifest.Name,
		Description:         manifest.Description,
		SubstrateMinVersion: manifest.MinR1Version,
		ErrorTypes: map[string]string{
			"region_excluded":   "TemplateRegionExcludedError",
			"signature_invalid": "TemplatePackSignatureError",
		},
		SignatureAuthority: string(manifest.SignatureAuthority),
	}

	for _, token := range manifest.AssertionsFor("heroa") {
		switch {
		case token == "public":
			desc.PublicTemplate = true
		case token == "private":
			desc.PrivateTemplate = true
		case strings.HasPrefix(token, heroaRegionPrefix):
			region := strings.TrimPrefix(token, heroaRegionPrefix)
			if region != "" {
				desc.RegionAllowlist = append(desc.RegionAllowlist, region)
			}
		case strings.HasPrefix(token, "region_"):
			desc.RegionAllowlist = append(desc.RegionAllowlist, strings.TrimPrefix(token, "region_"))
		}
	}

	// Surface manifest.ConsumerHooks as Heroa template parameters.
	// Stable order so tests stay deterministic.
	hookNames := make([]string, 0, len(manifest.ConsumerHooks))
	for name := range manifest.ConsumerHooks {
		hookNames = append(hookNames, name)
	}
	sortStrings(hookNames)
	for _, name := range hookNames {
		hook := manifest.ConsumerHooks[name]
		// Heroa parameters are derived from transform_args hooks per
		// spec T4c; others remain visible to the consumer for context
		// but only transform_args drives the parameter schema. We
		// emit all of them with a flag-via-hookKind discriminator so
		// the Heroa loader can decide.
		desc.Parameters = append(desc.Parameters, heroaParameter{
			Name:          name + "." + string(hook.Kind),
			HookName:      name,
			PayloadSchema: hook.PayloadSchema,
			Optional:      hook.Optional,
		})
	}
	return json.MarshalIndent(desc, "", "  ")
}

func sortStrings(in []string) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j-1] > in[j]; j-- {
			in[j-1], in[j] = in[j], in[j-1]
		}
	}
}
