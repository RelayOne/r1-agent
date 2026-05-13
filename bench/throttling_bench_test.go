//go:build throttle_bench

// Package bench: throttling hot-path benchmark for C3 task T21.
//
// Acceptance criterion: WHEN 1000 concurrent goroutines each issue
// 100 calls to throttle.AllowMCP with distinct session_ids THE p99
// overhead per Allow SHALL be < 100us.
//
// The benchmark file lives in bench/ alongside the existing
// r1d_serve_bench_test.go pattern; the `throttle_bench` build tag
// keeps it out of the default `go test` run because it intentionally
// hammers the gate with a million calls (~30s wall on a laptop) and
// would slow CI.
//
// Run with:
//
//	go test -tags throttle_bench -run TestThrottleHotPath \
//	  ./bench -timeout=60s -race -v
//
// The harness reports avg / p50 / p95 / p99 per Allow and fails if
// p99 >= 100us. Failure prints the histogram so flaky CI hosts can
// be diagnosed without re-running.
package bench

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/throttle"
	throttlepolicy "github.com/RelayOne/r1/internal/throttle/policy"
)

const (
	benchGoroutines = 1000
	benchCallsEach  = 100
	benchTools      = 38
	benchTenants    = 10
	benchP99Budget  = 100 * time.Microsecond
)

// TestThrottleHotPath verifies the p99 Allow overhead claim.
//
// We do NOT exercise the deny path (that is the slow path and
// dominated by the bus emission). The fast path is allow-allow-allow
// against a generous burst so every call goes through the
// sync.Map.Load + Reserve + Cancel-on-no-delay branches that the
// spec calls out as the load-bearing hot path.
func TestThrottleHotPath(t *testing.T) {
	// Loose policy so virtually every call admits — we are measuring
	// the gate overhead, not the rejection latency.
	cfg, err := throttlepolicy.Validate(throttlepolicy.Config{
		Defaults: throttlepolicy.Scoped{
			PerSession: throttlepolicy.Limit{Rate: "1000000/s", Burst: 1000000},
			PerTenant:  throttlepolicy.Limit{Rate: "10000000/s", Burst: 10000000},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	l := throttle.New(cfg)

	// Pre-allocate sample buffers per goroutine to keep allocations
	// out of the hot path.
	samples := make([][]time.Duration, benchGoroutines)
	for i := range samples {
		samples[i] = make([]time.Duration, benchCallsEach)
	}

	var allowed int64
	var denied int64
	tools := []string{}
	for i := 0; i < benchTools; i++ {
		tools = append(tools, fmt.Sprintf("r1.bench.tool-%02d", i))
	}

	var wg sync.WaitGroup
	start := time.Now()
	for g := 0; g < benchGoroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			sessionID := fmt.Sprintf("session-%04d", g)
			tenantID := fmt.Sprintf("tenant-%d", g%benchTenants)
			ctx := context.Background()
			for c := 0; c < benchCallsEach; c++ {
				tool := tools[c%benchTools]
				t0 := time.Now()
				dec := l.AllowMCP(ctx, sessionID, tenantID, tool)
				samples[g][c] = time.Since(t0)
				if dec.Allowed {
					atomic.AddInt64(&allowed, 1)
				} else {
					atomic.AddInt64(&denied, 1)
				}
			}
		}(g)
	}
	wg.Wait()
	wall := time.Since(start)

	// Flatten and compute percentiles.
	flat := make([]time.Duration, 0, benchGoroutines*benchCallsEach)
	for _, row := range samples {
		flat = append(flat, row...)
	}
	sort.Slice(flat, func(i, j int) bool { return flat[i] < flat[j] })

	p50 := flat[len(flat)*50/100]
	p95 := flat[len(flat)*95/100]
	p99 := flat[len(flat)*99/100]
	pmax := flat[len(flat)-1]

	var total time.Duration
	for _, s := range flat {
		total += s
	}
	avg := time.Duration(int64(total) / int64(len(flat)))

	t.Logf("samples=%d allowed=%d denied=%d wall=%v", len(flat), allowed, denied, wall)
	t.Logf("avg=%v  p50=%v  p95=%v  p99=%v  max=%v", avg, p50, p95, p99, pmax)

	if p99 >= benchP99Budget {
		t.Fatalf("p99 %v exceeds budget %v (avg=%v p50=%v p95=%v max=%v)",
			p99, benchP99Budget, avg, p50, p95, pmax)
	}
}
