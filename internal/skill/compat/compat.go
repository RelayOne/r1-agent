// Package compat — runtime adapters for cross-product skill packs.
//
// C7 cross-product-skill-exchange T4: each runtime in the closed-set
// {r1, cloudswarm, heroa, veritize} ships an Adapt(manifest) entry
// here that transforms an R1 v2 manifest into the consumer-side
// skill descriptor (CloudSwarm SkillDefinition, Heroa Template, etc.).
//
// Why isolated adapters: the spec's boundaries section forbids us
// from changing the v1 PackMeta struct OR the pack.sig.json
// envelope. All cross-product knowledge therefore lives here as
// pure functions over ManifestV2.
//
// Each adapter:
//   1. Calls manifest.CheckCompat(runtime) and returns its error.
//   2. Validates runtime_assertions[runtime] against the adapter's
//      closed allow-set. Unknown tokens are a load-time error so a
//      malicious pack cannot bypass the adapter contract via novel
//      keys (Business Logic § Pack-runtime negotiation step 4).
//   3. Returns a JSON descriptor matching the target runtime's
//      contract.
//
// Adapters are deterministic: given the same input manifest they
// produce byte-identical output. Tests rely on this for golden-file
// assertions.
package compat

import (
	"fmt"

	"github.com/RelayOne/r1/internal/skill"
)

// AdapterFunc is the entry point each runtime adapter ships.
// Returns the wrapper bytes (typically JSON) the operator writes
// into <pack>/wrappers/<runtime>.wrapper for downstream consumption
// by the target product.
type AdapterFunc func(manifest *skill.ManifestV2) ([]byte, error)

// adapters is the closed-set dispatch table populated by each
// runtime's init() in this package.
var adapters = map[string]AdapterFunc{}

// Register adds an adapter to the dispatch table. Called by each
// runtime's init(). Duplicate registrations panic — keeps the
// invariant that a runtime ID maps to exactly one Adapt function.
func Register(runtime string, fn AdapterFunc) {
	if _, ok := adapters[runtime]; ok {
		panic(fmt.Sprintf("compat: duplicate adapter registration for %q", runtime))
	}
	adapters[runtime] = fn
}

// Adapt dispatches to the runtime adapter registered for runtime.
// Errors with "unsupported adoption target" when the runtime is not
// in the closed set — matches the spec's Error Handling table.
func Adapt(runtime string, manifest *skill.ManifestV2) ([]byte, error) {
	if manifest == nil {
		return nil, fmt.Errorf("compat: nil manifest")
	}
	fn, ok := adapters[runtime]
	if !ok {
		return nil, fmt.Errorf("compat: unsupported adoption target: %s", runtime)
	}
	return fn(manifest)
}

// Runtimes returns the registered runtime ids. Sorted ascending so
// tests stay deterministic.
func Runtimes() []string {
	out := make([]string, 0, len(adapters))
	for k := range adapters {
		out = append(out, k)
	}
	// Sort without importing sort to keep this file's imports small.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// validateAssertions returns nil if every assertion token for the
// given runtime is in allowed. Unknown tokens are a load-time error
// per spec.
func validateAssertions(manifest *skill.ManifestV2, runtime string, allowed map[string]struct{}) error {
	for _, token := range manifest.AssertionsFor(runtime) {
		if _, ok := allowed[token]; !ok {
			return fmt.Errorf("compat: pack %q runtime_assertions[%s] contains unknown token %q",
				manifest.Name, runtime, token)
		}
	}
	return nil
}
