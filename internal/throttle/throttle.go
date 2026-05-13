// Package throttle implements per-tool, two-tier (session + tenant)
// token-bucket rate limiting for the model-initiated tool surface
// (r1.* + MCP servers). See specs/per-tool-throttling.md.
//
// Hot path: Limiter.AllowMCP / Limiter.AllowAgentloop are invoked from
// the MCP server tools/call dispatch sites and the native agentloop
// executeTools loop right before a tool handler runs. Both paths are
// lock-free on the read side (atomic.Pointer for the active policy,
// sync.Map for the bucket index) so the throttle gate does not become
// a contention hot-spot under bursty concurrent tool fan-out.
//
// Library: golang.org/x/time/rate — chosen for non-blocking Allow()
// semantics + precise Reserve()-based retry-after computation. Per
// process state only; cross-daemon coordination is out of scope (D-D2
// pins sessions to a single daemon).
//
// Policy schema and parsing live in the leaf sub-package
// internal/throttle/policy so internal/config can also import them
// without creating a cycle (config -> mcp -> throttle).
package throttle

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	throttlepolicy "github.com/RelayOne/r1/internal/throttle/policy"
)

// Scope distinguishes the two bucket tiers checked on every call. Both
// tiers must allow the call; either tier's denial denies the whole
// request and the other tier's reservation is cancelled (T4).
type Scope string

const (
	ScopeSession Scope = "session"
	ScopeTenant  Scope = "tenant"
)

// Decision is the structured outcome of a single Allow* call.
//
// Allowed=true means BOTH the per-session and per-tenant buckets had a
// token available and have committed to the reservation. Allowed=false
// means at least one tier denied; Scope identifies which tier blocked
// (whichever had the larger retry-after), Principal is the matching
// identifier (sessionID or tenantID) and RetryAfter is the maximum of
// the two tiers' Delay() values so the caller can communicate a single
// "wait this long" number to the model / operator.
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
	Scope      Scope
	Principal  string
	Tool       string
}

// Config is the policy shape consumed by New / Reload. Re-exported
// from internal/throttle/policy so callers outside the throttle
// package don't need to import both.
type Config = throttlepolicy.Config

// Limiter is the public façade for the throttle gate.
//
// AllowMCP is called from the MCP server tools/call dispatch site
// before HandleToolCall. AllowAgentloop is called from agentloop's
// executeTools before the registered handler runs.
//
// Reload swaps the active policy without dropping in-flight tokens
// (per-bucket Limits are mutated via SetLimit/SetBurst; the bucket
// object itself is preserved so its current Tokens() count is kept).
type Limiter interface {
	AllowMCP(ctx context.Context, sessionID, tenantID, tool string) Decision
	AllowAgentloop(ctx context.Context, sessionID, tenantID, tool string) Decision
	Reload(cfg Config) error
	// DropSessionTokens zeroes every (ScopeSession, sessionID, *) bucket
	// for the supplied sessionID. Hook for promptguard injection-budget
	// kill (A1 T5) — see internal/promptguard.
	DropSessionTokens(sessionID string)
}

// limiter is the production implementation of Limiter.
type limiter struct {
	cfg     atomic.Pointer[Config]
	buckets sync.Map // map[string]*bucketEntry — see bucket.go for the key shape

	mu      sync.Mutex
	allowed map[string]int64
	deniedS map[string]int64
	deniedT map[string]int64

	disabled bool

	emitter atomic.Pointer[emitterState]
}

// New constructs a Limiter using the supplied policy. A zero-valued
// policy means "use bundled defaults" (T12). The R1_DISABLE_THROTTLE=1
// escape hatch (T25) returns a no-op limiter that admits every call.
func New(cfg Config) Limiter {
	if strings.TrimSpace(os.Getenv("R1_DISABLE_THROTTLE")) == "1" {
		return NoOpLimiter()
	}
	l := &limiter{
		allowed: make(map[string]int64),
		deniedS: make(map[string]int64),
		deniedT: make(map[string]int64),
	}
	if cfg.IsZero() {
		cfg = DefaultPolicy()
	}
	l.cfg.Store(&cfg)
	return l
}

// NoOpLimiter returns a Limiter that admits every call. Used for the
// --one-shot bypass (T15) and the R1_DISABLE_THROTTLE escape hatch.
func NoOpLimiter() Limiter {
	return &limiter{disabled: true}
}

// AllowMCP is the MCP-boundary hook.
func (l *limiter) AllowMCP(ctx context.Context, sessionID, tenantID, tool string) Decision {
	return l.allow(ctx, sessionID, tenantID, tool)
}

