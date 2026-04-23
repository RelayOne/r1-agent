// Package skillmfr — manifest.go
//
// STOKE-003: capability manifest schema + enforcement. Every
// tool / skill / MCP server / hired agent registered with
// Stoke must carry a complete Capability Manifest declaring
// its input schema, output schema, when-to-use / when-not-to-
// use guidance, and behavior flags. Without the manifest, the
// registration is rejected.
//
// Why: the SOW cites a 15-27 pp accuracy variance directly
// attributable to unstructured tool descriptions. A manifest
// makes "when to use" a data field the dispatcher can reason
// about, not a free-form preamble the model skims.
//
// Scope of this file:
//
//   - Manifest struct with the 5 required sections
//   - Validation rules (min 1 whenToUse, min 2 whenNotToUse,
//     typed input/output schemas non-empty)
//   - ComputeHash for drift detection (SHA256 over canonical
//     JSON of the manifest fields)
//   - Manifest registry with Register / Get / List
//   - CLI helper ScaffoldManifest to generate a skeleton
//     from an OpenAPI operation (minimal impl — the operator-
//     facing CLI wraps this)
package skillmfr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Manifest is the canonical capability descriptor every
// tool / skill / hired-agent must ship.
type Manifest struct {
	// Name is the unique identifier. Must match the pattern
	// used by the registry (lowercase alpha + digits + hyphen
	// + underscore).
	Name string `json:"name"`

	// Version is a semver-ish string. Compared as opaque
	// strings by the store; operators handle semver ordering
	// themselves.
	Version string `json:"version"`

	// Description is the human-facing one-line summary.
	// Shown in discovery UIs + used as the header line when
	// the manifest is injected into an agent's system prompt.
	Description string `json:"description"`

	// InputSchema is the typed input shape in JSON Schema.
	// Opaque to this package (we don't evaluate the schema)
	// but REQUIRED non-empty so downstream validators have
	// something to check.
	InputSchema json.RawMessage `json:"inputSchema"`

	// OutputSchema is the typed output shape in JSON Schema.
	// Same constraint as InputSchema.
	OutputSchema json.RawMessage `json:"outputSchema"`

	// WhenToUse lists concrete scenarios where the capability
	// is the right choice. Must have at least 1 entry. Free-
	// form strings; LLMs + discovery layers rank candidates
	// against user intent by matching these.
	WhenToUse []string `json:"whenToUse"`

	// WhenNotToUse lists scenarios where the capability is
	// NOT appropriate. Must have at least 2 entries per the
	// SOW (the 2-entry floor forces operators to think about
	// non-use cases rather than just enumerating use cases).
	WhenNotToUse []string `json:"whenNotToUse"`

	// BehaviorFlags are the operational declarations that
	// drive downstream wiring: whether the capability mutates
	// state, whether it requires network, preferred sandbox,
	// etc.
	BehaviorFlags BehaviorFlags `json:"behaviorFlags"`

	// RecommendedFor is an optional list of capability tags
	// used by catalog ranking. Downstream discovery UIs
	// (CloudSwarm skill catalog, skillselect) use these tags
	// to surface a skill for a given capability bucket
	// (e.g. "landing-page", "cold-email", "fact-check")
	// without having to grep WhenToUse strings.
	//
	// Backward-compatibility: omitted from JSON when empty
	// via omitempty; existing manifests that predate this
	// field unmarshal to a nil slice and continue to pass
	// Validate unchanged (no floor — empty list is valid).
	//
	// Source: CLOUDSWARM-R1-INTEGRATION.md §2.9 / §5.6.
	RecommendedFor []string `json:"recommendedFor,omitempty"`
}

// BehaviorFlags describes non-functional aspects the
// dispatcher needs to place a capability correctly.
type BehaviorFlags struct {
	// MutatesState: true if the capability modifies
	// persistent state (filesystem, database, external API
	// side effects). Used to decide caching + dry-run
	// policy.
	MutatesState bool `json:"mutatesState"`

	// RequiresNetwork: true if the capability makes any
	// outbound network call. Used to decide sandbox network
	// policy.
	RequiresNetwork bool `json:"requiresNetwork"`

	// PreferredSandbox names the sandbox backend the
	// capability prefers (e.g. "docker", "firecracker").
	// Empty defaults to the operator's configured default.
	PreferredSandbox string `json:"preferredSandbox,omitempty"`

	// CostCategory is a coarse-grained cost bucket for
	// budget-aware scheduling: "free" / "cheap" /
	// "moderate" / "expensive". Not a precise dollar figure
	// — the SDK doesn't know pricing — just a hint.
	CostCategory string `json:"costCategory,omitempty"`
}

// ErrIncompleteManifest is returned by Validate when a
// required field is missing or below its minimum threshold.
// Wrapped so callers can errors.Is against it.
var ErrIncompleteManifest = errors.New("skillmfr: manifest incomplete")

