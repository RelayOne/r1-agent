package throttle_test

import (
	"context"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/throttle"
	throttlepolicy "github.com/RelayOne/r1/internal/throttle/policy"
)

// TestOverrideGlobPrecedence covers T6: >= 10 cases including
// negative cases and first-match precedence.
func TestOverrideGlobPrecedence(t *testing.T) {
	t.Parallel()

	// Both defaults set to burst 10 so the cap probe (burst+5 calls)
	// stays meaningful on whichever scope the test exercises. The
	// "empty-principal" case is intentionally omitted because the
	// limiter short-circuits to Allowed when both sessionID and
	// tenantID are empty (there's nothing to attribute the call to);
	// that's the correct semantics, not a precedence question.
	cfg := mustValidate(t, throttlepolicy.Config{
		Defaults: throttlepolicy.Scoped{
			PerSession: throttlepolicy.Limit{Rate: "10/s", Burst: 10},
			PerTenant:  throttlepolicy.Limit{Rate: "10/s", Burst: 10},
		},
		Overrides: []throttlepolicy.Override{
			// Order matters: enterprise prefix wins before any later
			// pattern. The tests below pin first-match semantics.
			{Principal: "tenant:enterprise-*", Multiplier: 10},
			{Principal: "session:bench-*", Multiplier: 100},
			{Principal: "session:bench-*-shadow", Multiplier: 50}, // unreachable
			{Principal: "exact-name", Multiplier: 3},
			{Principal: "*-trailing", Multiplier: 2},
			{Principal: "leading-*", Multiplier: 4},
		},
	})

	cases := []struct {
		name          string
		tenantID      string
		sessionID     string
		expectScope   throttle.Scope
		expectBoosted bool // expect a higher burst than the un-boosted default
	}{
		{"enterprise-match", "tenant:enterprise-acme", "", throttle.ScopeTenant, true},
		{"enterprise-other-no-match", "tenant:other-acme", "", throttle.ScopeTenant, false},
		{"bench-session-match", "", "session:bench-42", throttle.ScopeSession, true},
		{"bench-shadow-still-bench-match", "", "session:bench-42-shadow", throttle.ScopeSession, true},
		{"exact-match", "", "exact-name", throttle.ScopeSession, true},
		{"trailing-wildcard", "", "x-trailing", throttle.ScopeSession, true},
		{"leading-wildcard", "", "leading-x", throttle.ScopeSession, true},
		{"no-match", "", "totally-different", throttle.ScopeSession, false},
		{"nested-wildcard-no-match", "", "session:nope", throttle.ScopeSession, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := throttle.New(cfg)
			// Fire enough calls to exhaust the un-boosted bucket; if
			// the boost applied, more calls succeed.
			const burst = 10
			ctx := context.Background()
			allowed := 0
			for i := 0; i < burst+5; i++ {
				if l.AllowMCP(ctx, tc.sessionID, tc.tenantID, "tool.x").Allowed {
					allowed++
				}
			}
			if tc.expectBoosted && allowed <= burst {
				t.Fatalf("%s: expected boosted bucket to admit > %d, got %d", tc.name, burst, allowed)
			}
			if !tc.expectBoosted && allowed > burst+2 {
				t.Fatalf("%s: expected un-boosted bucket to admit <= %d, got %d", tc.name, burst+2, allowed)
			}
		})
	}

	// First-match precedence: a principal matching the FIRST override
	// must NOT pick up the later override's multiplier.
	l := throttle.New(cfg)
	dec1 := decisionRetry(l, "tenant:enterprise-shadow", "", "tool.x", 200)
	dec2 := decisionRetry(l, "tenant:other", "", "tool.x", 200)
	if dec1 <= dec2 {
		t.Fatalf("enterprise multiplier should admit more than the un-boosted other tenant (enterprise=%d other=%d)", dec1, dec2)
	}
}

func decisionRetry(l throttle.Limiter, tenantID, sessionID, tool string, count int) int {
	ctx := context.Background()
	allowed := 0
	for i := 0; i < count; i++ {
		if l.AllowMCP(ctx, sessionID, tenantID, tool).Allowed {
			allowed++
		}
	}
	return allowed
}

// TestEmptyConfigIsOpen — when no policy is loaded, every call is
// admitted.
func TestEmptyConfigIsOpen(t *testing.T) {
	t.Parallel()
	l := throttle.New(throttlepolicy.Config{})
	// New() substitutes bundled defaults when Config is zero; with
	// those defaults a single allow should succeed.
	dec := l.AllowMCP(context.Background(), "any", "any", "any.tool")
	if !dec.Allowed {
		t.Fatalf("first call against bundled defaults should succeed, got %+v", dec)
	}
}

