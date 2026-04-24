// Package promptcache optimizes prompts for maximum cache hits.
// Inspired by Anthropic's prompt caching and claw-code's cache-stable prompts:
//
// Anthropic charges 90% less for cached prompt tokens. This package:
// - Separates static content (instructions, repo map) from dynamic (task, context)
// - Places static content first (cache key is prefix-based)
// - Detects cache breaks when static content changes
// - Tracks cache hit rates and token savings
// - Suggests prompt restructuring for better cache utilization
package promptcache

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Section is a labeled chunk of prompt content.
type Section struct {
	Label    string `json:"label"`
	Content  string `json:"content"`
	Static   bool   `json:"static"`   // true = doesn't change between calls
	Priority int    `json:"priority"` // lower = placed first
	Tokens   int    `json:"tokens"`
}

// OptimizedPrompt is a cache-optimized prompt.
type OptimizedPrompt struct {
	System   string   `json:"system"`
	User     string   `json:"user"`
	Sections []Section `json:"sections"`
	StaticHash string  `json:"static_hash"`
	StaticTokens  int `json:"static_tokens"`
	DynamicTokens int `json:"dynamic_tokens"`
}

// CacheStats tracks cache performance.
type CacheStats struct {
	mu            sync.Mutex
	Hits          int     `json:"hits"`
	Misses        int     `json:"misses"`
	Breaks        int     `json:"breaks"`       // cache invalidations
	TokensSaved   int     `json:"tokens_saved"` // estimated tokens saved by caching
	LastBreak     time.Time `json:"last_break,omitempty"`
	LastBreakCause string  `json:"last_break_cause,omitempty"`
}

// Optimizer builds cache-optimized prompts.
type Optimizer struct {
	mu            sync.RWMutex
	sections      []Section
	lastStaticHash string
	stats         CacheStats
}

// New creates an optimizer.
func New() *Optimizer {
	return &Optimizer{}
}

// AddSection registers a prompt section.
func (o *Optimizer) AddSection(s Section) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if s.Tokens == 0 {
		s.Tokens = estimateTokens(s.Content)
	}
	o.sections = append(o.sections, s)
}

// SetSections replaces all sections.
func (o *Optimizer) SetSections(sections []Section) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i := range sections {
		if sections[i].Tokens == 0 {
			sections[i].Tokens = estimateTokens(sections[i].Content)
		}
	}
	o.sections = sections
}

// Build constructs an optimized prompt with static content first.
func (o *Optimizer) Build(dynamicContent string) *OptimizedPrompt {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Separate static and dynamic sections
	var staticSections, dynamicSections []Section
	for _, s := range o.sections {
		if s.Static {
			staticSections = append(staticSections, s)
		} else {
			dynamicSections = append(dynamicSections, s)
		}
	}

	// Sort static by priority (lower first = placed at start for cache key)
	sortByPriority(staticSections)
	sortByPriority(dynamicSections)

	// Build system prompt: static sections first
	var system strings.Builder
	staticTokens := 0
	for _, s := range staticSections {
		if system.Len() > 0 {
			system.WriteString("\n\n")
		}
		if s.Label != "" {
			fmt.Fprintf(&system, "## %s\n\n", s.Label)
		}
		system.WriteString(s.Content)
		staticTokens += s.Tokens
	}

	// Add dynamic sections after static
	dynamicTokens := 0
	for _, s := range dynamicSections {
		if system.Len() > 0 {
			system.WriteString("\n\n")
		}
		if s.Label != "" {
			fmt.Fprintf(&system, "## %s\n\n", s.Label)
		}
		system.WriteString(s.Content)
		dynamicTokens += s.Tokens
	}

	// Compute static hash for cache break detection
	staticHash := hashContent(system.String()[:min(system.Len(), staticTokens*4)])

	// Detect cache breaks
	if o.lastStaticHash != "" && o.lastStaticHash != staticHash {
		o.stats.mu.Lock()
		o.stats.Breaks++
		o.stats.Misses++
		o.stats.LastBreak = time.Now()
		o.stats.LastBreakCause = "static content changed"
		o.stats.mu.Unlock()
	} else if o.lastStaticHash != "" {
		o.stats.mu.Lock()
		o.stats.Hits++
		o.stats.TokensSaved += staticTokens
		o.stats.mu.Unlock()
	}
	o.lastStaticHash = staticHash

	allSections := make([]Section, 0, len(staticSections)+len(dynamicSections))
	allSections = append(allSections, staticSections...)
	allSections = append(allSections, dynamicSections...)

	return &OptimizedPrompt{
		System:        system.String(),
		User:          dynamicContent,
		Sections:      allSections,
		StaticHash:    staticHash,
		StaticTokens:  staticTokens,
		DynamicTokens: dynamicTokens + estimateTokens(dynamicContent),
	}
}

// Stats returns cache performance statistics.
func (o *Optimizer) Stats() CacheStats {
	o.stats.mu.Lock()
	defer o.stats.mu.Unlock()
	return CacheStats{
		Hits:           o.stats.Hits,
		Misses:         o.stats.Misses,
		Breaks:         o.stats.Breaks,
		TokensSaved:    o.stats.TokensSaved,
		LastBreak:      o.stats.LastBreak,
		LastBreakCause: o.stats.LastBreakCause,
	}
}

// HitRate returns the cache hit rate (0-1).
func (o *Optimizer) HitRate() float64 {
	s := o.Stats()
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(total)
}

// Suggestions returns optimization suggestions for the current sections.
func (o *Optimizer) Suggestions() []string {
	o.mu.RLock()
	defer o.mu.RUnlock()

	var suggestions []string

	// Check if dynamic content is before static
	lastStaticIdx := -1
	firstDynamicIdx := -1
	for i, s := range o.sections {
		if s.Static {
			lastStaticIdx = i
		} else if firstDynamicIdx == -1 {
			firstDynamicIdx = i
		}
	}
	if firstDynamicIdx >= 0 && lastStaticIdx > firstDynamicIdx {
		suggestions = append(suggestions, "Move static sections before dynamic sections for better cache utilization")
	}

	// Check for large dynamic sections that could be made static
	for _, s := range o.sections {
		if !s.Static && s.Tokens > 1000 {
			suggestions = append(suggestions,
				fmt.Sprintf("Section %q is large (%d tokens) and dynamic - consider making it static or splitting it", s.Label, s.Tokens))
		}
	}

	// Check total size
	total := 0
	for _, s := range o.sections {
		total += s.Tokens
	}
	if total > 100000 {
		suggestions = append(suggestions,
			fmt.Sprintf("Total prompt is very large (%d tokens) - consider trimming low-priority sections", total))
	}

	return suggestions
}

// EstimateSavings estimates the cost savings from caching.
func (o *Optimizer) EstimateSavings(inputPricePerMToken float64) float64 {
	s := o.Stats()
	if s.TokensSaved == 0 {
		return 0
	}
	// Cached tokens cost 10% of input price
	fullCost := float64(s.TokensSaved) * inputPricePerMToken / 1_000_000
	cachedCost := fullCost * 0.1
	return fullCost - cachedCost
}

func sortByPriority(sections []Section) {
	for i := 0; i < len(sections); i++ {
		for j := i + 1; j < len(sections); j++ {
			if sections[j].Priority < sections[i].Priority {
				sections[i], sections[j] = sections[j], sections[i]
			}
		}
	}
}

func hashContent(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:8])
}

func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}
