// Package ctxpack implements adaptive context bin-packing.
// Inspired by Aider's repo-map context selection and claw-code's budget management:
//
// The context window is finite and expensive. Packing it optimally means:
// - Prioritize items by relevance to the current task
// - Fit as many high-value items as possible (bin-packing)
// - Reserve space for the response
// - Track what was included vs excluded for debugging
//
// Items have a token cost and a relevance score. The packer maximizes
// total relevance within the token budget — a bounded knapsack problem
// solved greedily by relevance-per-token ratio.
package ctxpack

import (
	"fmt"
	"sort"
	"strings"
)

// Item is a piece of context that could be included in the prompt.
type Item struct {
	ID        string  `json:"id"`
	Category  string  `json:"category"`  // "file", "symbol", "history", "tool_result", "system"
	Content   string  `json:"content"`
	Tokens    int     `json:"tokens"`
	Relevance float64 `json:"relevance"` // 0.0-1.0, higher is more relevant
	Required  bool    `json:"required"`  // must include (system prompts, etc.)
	Pinned    bool    `json:"pinned"`    // prefer to include even if low relevance
}

// PackResult describes what was packed and what was excluded.
type PackResult struct {
	Included    []Item `json:"included"`
	Excluded    []Item `json:"excluded"`
	TotalTokens int    `json:"total_tokens"`
	Budget      int    `json:"budget"`
	Utilization float64 `json:"utilization"` // 0.0-1.0
}

// Config controls packing behavior.
type Config struct {
	MaxTokens       int     // total context budget
	ReserveResponse int     // tokens reserved for response
	MinRelevance    float64 // exclude items below this relevance
}

// Pack selects the optimal set of items to fill the context budget.
func Pack(items []Item, cfg Config) PackResult {
	available := cfg.MaxTokens - cfg.ReserveResponse
	if available <= 0 {
		return PackResult{Excluded: items, Budget: cfg.MaxTokens}
	}

	// Separate required items first
	var required, optional []Item
	for _, item := range items {
		if item.Required {
			required = append(required, item)
		} else {
			optional = append(optional, item)
		}
	}

	result := PackResult{Budget: cfg.MaxTokens}

	// Include all required items
	for _, item := range required {
		result.Included = append(result.Included, item)
		result.TotalTokens += item.Tokens
	}

	// Filter by minimum relevance
	var candidates []Item
	for _, item := range optional {
		if cfg.MinRelevance > 0 && item.Relevance < cfg.MinRelevance && !item.Pinned {
			result.Excluded = append(result.Excluded, item)
		} else {
			candidates = append(candidates, item)
		}
	}

	// Sort by relevance-per-token ratio (greedy knapsack)
	sort.Slice(candidates, func(i, j int) bool {
		ri := candidates[i].Relevance / float64(maxInt(candidates[i].Tokens, 1))
		rj := candidates[j].Relevance / float64(maxInt(candidates[j].Tokens, 1))
		// Pinned items sort first
		if candidates[i].Pinned != candidates[j].Pinned {
			return candidates[i].Pinned
		}
		return ri > rj
	})

	// Greedily pack
	remaining := available - result.TotalTokens
	for _, item := range candidates {
		if item.Tokens <= remaining {
			result.Included = append(result.Included, item)
			result.TotalTokens += item.Tokens
			remaining -= item.Tokens
		} else {
			result.Excluded = append(result.Excluded, item)
		}
	}

	if available > 0 {
		result.Utilization = float64(result.TotalTokens) / float64(available)
	}

	return result
}

// PackWithCategories packs with per-category token limits.
func PackWithCategories(items []Item, cfg Config, categoryLimits map[string]int) PackResult {
	available := cfg.MaxTokens - cfg.ReserveResponse
	catUsed := make(map[string]int)

	var required, optional []Item
	for _, item := range items {
		if item.Required {
			required = append(required, item)
		} else {
			optional = append(optional, item)
		}
	}

	result := PackResult{Budget: cfg.MaxTokens}

	for _, item := range required {
		result.Included = append(result.Included, item)
		result.TotalTokens += item.Tokens
		catUsed[item.Category] += item.Tokens
	}

	sort.Slice(optional, func(i, j int) bool {
		ri := optional[i].Relevance / float64(maxInt(optional[i].Tokens, 1))
		rj := optional[j].Relevance / float64(maxInt(optional[j].Tokens, 1))
		if optional[i].Pinned != optional[j].Pinned {
			return optional[i].Pinned
		}
		return ri > rj
	})

	remaining := available - result.TotalTokens
	for _, item := range optional {
		if cfg.MinRelevance > 0 && item.Relevance < cfg.MinRelevance && !item.Pinned {
			result.Excluded = append(result.Excluded, item)
			continue
		}

		// Check category limit
		if limit, ok := categoryLimits[item.Category]; ok {
			if catUsed[item.Category]+item.Tokens > limit {
				result.Excluded = append(result.Excluded, item)
				continue
			}
		}

		if item.Tokens <= remaining {
			result.Included = append(result.Included, item)
			result.TotalTokens += item.Tokens
			catUsed[item.Category] += item.Tokens
			remaining -= item.Tokens
		} else {
			result.Excluded = append(result.Excluded, item)
		}
	}

	if available > 0 {
		result.Utilization = float64(result.TotalTokens) / float64(available)
	}
	return result
}

// Summary returns a human-readable packing summary.
func (r PackResult) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Packed %d items (%d tokens, %.0f%% utilization)\n",
		len(r.Included), r.TotalTokens, r.Utilization*100)
	if len(r.Excluded) > 0 {
		fmt.Fprintf(&b, "Excluded %d items\n", len(r.Excluded))
	}

	// Category breakdown
	cats := make(map[string]int)
	for _, item := range r.Included {
		cats[item.Category] += item.Tokens
	}
	for cat, tokens := range cats {
		fmt.Fprintf(&b, "  %s: %d tokens\n", cat, tokens)
	}
	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
