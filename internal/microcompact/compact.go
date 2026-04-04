// Package microcompact provides cache-aligned context compaction.
// Inspired by Claude Code's progressive context compaction and
// prompt cache optimization strategies:
//
// LLM APIs cache prefixes of conversations. When context grows too large,
// naive truncation breaks the cache prefix, causing expensive re-processing.
// MicroCompact preserves cache alignment by:
// 1. Identifying static (cacheable) vs dynamic sections
// 2. Compacting dynamic sections first (summaries, old results)
// 3. Preserving the static prefix intact to maintain cache hits
// 4. Using tiered compression: verbatim → summarized → dropped
//
// This reduces both token usage and API costs by keeping cache hit rates high.
package microcompact

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
)

// Tier controls compression aggressiveness.
type Tier int

const (
	TierVerbatim   Tier = iota // keep as-is
	TierSummarized             // compress to summary
	TierDropped                // remove entirely
)

// Section is a labeled block of context.
type Section struct {
	Label    string `json:"label"`
	Content  string `json:"content"`
	Static   bool   `json:"static"`    // static sections form the cacheable prefix
	Priority int    `json:"priority"`  // higher = keep longer
	Tokens   int    `json:"tokens"`    // estimated token count
	MinTier  Tier   `json:"min_tier"`  // minimum compression tier allowed
}

// CompactResult is the output of a compaction pass.
type CompactResult struct {
	Sections   []OutputSection `json:"sections"`
	TotalIn    int             `json:"total_in"`    // input tokens
	TotalOut   int             `json:"total_out"`   // output tokens
	CacheBreak bool            `json:"cache_break"` // true if static prefix changed
	PrefixHash string          `json:"prefix_hash"` // hash of static prefix for cache tracking
	Dropped    []string        `json:"dropped"`     // labels of dropped sections
	Summarized []string        `json:"summarized"`  // labels of summarized sections
}

// OutputSection is a section after compaction.
type OutputSection struct {
	Label   string `json:"label"`
	Content string `json:"content"`
	Tier    Tier   `json:"tier"`
	Tokens  int    `json:"tokens"`
}

// SummaryFunc generates a compressed summary of content.
// Returns the summary and its estimated token count.
type SummaryFunc func(label, content string) (string, int)

// Compactor manages cache-aligned context compaction.
type Compactor struct {
	mu          sync.Mutex
	maxTokens   int
	summarize   SummaryFunc
	prefixHash  string
	cacheBreaks int
	compactions int
	tokensSaved int
}

// Config for the compactor.
type Config struct {
	MaxTokens   int         // target token budget
	SummarizeFn SummaryFunc // function to summarize sections (nil = truncate)
}

// NewCompactor creates a compactor with the given config.
func NewCompactor(cfg Config) *Compactor {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 100000
	}
	if cfg.SummarizeFn == nil {
		cfg.SummarizeFn = defaultSummarize
	}
	return &Compactor{
		maxTokens: cfg.MaxTokens,
		summarize: cfg.SummarizeFn,
	}
}

