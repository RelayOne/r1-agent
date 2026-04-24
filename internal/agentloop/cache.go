package agentloop

import (
	"encoding/json"
	"sort"

	"github.com/RelayOne/r1/internal/provider"
)

// CacheConfig controls prompt caching behavior.
// Following the P61 research: tools → system → messages hierarchy,
// with explicit breakpoints on stable content for 90% cost reduction.
type CacheConfig struct {
	// CacheTools adds cache_control to the last tool definition.
	CacheTools bool
	// CacheSystem adds cache_control to the system prompt.
	CacheSystem bool
	// AutoCache adds top-level cache_control for automatic message caching.
	AutoCache bool
	// TTL sets cache duration: "5m" (default, refreshes on hit) or "1h" (2x write cost).
	TTL string
}

// DefaultCacheConfig returns the recommended caching configuration
// for multi-turn agentic loops. This achieves ~82% input cost reduction.
func DefaultCacheConfig() CacheConfig {
	return CacheConfig{
		CacheTools:  true,
		CacheSystem: true,
		AutoCache:   true,
		TTL:         "5m",
	}
}

// SortToolsDeterministic sorts tool definitions alphabetically by name.
// Non-deterministic tool ordering busts the cache on every turn.
// This is the single most common cache-busting anti-pattern.
func SortToolsDeterministic(tools []provider.ToolDef) []provider.ToolDef {
	sorted := make([]provider.ToolDef, len(tools))
	copy(sorted, tools)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})
	return sorted
}

// CacheBreakpoint is a cache_control annotation for prompt caching.
type CacheBreakpoint struct {
	Type string `json:"type"` // "ephemeral"
}

// SystemBlock is a system prompt block with optional cache control.
type SystemBlock struct {
	Type         string           `json:"type"`
	Text         string           `json:"text"`
	CacheControl *CacheBreakpoint `json:"cache_control,omitempty"`
}

// BuildCachedSystemPrompt splits the system prompt into cached (static)
// and uncached (dynamic) sections. The static section gets a cache breakpoint.
func BuildCachedSystemPrompt(static, dynamic string) []SystemBlock {
	blocks := []SystemBlock{
		{
			Type:         "text",
			Text:         static,
			CacheControl: &CacheBreakpoint{Type: "ephemeral"},
		},
	}
	if dynamic != "" {
		blocks = append(blocks, SystemBlock{
			Type: "text",
			Text: dynamic,
		})
	}
	return blocks
}

// EstimateToolTokens estimates the token overhead for a set of tools.
// Each tool roughly costs: ~20 base + len(description)/4 + schema_tokens.
// Built-in tools have known fixed costs: bash ~245, text_editor ~700.
func EstimateToolTokens(tools []provider.ToolDef) int {
	const fixedOverhead = 346 // tool_choice: auto overhead
	total := fixedOverhead
	for _, t := range tools {
		switch t.Name {
		case "bash":
			total += 245
		case "str_replace_based_edit_tool":
			total += 700
		default:
			// ~20 base + description + schema
			total += 20 + len(t.Description)/4
			if t.InputSchema != nil {
				total += len(t.InputSchema) / 4
			}
		}
	}
	return total
}

// CacheSavingsEstimate estimates the cost savings from caching for a
// given number of turns. Returns (cost_without_cache, cost_with_cache).
func CacheSavingsEstimate(systemTokens, toolTokens, avgTurnTokens, turns int, model string) (float64, float64) {
	var inputPrice, cacheWritePrice, cacheReadPrice float64
	switch {
	case contains(model, "opus"):
		inputPrice = 5.0 / 1_000_000
		cacheWritePrice = 6.25 / 1_000_000
		cacheReadPrice = 0.50 / 1_000_000
	case contains(model, "haiku"):
		inputPrice = 1.0 / 1_000_000
		cacheWritePrice = 1.25 / 1_000_000
		cacheReadPrice = 0.10 / 1_000_000
	default:
		inputPrice = 3.0 / 1_000_000
		cacheWritePrice = 3.75 / 1_000_000
		cacheReadPrice = 0.30 / 1_000_000
	}

	cachedPrefix := systemTokens + toolTokens

	// Without caching: full input on every turn
	var noCacheTotal float64
	for t := 0; t < turns; t++ {
		turnTokens := cachedPrefix + avgTurnTokens*t
		noCacheTotal += float64(turnTokens) * inputPrice
	}

	// With caching: cache write on turn 1, cache read on subsequent turns
	var withCacheTotal float64
	withCacheTotal += float64(cachedPrefix) * cacheWritePrice                    // turn 1: write
	withCacheTotal += float64(avgTurnTokens) * inputPrice                        // turn 1: new content
	for t := 1; t < turns; t++ {
		withCacheTotal += float64(cachedPrefix) * cacheReadPrice                  // cache read
		withCacheTotal += float64(avgTurnTokens) * inputPrice                    // new content
		withCacheTotal += float64(avgTurnTokens) * cacheWritePrice               // incrementally cache history
	}

	return noCacheTotal, withCacheTotal
}

// BuiltinBashTool returns the Anthropic built-in bash tool definition.
// Using built-in tools is recommended as Claude is specifically trained on them.
func BuiltinBashTool() json.RawMessage {
	return json.RawMessage(`{"type": "bash_20250124", "name": "bash"}`)
}

// BuiltinTextEditorTool returns the Anthropic built-in text editor tool definition.
func BuiltinTextEditorTool() json.RawMessage {
	return json.RawMessage(`{"type": "text_editor_20250728", "name": "str_replace_based_edit_tool"}`)
}
