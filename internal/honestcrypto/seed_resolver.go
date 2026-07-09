// seed_resolver.go — the seed/context injection seam (MECHANISM ONLY).
//
// Resolve(profile, ctx) -> (prefix, meta). The mechanism is unencumbered and
// fully ours; the CORPUS content is proprietary and gitignored. The default
// resolver assembles tiered profile text (role -> domain -> task) and the caller
// PREPENDS it to a system prompt — it never replaces the caller's prompt. A
// richer corpus backend swaps in by config with zero caller changes; only the
// content behind the interface is proprietary, never the interface.
package honestcrypto

import "sync"

// SeedContext is free-form context the resolver may use to select tiers (task
// id, repo, etc).
type SeedContext map[string]any

// SeedResult is the assembled prefix plus which tiers contributed.
type SeedResult struct {
	// Prefix is the assembled text to prepend to a system prompt (may be empty).
	Prefix string
	// Meta records which tiers contributed and the profile, for auditability.
	Meta SeedMeta
}

// SeedMeta records the tiers used and the profile resolved.
type SeedMeta struct {
	Tiers   []string
	Profile string
}

// SeedTiers is a tier of seed content: role (T0), domain (T1), task (T2).
type SeedTiers struct {
	Role   string
	Domain string
	Task   string
}

// SeedResolver resolves a profile name to an assembled prefix.
type SeedResolver interface {
	Resolve(profile string, ctx SeedContext) SeedResult
	// Backend is the stable backend name.
	Backend() string
}

// BackendStaticFile is the default SeedResolver backend name.
const BackendStaticFile = "static-file"

// StaticSeedResolver is the unencumbered default mechanism. Given a profile name
// it looks up tiered content from an injected corpus map and assembles role ->
// domain -> task in order, skipping absent tiers, joined by a blank line. No
// content ships in this package — only the assembly rule.
type StaticSeedResolver struct {
	corpus map[string]SeedTiers
}

// NewStaticSeedResolver builds a resolver over the given corpus (nil is fine).
func NewStaticSeedResolver(corpus map[string]SeedTiers) *StaticSeedResolver {
	if corpus == nil {
		corpus = map[string]SeedTiers{}
	}
	return &StaticSeedResolver{corpus: corpus}
}

// Backend returns the stable backend name.
func (r *StaticSeedResolver) Backend() string { return BackendStaticFile }

// Resolve assembles the tiers for profile, skipping absent tiers and preserving
// role -> domain -> task order. An unknown profile yields an empty prefix and
// never errors.
func (r *StaticSeedResolver) Resolve(profile string, _ SeedContext) SeedResult {
	tiers := r.corpus[profile]
	parts := []string{}
	used := []string{}
	if tiers.Role != "" {
		parts = append(parts, tiers.Role)
		used = append(used, "role")
	}
	if tiers.Domain != "" {
		parts = append(parts, tiers.Domain)
		used = append(used, "domain")
	}
	if tiers.Task != "" {
		parts = append(parts, tiers.Task)
		used = append(used, "task")
	}
	return SeedResult{Prefix: join(parts, "\n\n"), Meta: SeedMeta{Tiers: used, Profile: profile}}
}

func join(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

// ---- Registry: swap a richer corpus backend in by config, zero caller changes ----

var (
	seedMu       sync.RWMutex
	seedResolver SeedResolver = NewStaticSeedResolver(nil)
)

// GetSeedResolver is the canonical accessor every caller uses.
func GetSeedResolver() SeedResolver {
	seedMu.RLock()
	defer seedMu.RUnlock()
	return seedResolver
}

// SetSeedResolver swaps the resolver backend by config.
func SetSeedResolver(next SeedResolver) {
	seedMu.Lock()
	defer seedMu.Unlock()
	seedResolver = next
}

// ResetSeedResolver restores the unencumbered default (test hygiene).
func ResetSeedResolver() {
	seedMu.Lock()
	defer seedMu.Unlock()
	seedResolver = NewStaticSeedResolver(nil)
}
