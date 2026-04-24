package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/ericmacdougall/stoke/internal/r1env"
)

// Role constants for the provider pool. Operators pin each PoolEntry to
// one of these roles (or "any" for a catch-all fallback). The resolver
// in BuildProvider matches role+model, then falls back to role="any".
const (
	RoleWorker    = "worker"
	RoleReasoning = "reasoning"
	RoleReviewer  = "reviewer"
	RoleAny       = "any"
)

// PoolEntry describes one provider in an operator's STOKE_PROVIDERS
// pool. Each entry declares:
//   - name:   stable identifier ("anthropic-main", "ollama-local")
//   - url:    base URL for the provider's API
//   - key:    API key (empty string for local endpoints like Ollama)
//   - models: model IDs this provider serves
//   - role:   "worker" | "reasoning" | "reviewer" | "any"
//
// The JSON shape is flat and stable so an operator can keep their
// pool config in version control as a small YAML / env blob.
type PoolEntry struct {
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	Key    string   `json:"key,omitempty"`
	Models []string `json:"models"`
	Role   string   `json:"role"`
}

// Pool is a thread-safe lookup over PoolEntries keyed by role+model.
// Constructed once from STOKE_PROVIDERS at startup, then read by
// every provider resolution site.
type Pool struct {
	entries []PoolEntry

	mu    sync.Mutex
	cache map[string]Provider
}

// NewPoolFromEnv parses STOKE_PROVIDERS (JSON array of PoolEntry)
// and returns a Pool. If the env var is empty or missing, returns
// (nil, nil) so callers can fall back to the existing SmartDefaults
// path without a branch at every site.
func NewPoolFromEnv() (*Pool, error) {
	raw := strings.TrimSpace(r1env.Get("R1_PROVIDERS", "STOKE_PROVIDERS"))
	if raw == "" {
		return nil, nil
	}
	return NewPoolFromJSON(raw)
}

// NewPoolFromJSON parses a JSON array of PoolEntry and returns a Pool.
// Extracted so tests (and future YAML/file loaders) can construct a
// pool without mutating the process environment.
func NewPoolFromJSON(raw string) (*Pool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var entries []PoolEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("STOKE_PROVIDERS parse: %w", err)
	}
	if err := validatePool(entries); err != nil {
		return nil, err
	}
	return &Pool{entries: entries, cache: map[string]Provider{}}, nil
}

// Entries returns a copy of the pool's entries. Used by diagnostics
// (e.g. the REPL banner) that want to list what's configured without
// triggering a Provider build.
func (p *Pool) Entries() []PoolEntry {
	if p == nil {
		return nil
	}
	out := make([]PoolEntry, len(p.entries))
	copy(out, p.entries)
	return out
}

// BuildProvider returns a Provider instance for the given role+model.
// Lookup order:
//  1. exact entry where role matches + model in Models slice
//  2. fallback: entry with role="any" + model in Models slice
//
// Returns an error when no entry serves the combo. Results are cached
// by (role, model) so repeated calls return the same *Provider without
// re-parsing the URL heuristic.
func (p *Pool) BuildProvider(role, model string) (Provider, error) {
	if p == nil {
		return nil, fmt.Errorf("provider pool not configured")
	}
	if role == "" {
		return nil, fmt.Errorf("BuildProvider: empty role")
	}
	if model == "" {
		return nil, fmt.Errorf("BuildProvider: empty model")
	}
	key := role + ":" + model
	p.mu.Lock()
	defer p.mu.Unlock()
	if prov, ok := p.cache[key]; ok {
		return prov, nil
	}
	entry, ok := p.findEntry(role, model)
	if !ok {
		return nil, fmt.Errorf("provider pool: no entry serves role=%s model=%s", role, model)
	}
	prov := buildProviderFromEntry(entry)
	p.cache[key] = prov
	return prov, nil
}

// BuildProviderByRole returns a Provider using the first entry
// whose role matches, ignoring the specific model. Useful when
// the caller wants "whatever worker provider is configured" and
// will accept its default model. Returns the entry's first model
// plus the Provider.
//
// Lookup order mirrors BuildProvider: exact role first, then
// role="any". Returns an error when no entry matches the role
// and no role="any" entry exists.
func (p *Pool) BuildProviderByRole(role string) (Provider, string, error) {
	if p == nil {
		return nil, "", fmt.Errorf("provider pool not configured")
	}
	if role == "" {
		return nil, "", fmt.Errorf("BuildProviderByRole: empty role")
	}
	entry, ok := p.findEntryByRole(role)
	if !ok {
		return nil, "", fmt.Errorf("provider pool: no entry for role=%s (and no role=any fallback)", role)
	}
	if len(entry.Models) == 0 {
		// validatePool rejects empty Models so this is defense-in-depth.
		return nil, "", fmt.Errorf("provider pool: entry %q has no models", entry.Name)
	}
	model := entry.Models[0]
	key := role + ":" + model
	p.mu.Lock()
	defer p.mu.Unlock()
	if prov, ok := p.cache[key]; ok {
		return prov, model, nil
	}
	prov := buildProviderFromEntry(entry)
	p.cache[key] = prov
	return prov, model, nil
}

