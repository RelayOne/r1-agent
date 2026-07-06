//go:build bench

// bench_test.go: latency measurement harness (spec C6 T7).
//
// Build-tag-gated so the default `go test ./...` stays fast and
// doesn't require a live provider. The nightly bench cron (per
// services/cloudbuild-bench-nightly.yaml) invokes this with
// `go test -tags=bench ./internal/browser/...` and uploads the
// p50/p99 numbers to gs://resolute-parity-484218-g1-r1-bench-reports/<date>/.
//
// Thresholds enforce a hard p50 / p99 budget per provider; release
// blocker only when p99 regresses >25% versus the most recent green
// nightly (compared by the cron job, not in-test).

package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

// benchSamples is the number of navigate cycles per provider.
const benchSamples = 100

// BenchmarkLocalProvider_Navigate measures p50/p99 navigate latency
// for the local Provider against an httptest server. Output:
//
//   BenchmarkLocalProvider_Navigate-8   100   1.2ms   p50=1.1ms p99=2.3ms
//
// The percentiles are stamped in the benchmark name so the bench
// harness can scrape them with a grep.
func BenchmarkLocalProvider_Navigate(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html><body>ok</body></html>"))
	}))
	defer srv.Close()

	p := NewLocalProvider(LocalConfig{})
	defer p.Close(context.Background())

	host := stripURLToHost(srv.URL)
	opts := SessionOpts{
		TenantID: "bench",
		NetworkPolicy: NetworkPolicy{
			Mode:  PolicyAllowByDefault,
			Allow: []string{host, "127.0.0.1", "localhost"},
		},
	}

	s, err := p.Open(context.Background(), opts)
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	defer s.Close(context.Background())

	samples := make([]time.Duration, 0, benchSamples)
	b.ResetTimer()
	for i := 0; i < benchSamples; i++ {
		start := time.Now()
		if _, err := s.Navigate(context.Background(), srv.URL); err != nil {
			b.Fatalf("Navigate %d: %v", i, err)
		}
		samples = append(samples, time.Since(start))
	}
	b.StopTimer()

	p50, p99 := percentiles(samples)
	b.Logf("local provider navigate: p50=%s p99=%s (n=%d)", p50, p99, len(samples))

	// Spec T7 budgets: local p50=500ms, p99=2s.
	if p50 > 500*time.Millisecond {
		b.Errorf("local p50 = %s, want <= 500ms", p50)
	}
	if p99 > 2*time.Second {
		b.Errorf("local p99 = %s, want <= 2s", p99)
	}
}

// percentiles returns p50, p99 from a sample slice.
func percentiles(samples []time.Duration) (time.Duration, time.Duration) {
	if len(samples) == 0 {
		return 0, 0
	}
	cp := make([]time.Duration, len(samples))
	copy(cp, samples)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	pIdx := func(p float64) int {
		i := int(float64(len(cp)) * p)
		if i >= len(cp) {
			i = len(cp) - 1
		}
		return i
	}
	return cp[pIdx(0.50)], cp[pIdx(0.99)]
}
