// Package ratelimit implements a client-side rate limiter using the token bucket algorithm.
// Inspired by claw-code's API rate management and Aider's retry-after handling:
//
// Prevents hitting provider rate limits by enforcing client-side throttling.
// - Token bucket for smooth request distribution
// - Per-provider limits (different APIs have different limits)
// - Retry-After header integration
// - Request cost weighting (large context = more tokens consumed)
//
// Hitting rate limits wastes time (retry delays) and money (partial responses).
// Client-side throttling keeps throughput smooth.
package ratelimit

import (
	"fmt"
	"sync"
	"time"
)

// Config for a rate limiter.
type Config struct {
	RequestsPerMinute int           // max requests per minute
	TokensPerMinute   int           // max tokens per minute
	BurstSize         int           // max burst (bucket capacity)
	MinInterval       time.Duration // minimum time between requests
}

// DefaultConfigs for common providers.
var DefaultConfigs = map[string]Config{
	"anthropic": {
		RequestsPerMinute: 60,
		TokensPerMinute:   100000,
		BurstSize:         5,
		MinInterval:       100 * time.Millisecond,
	},
	"openai": {
		RequestsPerMinute: 60,
		TokensPerMinute:   150000,
		BurstSize:         10,
		MinInterval:       50 * time.Millisecond,
	},
	"openrouter": {
		RequestsPerMinute: 200,
		TokensPerMinute:   500000,
		BurstSize:         20,
		MinInterval:       50 * time.Millisecond,
	},
}

// Limiter enforces rate limits using a token bucket.
type Limiter struct {
	mu          sync.Mutex
	config      Config
	tokens      float64   // current token count in bucket
	lastRefill  time.Time
	lastRequest time.Time
	retryAfter  time.Time // honor Retry-After header
	stats       Stats
}

// Stats tracks rate limiter metrics.
type Stats struct {
	Allowed   int `json:"allowed"`
	Throttled int `json:"throttled"`
	Waited    int `json:"waited"` // times we waited for tokens
}

// NewLimiter creates a limiter with the given config.
func NewLimiter(cfg Config) *Limiter {
	if cfg.BurstSize == 0 {
		cfg.BurstSize = cfg.RequestsPerMinute / 10
		if cfg.BurstSize < 1 {
			cfg.BurstSize = 1
		}
	}
	return &Limiter{
		config:     cfg,
		tokens:     float64(cfg.BurstSize),
		lastRefill: time.Now(),
	}
}

// ForProvider creates a limiter with provider-specific defaults.
func ForProvider(provider string) *Limiter {
	cfg, ok := DefaultConfigs[provider]
	if !ok {
		cfg = DefaultConfigs["anthropic"] // safe default
	}
	return NewLimiter(cfg)
}

// Allow checks if a request can proceed right now.
// Returns true if allowed, false if should wait.
func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()

	// Honor Retry-After
	if now.Before(l.retryAfter) {
		l.stats.Throttled++
		return false
	}

	// Enforce minimum interval
	if l.config.MinInterval > 0 && now.Sub(l.lastRequest) < l.config.MinInterval {
		l.stats.Throttled++
		return false
	}

	l.refill(now)

	if l.tokens < 1 {
		l.stats.Throttled++
		return false
	}

	l.tokens--
	l.lastRequest = now
	l.stats.Allowed++
	return true
}

// Wait blocks until a request is allowed or returns the wait duration.
// Does not actually sleep — caller decides whether to wait.
func (l *Limiter) Wait() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()

	// Honor Retry-After
	if now.Before(l.retryAfter) {
		return l.retryAfter.Sub(now)
	}

	// Minimum interval
	if l.config.MinInterval > 0 {
		elapsed := now.Sub(l.lastRequest)
		if elapsed < l.config.MinInterval {
			return l.config.MinInterval - elapsed
		}
	}

	l.refill(now)

	if l.tokens >= 1 {
		return 0 // can proceed immediately
	}

	// Calculate time until next token
	rate := float64(l.config.RequestsPerMinute) / 60.0
	if rate <= 0 {
		return time.Minute
	}
	needed := 1.0 - l.tokens
	wait := time.Duration(needed/rate*1000) * time.Millisecond
	l.stats.Waited++
	return wait
}

// AllowN checks if N tokens worth of work can proceed.
func (l *Limiter) AllowN(n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if now.Before(l.retryAfter) {
		l.stats.Throttled++
		return false
	}

	l.refill(now)

	cost := float64(n)
	if l.tokens < cost {
		l.stats.Throttled++
		return false
	}

	l.tokens -= cost
	l.lastRequest = now
	l.stats.Allowed++
	return true
}

// SetRetryAfter tells the limiter to pause until the given time.
// Used when receiving a 429 response with Retry-After header.
func (l *Limiter) SetRetryAfter(until time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until.After(l.retryAfter) {
		l.retryAfter = until
	}
}

// SetRetryAfterDuration pauses for the given duration.
func (l *Limiter) SetRetryAfterDuration(d time.Duration) {
	l.SetRetryAfter(time.Now().Add(d))
}

// Stats returns current metrics.
func (l *Limiter) GetStats() Stats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stats
}

// Reset clears the limiter state.
func (l *Limiter) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tokens = float64(l.config.BurstSize)
	l.lastRefill = time.Now()
	l.retryAfter = time.Time{}
	l.stats = Stats{}
}

func (l *Limiter) refill(now time.Time) {
	elapsed := now.Sub(l.lastRefill).Seconds()
	rate := float64(l.config.RequestsPerMinute) / 60.0
	l.tokens += elapsed * rate
	if l.tokens > float64(l.config.BurstSize) {
		l.tokens = float64(l.config.BurstSize)
	}
	l.lastRefill = now
}

// MultiLimiter combines multiple limiters (e.g., request + token limits).
type MultiLimiter struct {
	limiters map[string]*Limiter
}

// NewMultiLimiter creates a composite limiter.
func NewMultiLimiter() *MultiLimiter {
	return &MultiLimiter{limiters: make(map[string]*Limiter)}
}

// Add registers a named limiter.
func (m *MultiLimiter) Add(name string, l *Limiter) {
	m.limiters[name] = l
}

// Allow checks all limiters. All must allow for the request to proceed.
func (m *MultiLimiter) Allow() bool {
	for _, l := range m.limiters {
		if !l.Allow() {
			return false
		}
	}
	return true
}

// Wait returns the maximum wait across all limiters.
func (m *MultiLimiter) Wait() time.Duration {
	var maxWait time.Duration
	for _, l := range m.limiters {
		w := l.Wait()
		if w > maxWait {
			maxWait = w
		}
	}
	return maxWait
}

// Summary returns a human-readable status.
func (m *MultiLimiter) Summary() string {
	var parts []string
	for name, l := range m.limiters {
		s := l.GetStats()
		parts = append(parts, fmt.Sprintf("%s: %d allowed, %d throttled", name, s.Allowed, s.Throttled))
	}
	if len(parts) == 0 {
		return "no limiters configured"
	}
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "; "
		}
		result += p
	}
	return result
}
