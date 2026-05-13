// Package skill — manifest_v2.go
//
// C7 cross-product-skill-exchange T1: federated pack manifest schema.
//
// v2 sits NEXT TO the v1 pack.yaml on disk. Loaders MUST keep working
// when manifest.v2.json is absent: in that case the loader synthesizes
// a minimal v2 manifest in-memory with compat:["r1"]. v1 packs
// therefore keep loading unchanged.
//
// The closed runtime set is exposed as ValidRuntimes so adapters and
// the registry can validate inputs against a single source of truth.
// New runtimes require a new spec PR (per the boundaries section of
// specs/cross-product-skill-exchange.md).
package skill

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RelayOne/r1/internal/skillmfr"
)

// ManifestV2File is the on-disk file name a pack ships next to its
// v1 pack.yaml. Optional; absence triggers synthesis.
const ManifestV2File = "manifest.v2.json"

// ManifestSchemaVersionV2 is the schema-version literal v2 callers
// expect. New schemas bump this constant and add explicit upgrade
// logic — they do NOT silently accept differing version strings.
const ManifestSchemaVersionV2 = "2.0.0"

// HookKind enumerates the closed set of consumer-hook actions a v2
// pack may declare. Anything else is a load-time error.
type HookKind string

const (
	HookKindPreInvoke       HookKind = "pre_invoke"
	HookKindPostInvoke      HookKind = "post_invoke"
	HookKindTransformArgs   HookKind = "transform_args"
	HookKindTransformReturn HookKind = "transform_return"
	HookKindErrorMap        HookKind = "error_map"
)

// SignatureAuthority enumerates allowed signature authorities. The
// v2 manifest field declares which authority's signing key the pack
// was signed by; the trust root then maps that authority back to a
// concrete ed25519 public key.
type SignatureAuthority string

const (
	AuthorityR1         SignatureAuthority = "r1"
	AuthorityCloudSwarm SignatureAuthority = "cloudswarm"
	AuthorityHeroa      SignatureAuthority = "heroa"
	AuthorityVeritize   SignatureAuthority = "veritize"
	AuthorityTenant     SignatureAuthority = "tenant"
)

// ValidRuntimes is the closed-set allowlist for the `compat` field.
// Order is stable so synthesized defaults remain deterministic.
var ValidRuntimes = []string{"r1", "cloudswarm", "heroa", "veritize"}

// ValidAuthorities is the closed-set allowlist for
// `signature_authority`. Order is stable for the same reasons.
var ValidAuthorities = []SignatureAuthority{
	AuthorityR1, AuthorityCloudSwarm, AuthorityHeroa, AuthorityVeritize, AuthorityTenant,
}

// ValidHookKinds is the closed-set allowlist for `consumer_hooks[*].kind`.
var ValidHookKinds = []HookKind{
	HookKindPreInvoke, HookKindPostInvoke, HookKindTransformArgs, HookKindTransformReturn, HookKindErrorMap,
}

// HookSpec is a single consumer-hook declaration. The payload schema
// is opaque to this package — adapters read it when constructing the
// wrapper.
type HookSpec struct {
	Kind          HookKind        `json:"kind"`
	PayloadSchema json.RawMessage `json:"payload_schema"`
	Optional      bool            `json:"optional,omitempty"`
}

// ManifestV2 is the on-disk shape of `manifest.v2.json`. JSON tags
// match the spec's Data Models table verbatim.
type ManifestV2 struct {
	SchemaVersion       string                `json:"manifest_schema_version"`
	Name                string                `json:"name"`
	Version             string                `json:"version"`
	Description         string                `json:"description,omitempty"`
	MinR1Version        string                `json:"min_r1_version,omitempty"`
	Compat              []string              `json:"compat"`
	RuntimeAssertions   map[string][]string   `json:"runtime_assertions,omitempty"`
	ConsumerHooks       map[string]HookSpec   `json:"consumer_hooks,omitempty"`
	Dependencies        []string              `json:"dependencies,omitempty"`
	SignatureAuthority  SignatureAuthority    `json:"signature_authority,omitempty"`

	// Source records where this manifest came from for diagnostics.
	// "file" = read from manifest.v2.json. "synthesized_v1" = inferred
	// from pack.yaml because no manifest.v2.json was present.
	Source string `json:"-"`
}

