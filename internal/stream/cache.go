package stream

import (
	"sync"
)

// PromptCacheStats tracks prompt cache hit/miss rates across executions.
// Inspired by claw-code-parity's prompt_cache.rs module.
// Anthropic's prompt caching can save 90% on input tokens for repeated context.
type PromptCacheStats struct {
	mu              sync.Mutex
	totalRequests   int
	cacheHits       int
	cacheMisses     int
	cacheCreations  int
	tokensSaved     int
	tokensCreated   int
	estimatedSaving float64 // USD saved via caching
}

// NewPromptCacheStats creates an empty cache stats tracker.
func NewPromptCacheStats() *PromptCacheStats {
	return &PromptCacheStats{}
}

// Record updates cache stats from a token usage report.
func (c *PromptCacheStats) Record(usage TokenUsage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.totalRequests++
	if usage.CacheRead > 0 {
		c.cacheHits++
		c.tokensSaved += usage.CacheRead
		// Cache reads cost 10% of normal input tokens
		// Normal: $3/MTok input, Cache read: $0.30/MTok
		c.estimatedSaving += float64(usage.CacheRead) * 2.70 / 1_000_000
	}
	if usage.CacheCreation > 0 {
		c.cacheCreations++
		c.tokensCreated += usage.CacheCreation
		// Cache creation costs 25% MORE than normal input
		// Normal: $3/MTok, Cache write: $3.75/MTok
		c.estimatedSaving -= float64(usage.CacheCreation) * 0.75 / 1_000_000
	}
	if usage.CacheRead == 0 && usage.CacheCreation == 0 {
		c.cacheMisses++
	}
}

// CacheStats is a snapshot of prompt cache performance.
type CacheStats struct {
	TotalRequests   int     `json:"total_requests"`
	CacheHits       int     `json:"cache_hits"`
	CacheMisses     int     `json:"cache_misses"`
	CacheCreations  int     `json:"cache_creations"`
	HitRate         float64 `json:"hit_rate"`
	TokensSaved     int     `json:"tokens_saved"`
	TokensCreated   int     `json:"tokens_created"`
	EstimatedSaving float64 `json:"estimated_saving_usd"`
}

// Stats returns a snapshot of the cache performance.
func (c *PromptCacheStats) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()

	hitRate := 0.0
	if c.totalRequests > 0 {
		hitRate = float64(c.cacheHits) / float64(c.totalRequests)
	}

	return CacheStats{
		TotalRequests:   c.totalRequests,
		CacheHits:       c.cacheHits,
		CacheMisses:     c.cacheMisses,
		CacheCreations:  c.cacheCreations,
		HitRate:         hitRate,
		TokensSaved:     c.tokensSaved,
		TokensCreated:   c.tokensCreated,
		EstimatedSaving: c.estimatedSaving,
	}
}

// Reset clears all cache stats.
func (c *PromptCacheStats) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totalRequests = 0
	c.cacheHits = 0
	c.cacheMisses = 0
	c.cacheCreations = 0
	c.tokensSaved = 0
	c.tokensCreated = 0
	c.estimatedSaving = 0
}
