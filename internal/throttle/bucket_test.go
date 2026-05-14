package throttle_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/throttle"
	throttlepolicy "github.com/RelayOne/r1/internal/throttle/policy"
)

// TestBurstThenDeny covers T17a: 5 immediate Allows succeed when
// burst=5; the 6th denies with retry-after roughly equal to 1/rate.
func TestBurstThenDeny(t *testing.T) {
	t.Parallel()

	cfg := mustValidate(t, throttlepolicy.Config{
		Defaults: throttlepolicy.Scoped{
			PerSession: throttlepolicy.Limit{Rate: "5/s", Burst: 5},
			PerTenant:  throttlepolicy.Limit{Rate: "100/s", Burst: 100},
		},
	})
	l := throttle.New(cfg)

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		dec := l.AllowMCP(ctx, "sess-1", "tenant-1", "tool.x")
		if !dec.Allowed {
			t.Fatalf("call %d should have been allowed, got %+v", i, dec)
		}
	}
	dec := l.AllowMCP(ctx, "sess-1", "tenant-1", "tool.x")
	if dec.Allowed {
		t.Fatalf("6th call should have been denied, got allowed")
	}
	if dec.Scope != throttle.ScopeSession {
		t.Fatalf("expected session-scope denial, got %v", dec.Scope)
	}
	// At 5/s, retry-after should be roughly 200ms (1/5s = 200ms).
	if dec.RetryAfter < 100*time.Millisecond || dec.RetryAfter > 500*time.Millisecond {
		t.Fatalf("retry-after out of expected range: %v", dec.RetryAfter)
	}
}

// TestRefill covers T17b: after exhaustion, waiting 1s on a 1/s+burst-1
// bucket lets the next Allow succeed.
func TestRefill(t *testing.T) {
	t.Parallel()
	cfg := mustValidate(t, throttlepolicy.Config{
		Defaults: throttlepolicy.Scoped{
			PerSession: throttlepolicy.Limit{Rate: "1/s", Burst: 1},
			PerTenant:  throttlepolicy.Limit{Rate: "100/s", Burst: 100},
		},
	})
	l := throttle.New(cfg)
	ctx := context.Background()
	if dec := l.AllowMCP(ctx, "s", "t", "x"); !dec.Allowed {
		t.Fatalf("first call should succeed: %+v", dec)
	}
	if dec := l.AllowMCP(ctx, "s", "t", "x"); dec.Allowed {
		t.Fatalf("second call should fail: %+v", dec)
	}
	time.Sleep(1100 * time.Millisecond)
	if dec := l.AllowMCP(ctx, "s", "t", "x"); !dec.Allowed {
		t.Fatalf("post-refill call should succeed: %+v", dec)
	}
}

// TestConcurrentSafety covers T17c: 1000 goroutines hammer the same
// bucket; total allows <= burst + rate*duration. -race must pass.
func TestConcurrentSafety(t *testing.T) {
	t.Parallel()
	cfg := mustValidate(t, throttlepolicy.Config{
		Defaults: throttlepolicy.Scoped{
			PerSession: throttlepolicy.Limit{Rate: "100/s", Burst: 50},
			PerTenant:  throttlepolicy.Limit{Rate: "1000/s", Burst: 500},
		},
	})
	l := throttle.New(cfg)

	const goroutines = 1000
	const callsEach = 5
	var allowed int64
	done := make(chan struct{}, goroutines)
	start := time.Now()
	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < callsEach; j++ {
				if l.AllowMCP(context.Background(), "shared-session", "shared-tenant", "tool.hot").Allowed {
					atomic.AddInt64(&allowed, 1)
				}
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
	elapsed := time.Since(start)

	// Per-session bucket is the bottleneck at 100/s+burst-50; total
	// admissions cannot exceed burst + rate*elapsed.
	upper := int64(50) + int64(100*elapsed.Seconds()) + 5 // +5 slack for clock noise
	if allowed > upper {
		t.Fatalf("allowed=%d exceeds upper bound %d (elapsed %v)", allowed, upper, elapsed)
	}
	if allowed == 0 {
		t.Fatalf("expected at least one admission, got 0 (elapsed %v)", elapsed)
	}
}

// TestTwoTierInteraction covers T17d: per-session OK but per-tenant
// exhausted -> denied with tenant retry-after; session tokens not
// consumed (the load-bearing r.Cancel() correctness check).
func TestTwoTierInteraction(t *testing.T) {
	t.Parallel()
	cfg := mustValidate(t, throttlepolicy.Config{
		Defaults: throttlepolicy.Scoped{
			PerSession: throttlepolicy.Limit{Rate: "100/s", Burst: 100},
			PerTenant:  throttlepolicy.Limit{Rate: "1/s", Burst: 1},
		},
	})
	l := throttle.New(cfg)
	ctx := context.Background()

	// Drain the tenant bucket.
	if dec := l.AllowMCP(ctx, "s", "t", "x"); !dec.Allowed {
		t.Fatalf("first call should succeed: %+v", dec)
	}
	// Second call: session still has 99 tokens, tenant has 0 -> deny on tenant.
	dec := l.AllowMCP(ctx, "s", "t", "x")
	if dec.Allowed {
		t.Fatalf("second call should be denied (tenant exhausted): %+v", dec)
	}
	if dec.Scope != throttle.ScopeTenant {
		t.Fatalf("denial scope should be tenant, got %v", dec)
	}

	// Switch the tenant to a NEW fresh one on every iteration — each
	// fresh tenant has its own bucket with the configured burst=1, so
	// we don't conflate the test's session-token-preservation check
	// with tenant-bucket exhaustion. The session bucket is what we're
	// verifying: if r.Cancel() did not run on the rejected session
	// reservation we'd see fewer than 99 session tokens available.
	allowed := 0
	for i := 0; i < 99; i++ {
		freshTenant := fmt.Sprintf("fresh-tenant-%d", i)
		if l.AllowMCP(ctx, "s", freshTenant, "x").Allowed {
			allowed++
		}
	}
	if allowed < 99 {
		t.Fatalf("expected 99 session tokens preserved after tenant-deny cancel, got %d", allowed)
	}
}

// TestNilLimiterIsOpen covers the --one-shot path and any test that
// constructs a Loop with no throttler: nil interface is open.
func TestNoOpLimiter(t *testing.T) {
	t.Parallel()
	l := throttle.NoOpLimiter()
	for i := 0; i < 100; i++ {
		if dec := l.AllowMCP(context.Background(), "s", "t", "x"); !dec.Allowed {
			t.Fatalf("no-op limiter should always allow, got %+v", dec)
		}
	}
}

func mustValidate(t *testing.T, cfg throttlepolicy.Config) throttlepolicy.Config {
	t.Helper()
	out, err := throttlepolicy.Validate(cfg)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	return out
}
