package deploy

import (
	"fmt"
	"sort"
	"sync"
)

// Factory constructs a fresh Deployer for its provider.
//
// Registered factories are invoked at lookup time (see Get), not at
// init time, so each caller gets an independent Deployer instance and
// mutable per-call state (argv buffers, temp files, bus handles) is
// never shared across concurrent deploys. Factories MUST be safe to
// call from multiple goroutines concurrently.
type Factory func() Deployer

// registryMu guards the registry map below. A sync.RWMutex keeps
// lookups (Get / Names) cheap while still serializing the rare
// Register call. We do NOT use a sync.Map because we need the
// "panic on duplicate" semantics, which require a read-check plus
// a write under the same lock.
var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register adds a provider factory under name.
//
// Intended to be called from each provider package's init(), e.g.
//
//	func init() { deploy.Register("vercel", newVercelDeployer) }
//
// Panics on duplicate name because duplicates almost always indicate
// a programmer bug (two init() blocks registering the same provider,
// or a stale build artifact linked twice). Failing loud at process
// start is strictly better than silently preferring one over the
// other and then debugging why the "wrong" adapter ran in prod.
//
// Register is safe to call from multiple goroutines, though the
// expected call pattern is a handful of init()s serialized by the
// Go runtime.
func Register(name string, f Factory) {
	if name == "" {
		panic("deploy.Register: name must not be empty")
	}
	if f == nil {
		panic(fmt.Sprintf("deploy.Register(%q): factory must not be nil", name))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("deploy.Register: duplicate provider %q", name))
	}
	registry[name] = f
}

// Get constructs and returns a fresh Deployer for name.
//
// Returns a non-nil error of the form "deploy: unknown provider X"
// when name is not registered; the sentinel text matches what the
// CLI layer surfaces to operators so `stoke deploy --provider bogus`
// yields a predictable error message.
//
// Get calls the underlying Factory while NOT holding the registry
// lock so a slow factory does not block other lookups.
func Get(name string) (Deployer, error) {
	registryMu.RLock()
	f, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("deploy: unknown provider %s", name)
	}
	return f(), nil
}

// Names returns the registered provider names in sorted order.
//
// Sorting is stable and deterministic so callers (CLI error messages,
// golden tests, tab-completion output) get byte-identical output
// between runs.
func Names() []string {
	registryMu.RLock()
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	registryMu.RUnlock()
	sort.Strings(out)
	return out
}

// resetRegistryForTest clears the registry. Test-only; not exported.
// Used by registry_test.go to avoid bleed between subtests that each
// register providers with arbitrary names.
func resetRegistryForTest() {
	registryMu.Lock()
	registry = map[string]Factory{}
	registryMu.Unlock()
}