// AllowAgentloop is the native-agentloop boundary hook.
func (l *limiter) AllowAgentloop(ctx context.Context, sessionID, tenantID, tool string) Decision {
	return l.allow(ctx, sessionID, tenantID, tool)
}

// allow is the shared two-tier admission check. T4: composes a per-
// session and per-tenant reservation; cancels both atomically when
// either denies so no tokens are consumed by a rejected request.
func (l *limiter) allow(ctx context.Context, sessionID, tenantID, tool string) Decision {
	if l == nil || l.disabled {
		return Decision{Allowed: true, Tool: tool}
	}
	cfg := l.cfg.Load()
	if cfg == nil || cfg.IsZero() {
		return Decision{Allowed: true, Tool: tool}
	}

	now := time.Now()

	var (
		sessRes              *rate.Reservation
		tenRes               *rate.Reservation
		sessDelay, tenDelay  time.Duration
	)

	if strings.TrimSpace(sessionID) != "" {
		bucket := l.bucketFor(ScopeSession, sessionID, tool, cfg)
		if bucket != nil {
			sessRes = bucket.ReserveN(now, 1)
			sessDelay = sessRes.DelayFrom(now)
		}
	}
	if strings.TrimSpace(tenantID) != "" {
		bucket := l.bucketFor(ScopeTenant, tenantID, tool, cfg)
		if bucket != nil {
			tenRes = bucket.ReserveN(now, 1)
			tenDelay = tenRes.DelayFrom(now)
		}
	}

	if sessDelay == 0 && tenDelay == 0 {
		l.recordAllow(tool)
		return Decision{Allowed: true, Tool: tool}
	}

	if sessRes != nil {
		sessRes.CancelAt(now)
	}
	if tenRes != nil {
		tenRes.CancelAt(now)
	}

	dec := Decision{Allowed: false, Tool: tool}
	if sessDelay >= tenDelay {
		dec.Scope = ScopeSession
		dec.Principal = sessionID
		dec.RetryAfter = sessDelay
	} else {
		dec.Scope = ScopeTenant
		dec.Principal = tenantID
		dec.RetryAfter = tenDelay
	}
	l.recordDeny(tool, dec.Scope)
	l.emitDenied(ctx, dec)
	return dec
}

// Reload swaps the active policy in one atomic.Pointer store and
// updates every existing bucket's rate.Limit / Burst so in-flight
// tokens are preserved while the new rate takes effect immediately.
func (l *limiter) Reload(cfg Config) error {
	if l == nil || l.disabled {
		return nil
	}
	if cfg.IsZero() {
		cfg = DefaultPolicy()
	}
	normalized, err := throttlepolicy.Validate(cfg)
	if err != nil {
		return err
	}
	l.cfg.Store(&normalized)

	l.buckets.Range(func(k, v any) bool {
		entry, ok := v.(*bucketEntry)
		if !ok || entry == nil {
			return true
		}
		newLim := l.resolveLimit(entry.scope, entry.principal, entry.tool, &normalized)
		entry.limiter.SetLimitAt(time.Now(), newLim.Rate)
		entry.limiter.SetBurstAt(time.Now(), newLim.Burst)
		return true
	})
	return nil
}

// DropSessionTokens zeroes every (ScopeSession, sessionID, *) bucket.
func (l *limiter) DropSessionTokens(sessionID string) {
	if l == nil || l.disabled || strings.TrimSpace(sessionID) == "" {
		return
	}
	l.buckets.Range(func(k, v any) bool {
		entry, ok := v.(*bucketEntry)
		if !ok || entry == nil {
			return true
		}
		if entry.scope == ScopeSession && entry.principal == sessionID {
			entry.limiter.SetLimit(0)
			entry.limiter.SetBurst(0)
		}
		return true
	})
}

// Counts returns a snapshot of allowed/denied counts per tool.
func (l *limiter) Counts() (allowed, deniedSession, deniedTenant map[string]int64) {
	if l == nil {
		return nil, nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	allowed = cloneInt64(l.allowed)
	deniedSession = cloneInt64(l.deniedS)
	deniedTenant = cloneInt64(l.deniedT)
	return
}

func cloneInt64(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (l *limiter) recordAllow(tool string) {
	l.mu.Lock()
	l.allowed[tool]++
	l.mu.Unlock()
}

func (l *limiter) recordDeny(tool string, scope Scope) {
	l.mu.Lock()
	if scope == ScopeTenant {
		l.deniedT[tool]++
	} else {
		l.deniedS[tool]++
	}
	l.mu.Unlock()
}
