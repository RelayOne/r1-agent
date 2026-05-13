package lifecycle

import (
	"errors"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// Policy is the subset of r1.policy.yaml the lifecycle package reads.
// The shape mirrors internal/analytics/policy.go for consistency: a
// top-level `lifecycle` block carrying the operator-controlled knobs
// from §9 of the spec.
//
//	lifecycle:
//	  disabled: false
//	  tenant_optouts:
//	    - tenant-uuid-1
//	    - tenant-uuid-2
//
// Keeping the type local (rather than reaching into internal/config/'s
// full policy parser) keeps the lifecycle package self-contained.
type Policy struct {
	Lifecycle LifecyclePolicy `yaml:"lifecycle"`
}

// LifecyclePolicy carries the two operator-controlled knobs from §9
// of the spec. Disabled is the global kill switch; TenantOptOuts is
// the per-tenant allowlist (matching the analytics package's shape).
type LifecyclePolicy struct {
	Disabled      bool     `yaml:"disabled"`
	TenantOptOuts []string `yaml:"tenant_optouts"`
}

// LoadPolicy reads an r1.policy.yaml from path and returns the parsed
// lifecycle block. A missing file is NOT an error — it returns the
// zero policy (lifecycle enabled, no opt-outs).
func LoadPolicy(path string) (LifecyclePolicy, error) {
	if path == "" {
		return LifecyclePolicy{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LifecyclePolicy{}, nil
		}
		return LifecyclePolicy{}, err
	}
	var p Policy
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return LifecyclePolicy{}, err
	}
	return p.Lifecycle, nil
}

// PolicyGate is the hot-reloadable per-tenant gate consulted by the
// bus subscriber on every event. It is goroutine-safe; mutations swap
// the internal map atomically so a hot-reload never deadlocks the
// observe goroutine.
//
// The policy package itself does NOT subscribe to file-system events
// — the operator-side r1d-server policy loader (specs/r1d-server.md)
// calls Apply whenever r1.policy.yaml changes on disk.
type PolicyGate struct {
	mu       sync.RWMutex
	disabled bool
	optOuts  map[string]struct{}
}

// NewPolicyGate constructs an empty, "lifecycle enabled" gate.
func NewPolicyGate() *PolicyGate {
	return &PolicyGate{optOuts: make(map[string]struct{})}
}

// Apply overlays p onto the gate. Existing tenant opt-outs are
// replaced (not merged) so the YAML file is the single source of
// truth on every reload.
func (g *PolicyGate) Apply(p LifecyclePolicy) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.disabled = p.Disabled
	g.optOuts = make(map[string]struct{}, len(p.TenantOptOuts))
	for _, t := range p.TenantOptOuts {
		if t == "" {
			continue
		}
		g.optOuts[t] = struct{}{}
	}
}

// Disabled reports whether the global kill switch is set. A true
// return means the subscriber should drop every event regardless of
// tenant.
func (g *PolicyGate) Disabled() bool {
	if g == nil {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.disabled
}

// LifecycleDisabled reports whether the gate has been disabled for
// the given tenant. An empty tenantID is interpreted as "no tenant
// bound — fall back to the global switch". This matches the spec
// §6.3 anonymous-user filter: anonymous events are dropped above
// this layer, but a defensive tenant-empty caller should still see
// the global switch.
func (g *PolicyGate) LifecycleDisabled(tenantID string) bool {
	if g == nil {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.disabled {
		return true
	}
	if tenantID == "" {
		return false
	}
	_, ok := g.optOuts[tenantID]
	return ok
}

// Snapshot returns a defensive copy of the gate's current state. Used
// by the admin metrics surface to expose the resolved policy.
func (g *PolicyGate) Snapshot() LifecyclePolicy {
	if g == nil {
		return LifecyclePolicy{}
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := LifecyclePolicy{Disabled: g.disabled}
	for t := range g.optOuts {
		out.TenantOptOuts = append(out.TenantOptOuts, t)
	}
	return out
}
