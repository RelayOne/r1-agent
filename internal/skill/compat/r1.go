// Package compat — r1.go
//
// C7 T4a: R1 native adapter (passthrough baseline).
//
// The R1 runtime IS the canonical substrate, so the adapter is an
// identity transform: input manifest -> JSON descriptor that mirrors
// the manifest's fields verbatim. The conformance suite uses this
// as the baseline against which the sibling adapters are compared.
package compat

import (
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/skill"
)

func init() {
	Register("r1", AdaptR1)
}

// r1AllowedAssertions enumerates the closed-set assertion tokens
// R1's own runtime understands. Unknown tokens fail load — see
// validateAssertions.
var r1AllowedAssertions = map[string]struct{}{
	"native":      {},
	"vetted":      {},
	"trust_root":  {},
	"strict_mcp":  {},
	"sandbox_on":  {},
	"sandbox_off": {},
}

// R1Descriptor is the on-disk shape AdaptR1 emits. Stable so
// downstream R1 tooling can deserialize it without re-parsing the
// full v2 manifest.
type R1Descriptor struct {
	Runtime            string                `json:"runtime"`
	Name               string                `json:"name"`
	Version            string                `json:"version"`
	Description        string                `json:"description,omitempty"`
	MinR1Version       string                `json:"min_r1_version,omitempty"`
	Compat             []string              `json:"compat"`
	Dependencies       []string              `json:"dependencies,omitempty"`
	Assertions         []string              `json:"assertions,omitempty"`
	ConsumerHooks      map[string]hookEcho   `json:"consumer_hooks,omitempty"`
	SignatureAuthority string                `json:"signature_authority,omitempty"`
}

type hookEcho struct {
	Kind          string          `json:"kind"`
	PayloadSchema json.RawMessage `json:"payload_schema,omitempty"`
	Optional      bool            `json:"optional,omitempty"`
}

// AdaptR1 emits a passthrough JSON descriptor.
func AdaptR1(manifest *skill.ManifestV2) ([]byte, error) {
	if err := manifest.CheckCompat("r1"); err != nil {
		return nil, fmt.Errorf("compat r1: %w", err)
	}
	if err := validateAssertions(manifest, "r1", r1AllowedAssertions); err != nil {
		return nil, err
	}
	desc := R1Descriptor{
		Runtime:            "r1",
		Name:               manifest.Name,
		Version:            manifest.Version,
		Description:        manifest.Description,
		MinR1Version:       manifest.MinR1Version,
		Compat:             append([]string(nil), manifest.Compat...),
		Dependencies:       append([]string(nil), manifest.Dependencies...),
		Assertions:         manifest.AssertionsFor("r1"),
		SignatureAuthority: string(manifest.SignatureAuthority),
	}
	if len(manifest.ConsumerHooks) > 0 {
		desc.ConsumerHooks = make(map[string]hookEcho, len(manifest.ConsumerHooks))
		for name, hook := range manifest.ConsumerHooks {
			desc.ConsumerHooks[name] = hookEcho{
				Kind:          string(hook.Kind),
				PayloadSchema: hook.PayloadSchema,
				Optional:      hook.Optional,
			}
		}
	}
	return json.MarshalIndent(desc, "", "  ")
}
