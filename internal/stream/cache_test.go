package stream

import (
	"testing"
)

func TestPromptCacheStats(t *testing.T) {
	stats := NewPromptCacheStats()

	// First request: cache creation
	stats.Record(TokenUsage{Input: 1000, Output: 200, CacheCreation: 500, CacheRead: 0})

	// Second request: cache hit
	stats.Record(TokenUsage{Input: 200, Output: 300, CacheCreation: 0, CacheRead: 800})

	// Third request: no cache
	stats.Record(TokenUsage{Input: 500, Output: 100, CacheCreation: 0, CacheRead: 0})

	s := stats.Stats()
	if s.TotalRequests != 3 {
		t.Errorf("expected 3 requests, got %d", s.TotalRequests)
	}
	if s.CacheHits != 1 {
		t.Errorf("expected 1 hit, got %d", s.CacheHits)
	}
	if s.CacheMisses != 1 {
		t.Errorf("expected 1 miss, got %d", s.CacheMisses)
	}
	if s.CacheCreations != 1 {
		t.Errorf("expected 1 creation, got %d", s.CacheCreations)
	}
	if s.TokensSaved != 800 {
		t.Errorf("expected 800 tokens saved, got %d", s.TokensSaved)
	}
	if s.TokensCreated != 500 {
		t.Errorf("expected 500 tokens created, got %d", s.TokensCreated)
	}
}

func TestPromptCacheStatsHitRate(t *testing.T) {
	stats := NewPromptCacheStats()

	// 4 requests, 3 cache hits
	stats.Record(TokenUsage{CacheRead: 100})
	stats.Record(TokenUsage{CacheRead: 200})
	stats.Record(TokenUsage{CacheRead: 300})
	stats.Record(TokenUsage{Input: 100})

	s := stats.Stats()
	if s.HitRate < 0.74 || s.HitRate > 0.76 {
		t.Errorf("expected ~0.75 hit rate, got %f", s.HitRate)
	}
}

func TestPromptCacheStatsReset(t *testing.T) {
	stats := NewPromptCacheStats()
	stats.Record(TokenUsage{CacheRead: 100})

	stats.Reset()
	s := stats.Stats()
	if s.TotalRequests != 0 {
		t.Errorf("expected 0 after reset, got %d", s.TotalRequests)
	}
}
