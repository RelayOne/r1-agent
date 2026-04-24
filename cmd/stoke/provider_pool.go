package main

// S-6: multi-provider pool. STOKE_PROVIDERS, when set, overrides the
// single-provider SmartDefaults path with a per-role lookup.
//
// The REPL / TUI entry points call resolveSmartDefaultsWithPool()
// instead of detectSmartDefaults() directly. When STOKE_PROVIDERS is
// unset, behavior is identical to pre-S-6 (detectSmartDefaults
// probes LiteLLM → claude → codex → ANTHROPIC_API_KEY). When set,
// the pool wins: the worker-role entry populates SmartDefaults so
// downstream SmartDefaults consumers keep working, and the pool
// itself is stashed on smartPool for per-role resolution sites.

import (
	"fmt"

	"github.com/ericmacdougall/stoke/internal/provider"
)

// resolveSmartDefaultsWithPool wraps detectSmartDefaults with a
// pool-first branch. Returns:
//   - SmartDefaults populated from the worker-role entry when a
//     pool is configured (reasoning/reviewer are served by the
//     pool directly at per-role sites)
//   - the unmodified detectSmartDefaults() result when no pool
//     is configured
//
// Fails with fatal() when STOKE_PROVIDERS is set but malformed, or
// when the pool is set but has no entry serving the "worker" role
// (and no "any" fallback). That's an operator-configuration error
// worth halting on — the alternative is to silently fall back to
// SmartDefaults, which would hide the misconfiguration.
func resolveSmartDefaultsWithPool() SmartDefaults {
	pool, err := provider.NewPoolFromEnv()
	if err != nil {
		fatal("STOKE_PROVIDERS: %v", err)
	}
	if pool == nil {
		// Backward-compatible path: exactly detectSmartDefaults().
		return detectSmartDefaults()
	}

	// Pool path: pick the worker-role entry to fill SmartDefaults so
	// the REPL banner + downstream single-provider call sites still
	// work. Reasoning / reviewer are resolved via smartPool at their
	// own sites — this struct only covers the default runner.
	_, workerModel, werr := pool.BuildProviderByRole(provider.RoleWorker)
	if werr != nil {
		fatal("STOKE_PROVIDERS: worker role not resolvable: %v", werr)
	}
	// Pull the worker entry directly so we can surface the URL + key
	// into SmartDefaults — BuildProviderByRole only returns the
	// Provider, not the entry metadata.
	entry, ok := findPoolEntryForRole(pool, provider.RoleWorker)
	if !ok {
		fatal("STOKE_PROVIDERS: worker role not found (this should not happen after BuildProviderByRole succeeded)")
	}

	d := SmartDefaults{
		RunnerMode:    "native",
		NativeBaseURL: entry.URL,
		NativeAPIKey:  entry.Key,
		NativeModel:   workerModel,
		Notes: []string{
			fmt.Sprintf("STOKE_PROVIDERS pool active (%d entries) → worker=%s reasoning=%s reviewer=%s",
				len(pool.Entries()),
				describePoolRole(pool, provider.RoleWorker),
				describePoolRole(pool, provider.RoleReasoning),
				describePoolRole(pool, provider.RoleReviewer),
			),
		},
	}
	// Empty key on a local endpoint (e.g. Ollama) still needs a
	// non-empty stub downstream — callers that expect an
	// Authorization header would otherwise barf. LocalLiteLLMStub
	// is the existing convention for "no real key, but something
	// to send" so we reuse it rather than inventing a second stub.
	if d.NativeAPIKey == "" {
		d.NativeAPIKey = provider.LocalLiteLLMStub
	}
	return d
}

// findPoolEntryForRole returns the first entry whose Role matches,
// with a role="any" fallback. Mirrors Pool.BuildProviderByRole's
// internal lookup order but returns the raw entry so callers can
// pull URL + Key without reconstructing them from the Provider.
func findPoolEntryForRole(pool *provider.Pool, role string) (provider.PoolEntry, bool) {
	for _, e := range pool.Entries() {
		if e.Role == role {
			return e, true
		}
	}
	for _, e := range pool.Entries() {
		if e.Role == provider.RoleAny {
			return e, true
		}
	}
	return provider.PoolEntry{}, false
}

// describePoolRole returns a short "name[@model]" string describing
// which entry serves a role, or "<none>" when no entry matches.
// Used only in the smart-defaults banner; never parsed.
func describePoolRole(pool *provider.Pool, role string) string {
	entry, ok := findPoolEntryForRole(pool, role)
	if !ok {
		return "<none>"
	}
	if len(entry.Models) == 0 {
		return entry.Name
	}
	return entry.Name + "@" + entry.Models[0]
}

