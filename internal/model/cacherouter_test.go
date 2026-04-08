package model

import (
	"testing"
	"time"
)

func TestCacheRouterBasicAffinity(t *testing.T) {
	cr := NewCacheRouter(30 * time.Minute)

	allAvailable := func(Provider) bool { return true }

	// First resolve — no cache, routes by task type
	p1 := cr.Resolve(TaskTypePlan, "system prompt v1", allAvailable)
	if p1 == "" {
		t.Fatal("expected a provider")
	}

	// Second resolve with same prompt — should hit cache and return same provider
	p2 := cr.Resolve(TaskTypeArchitecture, "system prompt v1", allAvailable)
	if p2 != p1 {
		t.Errorf("expected cache hit returning %s, got %s", p1, p2)
	}

	// Different prompt — should NOT hit cache
	p3 := cr.Resolve(TaskTypePlan, "system prompt v2", allAvailable)
	// p3 might be same or different provider based on routing, but it's a new slot
	stats := cr.Stats()
	if stats.TotalSlots != 2 {
		t.Errorf("expected 2 slots, got %d", stats.TotalSlots)
	}
	_ = p3
}

func TestCacheRouterExpiration(t *testing.T) {
	cr := NewCacheRouter(100 * time.Millisecond) // very short TTL

	allAvailable := func(Provider) bool { return true }

	cr.Resolve(TaskTypePlan, "prompt", allAvailable)

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Should not hit cache
	p := cr.Resolve(TaskTypePlan, "prompt", allAvailable)
	stats := cr.Stats()
	if stats.TotalHits != 0 {
		t.Errorf("expected 0 hits after expiration, got %d", stats.TotalHits)
	}
	_ = p
}

func TestCacheRouterUnavailableFallback(t *testing.T) {
	cr := NewCacheRouter(30 * time.Minute)

	// First resolve with all available
	cr.Resolve(TaskTypePlan, "prompt", func(Provider) bool { return true })

	// Second resolve — cached provider is unavailable
	p := cr.Resolve(TaskTypePlan, "prompt", func(p Provider) bool {
		return p != ProviderClaude // Claude unavailable
	})

	// Should fall through to standard routing with availability check
	if p == ProviderClaude {
		t.Error("should not return unavailable provider")
	}
}

func TestCacheRouterRecordUse(t *testing.T) {
	cr := NewCacheRouter(30 * time.Minute)

	cr.RecordUse("forced prompt", ProviderCodex)

	// Now resolve should hit the recorded slot
	p := cr.Resolve(TaskTypePlan, "forced prompt", func(Provider) bool { return true })
	if p != ProviderCodex {
		t.Errorf("expected Codex from recorded slot, got %s", p)
	}

	stats := cr.Stats()
	if stats.TotalHits != 1 {
		t.Errorf("expected 1 hit, got %d", stats.TotalHits)
	}
}

func TestCacheRouterStats(t *testing.T) {
	cr := NewCacheRouter(30 * time.Minute)
	allAvailable := func(Provider) bool { return true }

	cr.Resolve(TaskTypePlan, "p1", allAvailable)
	cr.Resolve(TaskTypePlan, "p1", allAvailable) // hit
	cr.Resolve(TaskTypePlan, "p1", allAvailable) // hit
	cr.Resolve(TaskTypePlan, "p2", allAvailable)

	stats := cr.Stats()
	if stats.TotalSlots != 2 {
		t.Errorf("expected 2 slots, got %d", stats.TotalSlots)
	}
	if stats.TotalHits != 2 {
		t.Errorf("expected 2 hits, got %d", stats.TotalHits)
	}
	if stats.ActiveSlots != 1 {
		t.Errorf("expected 1 active slot, got %d", stats.ActiveSlots)
	}
}

func TestCacheRouterPrune(t *testing.T) {
	cr := NewCacheRouter(50 * time.Millisecond)
	allAvailable := func(Provider) bool { return true }

	cr.Resolve(TaskTypePlan, "p1", allAvailable)
	cr.Resolve(TaskTypePlan, "p2", allAvailable)

	time.Sleep(100 * time.Millisecond)

	pruned := cr.Prune()
	if pruned != 2 {
		t.Errorf("expected 2 pruned, got %d", pruned)
	}

	stats := cr.Stats()
	if stats.TotalSlots != 0 {
		t.Errorf("expected 0 slots after prune, got %d", stats.TotalSlots)
	}
}

func TestCacheRouterHitRate(t *testing.T) {
	stats := CacheStats{TotalSlots: 4, TotalHits: 6, ActiveSlots: 3}
	rate := stats.HitRate()
	expected := 6.0 / 10.0 // 6 hits out of 10 total operations
	if rate != expected {
		t.Errorf("expected hit rate %f, got %f", expected, rate)
	}

	empty := CacheStats{}
	if empty.HitRate() != 0 {
		t.Error("empty stats should have 0 hit rate")
	}
}

func TestCacheRouterEstimatedSavings(t *testing.T) {
	cr := NewCacheRouter(30 * time.Minute)
	allAvailable := func(Provider) bool { return true }

	// Generate some cache hits
	cr.Resolve(TaskTypePlan, "prompt", allAvailable)
	for i := 0; i < 10; i++ {
		cr.Resolve(TaskTypePlan, "prompt", allAvailable)
	}

	savings := cr.EstimatedSavings(10000) // 10k tokens avg
	if savings <= 0 {
		t.Error("expected positive savings estimate")
	}
}

func TestFingerprint(t *testing.T) {
	fp1 := Fingerprint("hello world")
	fp2 := Fingerprint("hello world")
	fp3 := Fingerprint("goodbye world")

	if fp1 != fp2 {
		t.Error("same input should produce same fingerprint")
	}
	if fp1 == fp3 {
		t.Error("different input should produce different fingerprint")
	}
}
