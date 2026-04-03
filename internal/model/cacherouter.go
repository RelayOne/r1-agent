// cacherouter.go adds prompt cache-aware routing to the model router.
// Inspired by SOTA research: when tasks share the same system prompt and repo
// context, route them to the same provider to hit prompt cache. Anthropic
// prefix caching delivers ~90% cost reduction on cached tokens.
//
// Key insight: if task B uses the same system prompt as task A, and task A
// was routed to ProviderClaude, route task B to ProviderClaude too even if
// the routing table would otherwise prefer ProviderCodex.
package model

import (
	"hash/fnv"
	"sync"
	"time"
)

// CacheSlot tracks which provider holds a cached prompt for a given fingerprint.
type CacheSlot struct {
	Fingerprint uint64
	Provider    Provider
	HitCount    int
	CreatedAt   time.Time
	LastHitAt   time.Time
}

// CacheRouter augments the standard routing table with prompt cache affinity.
// When the system prompt fingerprint matches a cached slot, the same provider
// is preferred to benefit from API-level prompt caching.
type CacheRouter struct {
	mu    sync.Mutex
	slots map[uint64]*CacheSlot
	ttl   time.Duration
}

// NewCacheRouter creates a cache-aware router with a slot TTL.
// Slots expire after TTL to prevent stale affinity (default 30 minutes,
// matching Anthropic's cache TTL).
func NewCacheRouter(ttl time.Duration) *CacheRouter {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &CacheRouter{
		slots: make(map[uint64]*CacheSlot),
		ttl:   ttl,
	}
}

// Fingerprint computes a fast hash of the prompt content for cache keying.
func Fingerprint(prompt string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(prompt))
	return h.Sum64()
}

// Resolve returns the best provider considering both task type and cache affinity.
// If a cache slot exists for this prompt fingerprint, prefer that provider.
// Otherwise, fall through to standard task-type routing.
func (cr *CacheRouter) Resolve(taskType TaskType, systemPrompt string, isAvailable func(Provider) bool) Provider {
	fp := Fingerprint(systemPrompt)
	now := time.Now()

	cr.mu.Lock()
	defer cr.mu.Unlock()

	// Check for cached slot
	if slot, ok := cr.slots[fp]; ok {
		if now.Sub(slot.CreatedAt) < cr.ttl && isAvailable(slot.Provider) {
			slot.HitCount++
			slot.LastHitAt = now
			return slot.Provider
		}
		// Expired or unavailable — remove
		delete(cr.slots, fp)
	}

	// Fall through to standard routing
	provider := Resolve(taskType, isAvailable)

	// Record the slot
	cr.slots[fp] = &CacheSlot{
		Fingerprint: fp,
		Provider:    provider,
		HitCount:    0,
		CreatedAt:   now,
		LastHitAt:   now,
	}

	return provider
}

// RecordUse explicitly records a provider usage for a prompt fingerprint.
// Use this when routing happens outside CacheRouter (e.g., forced provider).
func (cr *CacheRouter) RecordUse(systemPrompt string, provider Provider) {
	fp := Fingerprint(systemPrompt)
	now := time.Now()

	cr.mu.Lock()
	defer cr.mu.Unlock()

	if slot, ok := cr.slots[fp]; ok {
		slot.Provider = provider
		slot.HitCount++
		slot.LastHitAt = now
	} else {
		cr.slots[fp] = &CacheSlot{
			Fingerprint: fp,
			Provider:    provider,
			CreatedAt:   now,
			LastHitAt:   now,
		}
	}
}

// Stats returns cache router statistics.
func (cr *CacheRouter) Stats() CacheStats {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	stats := CacheStats{
		TotalSlots: len(cr.slots),
	}

	for _, slot := range cr.slots {
		stats.TotalHits += slot.HitCount
		if slot.HitCount > 0 {
			stats.ActiveSlots++
		}
	}

	return stats
}

// CacheStats summarizes cache router state.
type CacheStats struct {
	TotalSlots  int `json:"total_slots"`
	ActiveSlots int `json:"active_slots"` // slots with at least one hit
	TotalHits   int `json:"total_hits"`
}

// HitRate returns the fraction of resolves that hit cache.
func (s CacheStats) HitRate() float64 {
	total := s.TotalHits + s.TotalSlots
	if total == 0 {
		return 0
	}
	return float64(s.TotalHits) / float64(total)
}

// Prune removes expired slots. Call periodically to prevent unbounded growth.
func (cr *CacheRouter) Prune() int {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	now := time.Now()
	pruned := 0
	for fp, slot := range cr.slots {
		if now.Sub(slot.CreatedAt) >= cr.ttl {
			delete(cr.slots, fp)
			pruned++
		}
	}
	return pruned
}

// EstimatedSavings estimates USD saved from cache hits.
// Based on Anthropic pricing: cache read is ~90% cheaper than fresh input.
// At $3/M input tokens and $0.30/M cached, savings = hits * avgTokens * $2.70/M.
func (cr *CacheRouter) EstimatedSavings(avgPromptTokens int) float64 {
	stats := cr.Stats()
	// $2.70 saved per million tokens per cache hit
	return float64(stats.TotalHits) * float64(avgPromptTokens) * 2.70 / 1_000_000.0
}
