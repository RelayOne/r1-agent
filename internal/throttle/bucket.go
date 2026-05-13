package throttle

import (
	"path/filepath"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	throttlepolicy "github.com/RelayOne/r1/internal/throttle/policy"
)

// bucketEntry is what the buckets sync.Map stores. It pairs the
// rate.Limiter (which carries the actual token state) with the
// metadata needed to (a) update its Limit/Burst on reload (T14) and
// (b) sweep stale buckets in the GC loop (T16).
type bucketEntry struct {
	limiter    *rate.Limiter
	lastAccess atomic.Int64 // unix nanos
	scope      Scope
	principal  string
	tool       string
}

// keyFor builds the composite sync.Map key. The bucket-key shape is
// load-bearing for tests: changing it invalidates Reload's iteration
// invariant.
func keyFor(scope Scope, principal, tool string) string {
	return string(scope) + "|" + principal + "|" + tool
}

// bucketFor returns the *rate.Limiter for the given scope+principal+
// tool, constructing one on first reference via LoadOrStore so two
// goroutines racing into the same key only build one limiter.
func (l *limiter) bucketFor(scope Scope, principal, tool string, cfg *Config) *rate.Limiter {
	k := keyFor(scope, principal, tool)
	if v, ok := l.buckets.Load(k); ok {
		entry := v.(*bucketEntry)
		entry.lastAccess.Store(time.Now().UnixNano())
		return entry.limiter
	}
	lim := l.resolveLimit(scope, principal, tool, cfg)
	entry := &bucketEntry{
		limiter:   rate.NewLimiter(lim.Rate, lim.Burst),
		scope:     scope,
		principal: principal,
		tool:      tool,
	}
	entry.lastAccess.Store(time.Now().UnixNano())
	actual, loaded := l.buckets.LoadOrStore(k, entry)
	if loaded {
		existing := actual.(*bucketEntry)
		existing.lastAccess.Store(time.Now().UnixNano())
		return existing.limiter
	}
	return entry.limiter
}

// resolveLimit computes the effective (rate, burst) for a given
// scope+principal+tool. Precedence: tool-specific entry -> defaults ->
// multiplier from the first matching principal override. Resolution
// runs at bucket-construction time (cold path) so the hot Allow call
// pays only one sync.Map.Load.
func (l *limiter) resolveLimit(scope Scope, principal, tool string, cfg *Config) throttlepolicy.Effective {
	if cfg == nil {
		empty := throttlepolicy.Config{}
		cfg = &empty
	}
	base := cfg.Defaults.ForScope(string(scope))
	if cfg.Tools != nil {
		if perTool, ok := cfg.Tools[tool]; ok {
			if specific := perTool.ForScope(string(scope)); specific.Burst > 0 {
				base = specific
			}
		}
	}
	mul := resolveMultiplier(principal, cfg.Overrides)
	if mul == 0 {
		mul = 1.0
	}
	r := rate.Limit(float64(base.Rate) * mul)
	b := int(float64(base.Burst) * mul)
	if b < 1 {
		b = 1
	}
	if r <= 0 {
		// Hardcoded fallback so a fully-missing or zero block does not
		// degenerate into a deny-all bucket; the bundled defaults in
		// T12 should normally cover this.
		r = rate.Limit(10)
		b = 20
	}
	return throttlepolicy.Effective{Rate: r, Burst: b}
}

// resolveMultiplier walks overrides in declaration order; first
// filepath.Match hit wins. Returns 1.0 when nothing matches.
func resolveMultiplier(principal string, overrides []throttlepolicy.Override) float64 {
	for _, o := range overrides {
		if o.Principal == "" || o.Multiplier <= 0 {
			continue
		}
		ok, err := filepath.Match(o.Principal, principal)
		if err != nil {
			continue
		}
		if ok {
			return o.Multiplier
		}
	}
	return 1.0
}