// findEntry returns the PoolEntry serving (role, model): exact-role
// match first, then role="any" fallback. Must be called with
// p.mu held by BuildProvider — the entries slice is immutable
// post-construction but we keep all state access serialized.
func (p *Pool) findEntry(role, model string) (PoolEntry, bool) {
	for _, e := range p.entries {
		if e.Role == role && containsString(e.Models, model) {
			return e, true
		}
	}
	if role != RoleAny {
		for _, e := range p.entries {
			if e.Role == RoleAny && containsString(e.Models, model) {
				return e, true
			}
		}
	}
	return PoolEntry{}, false
}

// findEntryByRole returns the first entry whose role matches, falling
// back to the first role="any" entry. Used by BuildProviderByRole.
func (p *Pool) findEntryByRole(role string) (PoolEntry, bool) {
	for _, e := range p.entries {
		if e.Role == role {
			return e, true
		}
	}
	if role != RoleAny {
		for _, e := range p.entries {
			if e.Role == RoleAny {
				return e, true
			}
		}
	}
	return PoolEntry{}, false
}

// buildProviderFromEntry picks the right Provider constructor for the
// entry's URL. Detection mirrors engine.NativeRunner's heuristic:
// OpenAI-compatible endpoints (openrouter, openai, together, fireworks,
// deepseek, generativelanguage, localhost Ollama) get the OpenAI
// provider; everything else (Anthropic, LiteLLM, custom proxies
// speaking Anthropic Messages) gets the Anthropic provider.
//
// Ollama is detected by path: it serves OpenAI-compat at /v1 but the
// default URL is http://localhost:11434 which is not covered by the
// hostname heuristic, so we add an explicit match.
func buildProviderFromEntry(e PoolEntry) Provider {
	lower := strings.ToLower(e.URL)
	if strings.Contains(lower, "openrouter.ai") ||
		strings.Contains(lower, "api.openai.com") ||
		strings.Contains(lower, "api.together.xyz") ||
		strings.Contains(lower, "api.fireworks.ai") ||
		strings.Contains(lower, "api.deepseek.com") ||
		strings.Contains(lower, "generativelanguage.googleapis.com") ||
		strings.Contains(lower, "11434") || // Ollama default port
		strings.Contains(lower, "/ollama") {
		base := strings.TrimRight(e.URL, "/")
		base = strings.TrimSuffix(base, "/v1")
		return NewOpenAICompatProvider(e.Name, e.Key, base)
	}
	return NewAnthropicProvider(e.Key, e.URL)
}

// validatePool enforces the minimum invariants every PoolEntry must
// satisfy. Rejects: empty Name, empty URL, invalid Role, empty Models.
// Also enforces non-empty entry list — an empty array in
// STOKE_PROVIDERS is treated as a configuration error, not "fall
// back to SmartDefaults" (for that, unset the env var).
func validatePool(entries []PoolEntry) error {
	if len(entries) == 0 {
		return fmt.Errorf("STOKE_PROVIDERS: expected at least one entry, got empty array")
	}
	seenNames := make(map[string]bool, len(entries))
	for i, e := range entries {
		if strings.TrimSpace(e.Name) == "" {
			return fmt.Errorf("STOKE_PROVIDERS[%d]: empty name", i)
		}
		if seenNames[e.Name] {
			return fmt.Errorf("STOKE_PROVIDERS[%d]: duplicate name %q", i, e.Name)
		}
		seenNames[e.Name] = true
		if strings.TrimSpace(e.URL) == "" {
			return fmt.Errorf("STOKE_PROVIDERS[%d] (%s): empty url", i, e.Name)
		}
		if !isValidRole(e.Role) {
			return fmt.Errorf("STOKE_PROVIDERS[%d] (%s): invalid role %q (want one of: worker, reasoning, reviewer, any)", i, e.Name, e.Role)
		}
		if len(e.Models) == 0 {
			return fmt.Errorf("STOKE_PROVIDERS[%d] (%s): empty models", i, e.Name)
		}
		for j, m := range e.Models {
			if strings.TrimSpace(m) == "" {
				return fmt.Errorf("STOKE_PROVIDERS[%d] (%s): models[%d] is blank", i, e.Name, j)
			}
		}
	}
	return nil
}

// isValidRole returns true when the role is one of the four documented
// role strings. The resolver is case-sensitive so operators get a
// clear error for typos rather than silent mismatches.
func isValidRole(role string) bool {
	switch role {
	case RoleWorker, RoleReasoning, RoleReviewer, RoleAny:
		return true
	default:
		return false
	}
}

// containsString is a tiny helper so the lookup doesn't pull in
// slices.Contains (would force go1.21 module bump) or build a map
// per entry (pool entries are tiny; linear scan is fine).
func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