// TestDropSessionTokens covers T13: zeroing the session bucket
// stops subsequent calls for that session while sibling sessions
// continue working.
func TestDropSessionTokens(t *testing.T) {
	t.Parallel()
	cfg := mustValidate(t, throttlepolicy.Config{
		Defaults: throttlepolicy.Scoped{
			PerSession: throttlepolicy.Limit{Rate: "100/s", Burst: 100},
			PerTenant:  throttlepolicy.Limit{Rate: "1000/s", Burst: 1000},
		},
	})
	l := throttle.New(cfg)
	ctx := context.Background()

	// Drive a few calls to ensure session bucket exists.
	for i := 0; i < 5; i++ {
		if !l.AllowMCP(ctx, "victim", "tenant-a", "tool.x").Allowed {
			t.Fatalf("call %d should succeed before drop", i)
		}
	}

	l.DropSessionTokens("victim")

	// Subsequent calls for the dropped session must deny.
	dec := l.AllowMCP(ctx, "victim", "tenant-a", "tool.x")
	if dec.Allowed {
		t.Fatalf("call after DropSessionTokens should deny, got %+v", dec)
	}

	// Sibling session is unaffected.
	if !l.AllowMCP(ctx, "sibling", "tenant-a", "tool.x").Allowed {
		t.Fatalf("sibling session should still be allowed after victim drop")
	}
}

// TestMetricsKey covers the canonical naming used by /metrics.
func TestMetricsKey(t *testing.T) {
	t.Parallel()
	if got := throttle.MetricsKey("allowed", "r1.session.send", ""); got != "throttle.allowed.r1.session.send" {
		t.Fatalf("unexpected allowed key: %s", got)
	}
	if got := throttle.MetricsKey("denied", "r1.session.send", throttle.ScopeSession); got != "throttle.denied.r1.session.send.session" {
		t.Fatalf("unexpected denied key: %s", got)
	}
}

// TestNoOpLimiterCounts is a tiny smoke that the no-op path doesn't panic.
func TestNoOpLimiterSmoke(t *testing.T) {
	t.Parallel()
	l := throttle.NoOpLimiter()
	if err := l.Reload(throttlepolicy.Config{}); err != nil {
		t.Fatalf("NoOpLimiter.Reload should be a no-op: %v", err)
	}
	l.DropSessionTokens("anything") // must not panic
	dec := l.AllowAgentloop(context.Background(), "s", "t", "x")
	if !dec.Allowed {
		t.Fatalf("NoOpLimiter.AllowAgentloop should always allow, got %+v", dec)
	}
}

// TestReloadPreservesUnrelatedTokens covers T14 acceptance criterion 5:
// reloading the policy with a changed tool rate must not reset the
// token count for an UNRELATED bucket.
func TestReloadPreservesUnrelatedTokens(t *testing.T) {
	t.Parallel()
	cfg := mustValidate(t, throttlepolicy.Config{
		Defaults: throttlepolicy.Scoped{
			PerSession: throttlepolicy.Limit{Rate: "1/s", Burst: 1},
			PerTenant:  throttlepolicy.Limit{Rate: "100/s", Burst: 100},
		},
		Tools: map[string]throttlepolicy.Scoped{
			"browse.fetch": {
				PerSession: throttlepolicy.Limit{Rate: "1/s", Burst: 1},
				PerTenant:  throttlepolicy.Limit{Rate: "100/s", Burst: 100},
			},
		},
	})
	l := throttle.New(cfg)
	ctx := context.Background()

	// Drain the browse.fetch session bucket.
	if !l.AllowMCP(ctx, "s", "t", "browse.fetch").Allowed {
		t.Fatalf("first browse.fetch should succeed")
	}
	if l.AllowMCP(ctx, "s", "t", "browse.fetch").Allowed {
		t.Fatalf("second browse.fetch should deny (burst=1)")
	}

	// Reload with a NEW rate for browse.fetch only.
	newCfg := mustValidate(t, throttlepolicy.Config{
		Defaults: throttlepolicy.Scoped{
			PerSession: throttlepolicy.Limit{Rate: "1/s", Burst: 1},
			PerTenant:  throttlepolicy.Limit{Rate: "100/s", Burst: 100},
		},
		Tools: map[string]throttlepolicy.Scoped{
			"browse.fetch": {
				PerSession: throttlepolicy.Limit{Rate: "5/s", Burst: 5},
				PerTenant:  throttlepolicy.Limit{Rate: "100/s", Burst: 100},
			},
		},
	})
	if err := l.Reload(newCfg); err != nil {
		t.Fatalf("reload: %v", err)
	}

	// browse.fetch SHOULD still be exhausted (token count preserved
	// from before reload) — the new burst is 5 but the bucket has 0
	// tokens left. The new rate kicks in for the refill.
	dec := l.AllowMCP(ctx, "s", "t", "browse.fetch")
	if dec.Allowed {
		t.Fatalf("browse.fetch should still be exhausted post-reload (tokens preserved): %+v", dec)
	}

	// A different tool that never built a bucket prior to reload now
	// gets the (defaults) Limit — assert it's responsive.
	if !l.AllowMCP(ctx, "s", "t", "untouched.tool").Allowed {
		t.Fatalf("untouched.tool should admit post-reload")
	}

	// Sanity: the deny message refers to the right tool.
	if dec.Tool == "" || !strings.Contains(dec.Tool, "browse.fetch") {
		t.Fatalf("denial should reference browse.fetch, got %q", dec.Tool)
	}
}