// LoadManifestV2 reads packRoot/manifest.v2.json. When absent, it
// synthesizes a v2 manifest from the v1 PackMeta with compat:["r1"]
// + signature_authority:"r1" so v1 packs flow through v2-only call
// sites without breakage.
//
// Validation is run unconditionally so callers can never see a v2
// manifest that contradicts the closed-set rules.
func LoadManifestV2(packRoot string) (*ManifestV2, error) {
	manifestPath := filepath.Join(packRoot, ManifestV2File)
	payload, err := os.ReadFile(manifestPath)
	if err == nil {
		var m ManifestV2
		if jerr := json.Unmarshal(payload, &m); jerr != nil {
			return nil, fmt.Errorf("manifest.v2.json invalid: %v", jerr)
		}
		m.Source = "file"
		if verr := m.Validate(); verr != nil {
			return nil, verr
		}
		return &m, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("manifest.v2.json read: %w", err)
	}
	// Synthesize from v1 metadata.
	loaded, lerr := skillmfr.LoadPack(packRoot)
	if lerr != nil {
		return nil, fmt.Errorf("manifest.v2 synthesize: %w", lerr)
	}
	m := SynthesizeFromV1(loaded.Meta)
	if verr := m.Validate(); verr != nil {
		return nil, verr
	}
	return m, nil
}

// SynthesizeFromV1 builds a v2 manifest in-memory from a v1 PackMeta.
// Used by LoadManifestV2 + tests + the registry's v1-fallback path.
func SynthesizeFromV1(meta skillmfr.PackMeta) *ManifestV2 {
	deps := append([]string(nil), meta.Dependencies...)
	return &ManifestV2{
		SchemaVersion:      ManifestSchemaVersionV2,
		Name:               meta.Name,
		Version:            meta.Version,
		Description:        meta.Description,
		MinR1Version:       meta.MinR1Version,
		Compat:             []string{"r1"},
		Dependencies:       deps,
		SignatureAuthority: AuthorityR1,
		Source:             "synthesized_v1",
	}
}

// Validate enforces the closed-set rules. Errors are phrased to match
// the spec's Error Handling table so callers can dispatch on the
// human-facing message text in their own surfaces.
func (m *ManifestV2) Validate() error {
	if m == nil {
		return fmt.Errorf("manifest_v2: nil manifest")
	}
	if strings.TrimSpace(m.SchemaVersion) == "" {
		return fmt.Errorf("manifest_v2: manifest_schema_version required")
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("manifest_v2: name required")
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("manifest_v2: version required")
	}
	if len(m.Compat) == 0 {
		return fmt.Errorf("manifest_v2: compat must list >=1 runtime")
	}
	for _, runtime := range m.Compat {
		if !isValidRuntime(runtime) {
			return fmt.Errorf("manifest_v2: unknown runtime %q", runtime)
		}
	}
	for runtime := range m.RuntimeAssertions {
		if !isValidRuntime(runtime) {
			return fmt.Errorf("manifest_v2: runtime_assertions references unknown runtime %q", runtime)
		}
	}
	if m.SignatureAuthority != "" && !isValidAuthority(m.SignatureAuthority) {
		return fmt.Errorf("manifest_v2: signature_authority %q not allowed", m.SignatureAuthority)
	}
	for hookName, hook := range m.ConsumerHooks {
		if !isValidHookKind(hook.Kind) {
			return fmt.Errorf("manifest_v2: consumer_hooks[%q].kind %q not allowed", hookName, hook.Kind)
		}
		if len(hook.PayloadSchema) == 0 {
			return fmt.Errorf("manifest_v2: consumer_hooks[%q].payload_schema required", hookName)
		}
	}
	return nil
}

// CheckCompat returns nil if the manifest declares the given runtime
// in its compat list. Otherwise returns an error matching the spec's
// negotiation rule. Empty runtime is rejected — callers must pass a
// concrete identifier.
func (m *ManifestV2) CheckCompat(runtime string) error {
	if m == nil {
		return fmt.Errorf("manifest_v2: nil manifest")
	}
	runtime = strings.TrimSpace(runtime)
	if runtime == "" {
		return fmt.Errorf("manifest_v2: CheckCompat requires non-empty runtime")
	}
	if !isValidRuntime(runtime) {
		return fmt.Errorf("manifest_v2: unknown runtime %q", runtime)
	}
	for _, r := range m.Compat {
		if r == runtime {
			return nil
		}
	}
	return fmt.Errorf("pack %q declares compat=%v but %s not present",
		m.Name, append([]string(nil), m.Compat...), runtime)
}

// AssertionsFor returns the runtime_assertions slice for runtime, or
// nil. Adapters use this to look the tokens up in their closed
// allow-set (unknown tokens are a load error per spec).
func (m *ManifestV2) AssertionsFor(runtime string) []string {
	if m == nil || m.RuntimeAssertions == nil {
		return nil
	}
	out := append([]string(nil), m.RuntimeAssertions[runtime]...)
	sort.Strings(out)
	return out
}

func isValidRuntime(r string) bool {
	for _, valid := range ValidRuntimes {
		if valid == r {
			return true
		}
	}
	return false
}

func isValidAuthority(a SignatureAuthority) bool {
	for _, valid := range ValidAuthorities {
		if valid == a {
			return true
		}
	}
	return false
}

func isValidHookKind(k HookKind) bool {
	for _, valid := range ValidHookKinds {
		if valid == k {
			return true
		}
	}
	return false
}
