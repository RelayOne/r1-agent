// Package critic — registry.go
//
// The default rule chain assembled by DefaultRegistry. Spec
// §T4 item 16 mandates the InjectionAwareCritic is default-on AFTER
// the existing honesty-gate equivalent (the no-hardcoded-secrets and
// no-todo-fixme rules). The chain is ordered cheapest-first so the
// regex-only rules fail-fast before the slower function-based checks
// run.
//
// SPEC DEVIATION: the spec named the file `registry.go` and pointed at
// a non-existent `critic.DefaultRegistry()` API. In the real codebase
// `DefaultRules` is the canonical chain builder. This file adds the
// InjectionAwareCritic into that chain and exposes a thin
// `DefaultRegistry()` wrapper so test code that follows the spec
// language verbatim compiles.

package critic

// DefaultRegistry returns the default rule chain. It is the canonical
// public name for the operator-visible "what rules run by default"
// surface; internally it delegates to DefaultRulesWithInjection.
func DefaultRegistry() []Rule {
	return DefaultRulesWithInjection()
}

// DefaultRulesWithInjection returns DefaultRules() with the
// InjectionAwareCritic rule appended AFTER the existing chain. The
// position-after honesty-gate ordering matches the spec: the cheap
// honesty rules fire first; the injection-aware rule scans for
// promptguard signatures after.
func DefaultRulesWithInjection() []Rule {
	base := DefaultRules()
	return append(base, InjectionAwareRule())
}
