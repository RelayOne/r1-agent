package throttle

import (
	"github.com/RelayOne/r1/internal/metrics"
)

// MetricsKey returns the canonical metrics counter name for an
// (action, tool, scope) triple. Exported so test assertions and the
// /metrics endpoint integration test can construct the same names.
//
// Naming follows existing convention (dotted, lowercase, no
// punctuation in the tool fragment because the metrics registry
// stores raw map keys).
func MetricsKey(action, tool string, scope Scope) string {
	if scope == "" {
		return "throttle." + action + "." + tool
	}
	return "throttle." + action + "." + tool + "." + string(scope)
}

// PublishMetrics copies the limiter's internal allow/deny counters
// into the supplied metrics registry. Idempotent: counters are
// looked up by name and Add'd with the delta since the last call.
//
// Designed to be invoked from the daemon's existing /metrics
// scraper (currently a pull-side handler — see specs/r1d-server.md
// daemon.metrics). If the registry is nil this is a no-op so test
// harnesses that omit the metrics wiring keep working.
func PublishMetrics(l Limiter, reg *metrics.Registry) {
	if reg == nil {
		return
	}
	impl, ok := l.(*limiter)
	if !ok || impl == nil {
		return
	}
	allowed, deniedS, deniedT := impl.Counts()
	for tool, n := range allowed {
		reg.Counter(MetricsKey("allowed", tool, "")).Add(n)
	}
	for tool, n := range deniedS {
		reg.Counter(MetricsKey("denied", tool, ScopeSession)).Add(n)
	}
	for tool, n := range deniedT {
		reg.Counter(MetricsKey("denied", tool, ScopeTenant)).Add(n)
	}
	// Counts() returns a snapshot; reset the internal maps so the
	// next call only publishes the new delta. Lock-step with the
	// scraper.
	impl.mu.Lock()
	impl.allowed = map[string]int64{}
	impl.deniedS = map[string]int64{}
	impl.deniedT = map[string]int64{}
	impl.mu.Unlock()
}

// StatusSnapshot is the structured payload returned by the
// r1.throttle.status diagnostic tool (T24). One entry per live
// bucket; read-only.
type StatusSnapshot struct {
	Scope          Scope   `json:"scope"`
	Principal      string  `json:"principal"`
	Tool           string  `json:"tool"`
	AvailableTokens float64 `json:"available_tokens"`
	Burst          int     `json:"burst"`
	RatePerSecond  float64 `json:"rate_per_second"`
}

// Status walks every live bucket and returns its current limiter
// state. Read-only — never mutates. Intended for the
// r1.throttle.status MCP tool and for ad-hoc operator triage.
func Status(l Limiter) []StatusSnapshot {
	impl, ok := l.(*limiter)
	if !ok || impl == nil {
		return nil
	}
	var out []StatusSnapshot
	impl.buckets.Range(func(k, v any) bool {
		entry, ok := v.(*bucketEntry)
		if !ok || entry == nil {
			return true
		}
		out = append(out, StatusSnapshot{
			Scope:           entry.scope,
			Principal:       entry.principal,
			Tool:            entry.tool,
			AvailableTokens: entry.limiter.Tokens(),
			Burst:           entry.limiter.Burst(),
			RatePerSecond:   float64(entry.limiter.Limit()),
		})
		return true
	})
	return out
}