// Validate runs the SOW's required-field checks. Returns a
// wrapped ErrIncompleteManifest with the specific missing
// piece on failure; nil on success.
//
// The floors:
//   - Name:         non-empty
//   - Version:      non-empty
//   - Description:  non-empty
//   - InputSchema:  non-empty JSON
//   - OutputSchema: non-empty JSON
//   - WhenToUse:    at least 1 entry
//   - WhenNotToUse: at least 2 entries (SOW requirement)
func (m Manifest) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("%w: name required", ErrIncompleteManifest)
	}
	if m.Version == "" {
		return fmt.Errorf("%w: version required", ErrIncompleteManifest)
	}
	if m.Description == "" {
		return fmt.Errorf("%w: description required", ErrIncompleteManifest)
	}
	if err := validateSchemaBytes("inputSchema", m.InputSchema); err != nil {
		return err
	}
	if err := validateSchemaBytes("outputSchema", m.OutputSchema); err != nil {
		return err
	}
	if len(m.WhenToUse) < 1 {
		return fmt.Errorf("%w: whenToUse needs at least 1 entry", ErrIncompleteManifest)
	}
	if len(m.WhenNotToUse) < 2 {
		return fmt.Errorf("%w: whenNotToUse needs at least 2 entries", ErrIncompleteManifest)
	}
	return nil
}

// validateSchemaBytes enforces that InputSchema/OutputSchema
// are (a) non-empty, (b) not the literal "null", and (c)
// actual valid JSON. Previous behavior accepted arbitrary
// bytes like `not-json-at-all` through Validate and only
// failed later at the first json.Unmarshal site in a consumer
// — where the error was blamed on the consumer instead of
// the registration that let the broken manifest in.
func validateSchemaBytes(field string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("%w: %s required", ErrIncompleteManifest, field)
	}
	trimmed := string(raw)
	if trimmed == "null" {
		return fmt.Errorf("%w: %s required", ErrIncompleteManifest, field)
	}
	var anyVal interface{}
	if err := json.Unmarshal(raw, &anyVal); err != nil {
		return fmt.Errorf("%w: %s must be valid JSON: %v", ErrIncompleteManifest, field, err)
	}
	return nil
}

// ComputeHash returns the SHA256 content-hash of the manifest
// as a hex string. Used for drift detection: the hash is
// stored in the ledger on every invoke; if the manifest
// mutates out-of-band (a new tool version was deployed
// without a re-registration), the hash changes and operators
// see the drift in audit reports.
//
// Canonicalization: marshals via encoding/json.Marshal
// (field order = struct declaration order) so two callers
// with the same field values produce the same hash.
func (m Manifest) ComputeHash() (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("skillmfr: marshal for hash: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Registry holds registered manifests. Thread-safe. The
// Register path enforces Validate — incomplete manifests are
// rejected at registration time, not discovered later.
type Registry struct {
	mu        sync.RWMutex
	manifests map[string]Manifest
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{manifests: map[string]Manifest{}}
}

// Register validates the manifest and stores it. Returns
// ErrIncompleteManifest on validation failure; no partial
// state is written.
func (r *Registry) Register(m Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.manifests[m.Name] = m
	return nil
}

// Get retrieves a manifest by name.
func (r *Registry) Get(name string) (Manifest, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.manifests[name]
	return m, ok
}

// List returns the sorted-by-name list of registered
// manifests. Used by discovery UIs.
func (r *Registry) List() []Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Manifest, 0, len(r.manifests))
	for _, m := range r.manifests {
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

// RecordInvoke is called by the tool dispatcher at invoke
// time. Callers plumb the returned manifest hash into the
// ledger node they write for the invocation; drift between
// the stored hash and a re-computed one surfaces as a
// misconfiguration at audit time.
//
// Returns ErrNotFound when the named manifest doesn't exist
// (the dispatcher MUST reject invocation in that case).
func (r *Registry) RecordInvoke(name string) (string, error) {
	r.mu.RLock()
	m, ok := r.manifests[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrManifestNotFound, name)
	}
	return m.ComputeHash()
}

// ErrManifestNotFound is returned when a caller asks for a
// manifest that isn't registered.
var ErrManifestNotFound = errors.New("skillmfr: manifest not found")

// --- Scaffold helper ---

// ScaffoldFromOpenAPI builds a skeleton Manifest from a tool
// name + an OpenAPI operation. The operator fills in
// whenToUse / whenNotToUse manually before Register — the
// skeleton fails Validate() deliberately so nobody registers
// an un-reviewed scaffold.
//
// `operationRef` is opaque metadata the caller attaches (e.g.
// the OpenAPI path + method); carried into Description so the
// operator knows what they're scaffolding.
func ScaffoldFromOpenAPI(name, version, operationRef string, inputSchema, outputSchema json.RawMessage) Manifest {
	return Manifest{
		Name:         name,
		Version:      version,
		Description:  fmt.Sprintf("Scaffolded from %s (fill in real description before registration)", operationRef),
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
		WhenToUse:    nil, // operator must populate
		WhenNotToUse: nil, // operator must populate
		BehaviorFlags: BehaviorFlags{
			CostCategory: "moderate",
		},
	}
}
