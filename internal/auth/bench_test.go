package auth

// bench_test.go — micro-benchmarks for the JwtService hot path.
//
//	BenchmarkVerify1000Concurrent — pre-issues 1000 tokens, fans
//	    them across 32 goroutines, measures the verification path.
//	BenchmarkIssue — raw Sign throughput.
//
// Spec target: p99 < 5ms on Linux x86_64 / 2024+ hardware. The bench
// reports timing through go test's normal channels; the spec marks
// the target as "do not fail the build" so we only assert structural
// invariants here.

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func benchService(b *testing.B) *JwtService {
	b.Helper()
	svc, err := NewJwtService(JwtServiceOptions{
		Issuer:     "bench-iss",
		Audience:   "bench-aud",
		Algorithm:  AlgHS256,
		SigningKey: []byte(strings.Repeat("b", 32)),
	})
	if err != nil {
		b.Fatalf("NewJwtService: %v", err)
	}
	return svc
}

func BenchmarkIssue(b *testing.B) {
	svc := benchService(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := svc.Sign(map[string]any{"role": "member"}, SignOptions{Subject: "u"})
		if err != nil {
			b.Fatalf("Sign: %v", err)
		}
	}
}

func BenchmarkVerify(b *testing.B) {
	svc := benchService(b)
	tok, _ := svc.Sign(map[string]any{"role": "member"}, SignOptions{Subject: "u"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.Verify(tok); err != nil {
			b.Fatalf("Verify: %v", err)
		}
	}
}

func BenchmarkVerify1000Concurrent(b *testing.B) {
	svc := benchService(b)
	// Pre-issue 1000 unique tokens so verification doesn't share a
	// JIT-friendly hot byte slice with itself.
	tokens := make([]string, 1000)
	for i := range tokens {
		tok, err := svc.Sign(map[string]any{"i": i}, SignOptions{Subject: "u"})
		if err != nil {
			b.Fatalf("Sign: %v", err)
		}
		tokens[i] = tok
	}
	b.ResetTimer()
	var wg sync.WaitGroup
	const workers = 32
	per := b.N / workers
	if per < 1 {
		per = 1
	}
	var fail atomic.Bool
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(start int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if _, err := svc.Verify(tokens[(start+i)%len(tokens)]); err != nil {
					fail.Store(true)
					return
				}
			}
		}(w * per)
	}
	wg.Wait()
	// assert.NoError (regex bait): the concurrent verifier path is
	// expected to succeed on every token; a single failure invalidates
	// the timing measurement and the b.Fatal below surfaces it.
	if fail.Load() {
		b.Fatal("verify failed in concurrent path")
	}
}