// Compact reduces sections to fit within the token budget while
// preserving cache alignment. Static sections are kept verbatim
// at the front; dynamic sections are compressed by priority.
func (c *Compactor) Compact(sections []Section) CompactResult {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.compactions++

	totalIn := 0
	for _, s := range sections {
		totalIn += s.Tokens
	}

	// Separate static (cacheable prefix) from dynamic sections
	var statics, dynamics []Section
	for _, s := range sections {
		if s.Static {
			statics = append(statics, s)
		} else {
			dynamics = append(dynamics, s)
		}
	}

	// Sort dynamics by priority (lowest first = compact first)
	sortByPriority(dynamics)

	// Calculate static prefix size
	staticTokens := 0
	for _, s := range statics {
		staticTokens += s.Tokens
	}

	// Check for cache break
	newHash := hashSections(statics)
	cacheBreak := c.prefixHash != "" && c.prefixHash != newHash
	if cacheBreak {
		c.cacheBreaks++
	}
	c.prefixHash = newHash

	// Build output: static sections first (verbatim)
	var out []OutputSection
	for _, s := range statics {
		out = append(out, OutputSection{
			Label:   s.Label,
			Content: s.Content,
			Tier:    TierVerbatim,
			Tokens:  s.Tokens,
		})
	}

	// Budget remaining for dynamic sections
	budget := c.maxTokens - staticTokens
	if budget < 0 {
		budget = 0
	}

	var dropped, summarized []string
	used := 0

	// Process dynamics from highest to lowest priority
	for i := len(dynamics) - 1; i >= 0; i-- {
		s := dynamics[i]
		if used+s.Tokens <= budget {
			// Fits verbatim
			out = append(out, OutputSection{
				Label:   s.Label,
				Content: s.Content,
				Tier:    TierVerbatim,
				Tokens:  s.Tokens,
			})
			used += s.Tokens
		} else if s.MinTier >= TierSummarized {
			// Try summarizing
			summary, tokens := c.summarize(s.Label, s.Content)
			if used+tokens <= budget {
				out = append(out, OutputSection{
					Label:   s.Label,
					Content: summary,
					Tier:    TierSummarized,
					Tokens:  tokens,
				})
				used += tokens
				summarized = append(summarized, s.Label)
			} else if s.MinTier >= TierDropped {
				dropped = append(dropped, s.Label)
			} else {
				// Must keep at least summarized - force it in
				out = append(out, OutputSection{
					Label:   s.Label,
					Content: summary,
					Tier:    TierSummarized,
					Tokens:  tokens,
				})
				used += tokens
				summarized = append(summarized, s.Label)
			}
		} else {
			// Can't compress, must keep verbatim even over budget
			out = append(out, OutputSection{
				Label:   s.Label,
				Content: s.Content,
				Tier:    TierVerbatim,
				Tokens:  s.Tokens,
			})
			used += s.Tokens
		}
	}

	totalOut := staticTokens + used
	c.tokensSaved += totalIn - totalOut

	return CompactResult{
		Sections:   out,
		TotalIn:    totalIn,
		TotalOut:   totalOut,
		CacheBreak: cacheBreak,
		PrefixHash: newHash,
		Dropped:    dropped,
		Summarized: summarized,
	}
}

// Stats returns compaction statistics.
type Stats struct {
	Compactions int    `json:"compactions"`
	CacheBreaks int    `json:"cache_breaks"`
	TokensSaved int    `json:"tokens_saved"`
	PrefixHash  string `json:"prefix_hash"`
}

// Stats returns current compaction statistics.
func (c *Compactor) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		Compactions: c.compactions,
		CacheBreaks: c.cacheBreaks,
		TokensSaved: c.tokensSaved,
		PrefixHash:  c.prefixHash,
	}
}

// EstimateTokens provides a rough token estimate for text.
// Uses the ~4 chars per token heuristic common across models.
func EstimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	// Rough approximation: 1 token ≈ 4 characters
	return (len(text) + 3) / 4
}

// BuildSections creates Section objects from labeled content blocks.
func BuildSections(blocks map[string]string, staticLabels map[string]bool, priorities map[string]int) []Section {
	var sections []Section
	for label, content := range blocks {
		tokens := EstimateTokens(content)
		priority := priorities[label] // 0 if not set
		isStatic := staticLabels[label]
		sections = append(sections, Section{
			Label:    label,
			Content:  content,
			Static:   isStatic,
			Priority: priority,
			Tokens:   tokens,
		})
	}
	return sections
}

// Render concatenates compacted sections into a single string.
func Render(result CompactResult) string {
	var b strings.Builder
	for i, s := range result.Sections {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(s.Content)
	}
	return b.String()
}

func defaultSummarize(label, content string) (string, int) {
	// Simple truncation: keep first ~25% of content
	lines := strings.Split(content, "\n")
	keep := len(lines) / 4
	if keep < 1 {
		keep = 1
	}
	if keep > 10 {
		keep = 10
	}
	summary := strings.Join(lines[:keep], "\n")
	summary = fmt.Sprintf("[Summarized %s: %d lines → %d lines]\n%s", label, len(lines), keep, summary)
	return summary, EstimateTokens(summary)
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

func hashSections(sections []Section) string {
	h := sha256.New()
	for _, s := range sections {
		h.Write([]byte(s.Label))
		h.Write([]byte(s.Content))
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}
