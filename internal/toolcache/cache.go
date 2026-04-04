// Package toolcache caches expensive tool results across conversation turns.
// Inspired by claw-code's result caching and JetBrains' observation reuse:
//
// Many agent turns re-read the same files, re-grep the same patterns, or
// re-glob the same directories. Caching these results:
// - Saves API round-trips (no need to re-execute tools)
// - Reduces latency (cached results return instantly)
// - Lowers cost (fewer tokens spent on redundant outputs)
//
// Cache invalidation is based on:
// - TTL (time-based expiry)
// - File modification times (stale reads detected)
// - Explicit invalidation (after writes)
package toolcache

import (
	"crypto/sha256"
	"fmt"
	"os"
	"sync"
	"time"
)

// Entry is a cached tool result.
type Entry struct {
	Key       string    `json:"key"`
	Tool      string    `json:"tool"`
	Args      string    `json:"args"`
	Result    string    `json:"result"`
	Tokens    int       `json:"tokens"` // estimated token cost of result
	CreatedAt time.Time `json:"created_at"`
	HitCount  int       `json:"hit_count"`
	FileMtime int64     `json:"file_mtime,omitempty"` // for file-based cache entries
	FilePath  string    `json:"file_path,omitempty"`
}

// Config controls cache behavior.
type Config struct {
	MaxEntries int           // max cached entries (default 500)
	TTL        time.Duration // default expiry (default 5min)
	MaxTokens  int           // max total cached tokens (default 100000)
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxEntries: 500,
		TTL:        5 * time.Minute,
		MaxTokens:  100000,
	}
}

// Cache stores tool results with LRU eviction.
type Cache struct {
	mu      sync.RWMutex
	config  Config
	entries map[string]*Entry
	order   []string // LRU order (most recent last)
	tokens  int      // total cached tokens
	stats   Stats
}

// Stats tracks cache performance.
type Stats struct {
	Hits        int `json:"hits"`
	Misses      int `json:"misses"`
	Evictions   int `json:"evictions"`
	Invalidated int `json:"invalidated"`
	TokensSaved int `json:"tokens_saved"` // tokens not re-generated
}

// New creates a cache with the given config.
func New(cfg Config) *Cache {
	if cfg.MaxEntries == 0 {
		cfg = DefaultConfig()
	}
	return &Cache{
		config:  cfg,
		entries: make(map[string]*Entry),
	}
}

// Get retrieves a cached result. Returns ("", false) on miss.
func (c *Cache) Get(tool, args string) (string, bool) {
	key := cacheKey(tool, args)

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		c.stats.Misses++
		return "", false
	}

	// Check TTL
	if time.Since(entry.CreatedAt) > c.config.TTL {
		c.evict(key)
		c.stats.Misses++
		return "", false
	}

	// Check file freshness
	if entry.FilePath != "" {
		info, err := os.Stat(entry.FilePath)
		if err != nil || info.ModTime().UnixNano() != entry.FileMtime {
			c.evict(key)
			c.stats.Misses++
			return "", false
		}
	}

	entry.HitCount++
	c.stats.Hits++
	c.stats.TokensSaved += entry.Tokens
	c.touch(key)
	return entry.Result, true
}

// Put stores a tool result in the cache.
func (c *Cache) Put(tool, args, result string, tokens int) {
	key := cacheKey(tool, args)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict if at capacity
	for len(c.entries) >= c.config.MaxEntries || (c.config.MaxTokens > 0 && c.tokens+tokens > c.config.MaxTokens) {
		if len(c.order) == 0 {
			break
		}
		c.evict(c.order[0])
	}

	entry := &Entry{
		Key:       key,
		Tool:      tool,
		Args:      args,
		Result:    result,
		Tokens:    tokens,
		CreatedAt: time.Now(),
	}

	c.entries[key] = entry
	c.order = append(c.order, key)
	c.tokens += tokens
}

// PutFile stores a file-read result with modification time tracking.
func (c *Cache) PutFile(tool, args, filePath, result string, tokens int) {
	key := cacheKey(tool, args)

	var mtime int64
	if info, err := os.Stat(filePath); err == nil {
		mtime = info.ModTime().UnixNano()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for len(c.entries) >= c.config.MaxEntries {
		if len(c.order) == 0 {
			break
		}
		c.evict(c.order[0])
	}

	entry := &Entry{
		Key:       key,
		Tool:      tool,
		Args:      args,
		Result:    result,
		Tokens:    tokens,
		CreatedAt: time.Now(),
		FilePath:  filePath,
		FileMtime: mtime,
	}

	c.entries[key] = entry
	c.order = append(c.order, key)
	c.tokens += tokens
}

// Invalidate removes a specific entry.
func (c *Cache) Invalidate(tool, args string) {
	key := cacheKey(tool, args)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; ok {
		c.evict(key)
		c.stats.Invalidated++
	}
}

// InvalidateFile removes all entries related to a file path.
func (c *Cache) InvalidateFile(filePath string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var toEvict []string
	for key, entry := range c.entries {
		if entry.FilePath == filePath {
			toEvict = append(toEvict, key)
		}
	}
	for _, key := range toEvict {
		c.evict(key)
		c.stats.Invalidated++
	}
}

// Clear removes all entries.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*Entry)
	c.order = nil
	c.tokens = 0
}

// Len returns the number of cached entries.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// GetStats returns cache performance metrics.
func (c *Cache) GetStats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stats
}

// HitRate returns the cache hit rate (0.0 - 1.0).
func (c *Cache) HitRate() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	total := c.stats.Hits + c.stats.Misses
	if total == 0 {
		return 0
	}
	return float64(c.stats.Hits) / float64(total)
}

func (c *Cache) evict(key string) {
	if entry, ok := c.entries[key]; ok {
		c.tokens -= entry.Tokens
		delete(c.entries, key)
		c.stats.Evictions++
	}
	// Remove from order
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

func (c *Cache) touch(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			c.order = append(c.order, key)
			break
		}
	}
}

func cacheKey(tool, args string) string {
	h := sha256.Sum256([]byte(tool + "\x00" + args))
	return fmt.Sprintf("%x", h[:8])
}
