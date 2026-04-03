// masking.go implements observation masking for context compaction.
// Inspired by JetBrains Research (Dec 2025): replace old tool outputs with
// compact placeholders while keeping action/reasoning history intact.
// This is 52% cheaper than baseline and improved solve rates by 2.6%.
//
// Masking order (from cheapest to most expensive):
// 1. Observation masking (this file): replace old tool outputs with placeholders
// 2. Gentle compaction: truncate remaining long outputs
// 3. LLM summarization: use a model to compress (most expensive)
//
// The key insight: tool OUTPUTS are often large (file reads, command output) but
// the fact that the tool was called and what it found is more important than the
// raw output. Masking preserves the action history while dramatically reducing tokens.
package context

import (
	"fmt"
	"strings"
)

// MaskConfig controls observation masking behavior.
type MaskConfig struct {
	// WindowSize is the number of recent tool outputs to keep unmasked.
	// JetBrains found 10 turns optimal. Default: 10.
	WindowSize int

	// PreserveLabels controls which block labels are never masked.
	// e.g., "system", "task", "scope" should always be preserved.
	PreserveLabels map[string]bool

	// MaxOutputTokens masks outputs larger than this threshold regardless of age.
	// Default: 2000 tokens.
	MaxOutputTokens int
}

// DefaultMaskConfig returns the JetBrains-recommended defaults.
func DefaultMaskConfig() MaskConfig {
	return MaskConfig{
		WindowSize:      10,
		MaxOutputTokens: 2000,
		PreserveLabels: map[string]bool{
			"system":    true,
			"task":      true,
			"scope":     true,
			"plan":      true,
			"reminder":  true,
			"error":     true,
			"claude.md": true,
		},
	}
}

// MaskResult tracks what was masked.
type MaskResult struct {
	BlocksMasked int
	TokensSaved  int
	TokensBefore int
	TokensAfter  int
}

// MaskObservations replaces old tool outputs with compact placeholders.
// This is the cheapest compaction strategy and should be tried FIRST,
// before truncation or LLM summarization.
//
// Returns the result of masking. Modifies blocks in place.
func (m *Manager) MaskObservations(cfg MaskConfig) MaskResult {
	if cfg.WindowSize <= 0 {
		cfg.WindowSize = 10
	}
	if cfg.MaxOutputTokens <= 0 {
		cfg.MaxOutputTokens = 2000
	}

	result := MaskResult{
		TokensBefore: m.TotalTokens(),
	}

	// Find tool output blocks (by label or tier)
	type indexedBlock struct {
		index int
		block *ContextBlock
	}
	var toolOutputs []indexedBlock

	for i := range m.blocks {
		b := &m.blocks[i]

		// Skip preserved labels
		if cfg.PreserveLabels[b.Label] {
			continue
		}

		// Skip active tier (current turn)
		if b.Tier == TierActive {
			continue
		}

		if b.Label == "tool_output" || b.Label == "file_read" || b.Label == "command_output" || b.Label == "search_result" {
			toolOutputs = append(toolOutputs, indexedBlock{i, b})
		}
	}

	// Keep the last WindowSize outputs unmasked
	maskUpTo := len(toolOutputs) - cfg.WindowSize
	if maskUpTo < 0 {
		maskUpTo = 0
	}

	for idx, tb := range toolOutputs {
		shouldMask := false

		// Age-based masking: older than window
		if idx < maskUpTo {
			shouldMask = true
		}

		// Size-based masking: too large regardless of age
		if tb.block.Tokens > cfg.MaxOutputTokens {
			shouldMask = true
		}

		if shouldMask {
			oldTokens := tb.block.Tokens
			tb.block.Content = maskPlaceholder(tb.block)
			tb.block.Tokens = len(tb.block.Content) / 4
			result.BlocksMasked++
			result.TokensSaved += oldTokens - tb.block.Tokens
		}
	}

	result.TokensAfter = m.TotalTokens()
	return result
}

// CompactWithMasking applies the full compaction pipeline:
// 1. Observation masking (cheapest)
// 2. Standard compaction (gentle/moderate/aggressive)
// Returns the compaction level and masking results.
func (m *Manager) CompactWithMasking(maskCfg MaskConfig) (string, MaskResult) {
	// Step 1: Mask old observations
	maskResult := m.MaskObservations(maskCfg)

	// Step 2: If still over budget, apply standard compaction
	level := "none"
	if m.ShouldCompact() {
		level = m.Compact()
	}

	return level, maskResult
}

// maskPlaceholder generates a compact placeholder for a masked tool output.
func maskPlaceholder(b *ContextBlock) string {
	lines := strings.Count(b.Content, "\n") + 1

	// Extract a brief summary from the content
	summary := extractBrief(b.Content)

	return fmt.Sprintf("[%s: %s (%d lines, ~%d tokens — masked)]",
		b.Label, summary, lines, b.Tokens)
}

// extractBrief pulls the first meaningful line from content as a summary.
func extractBrief(content string) string {
	for _, line := range strings.SplitN(content, "\n", 10) {
		line = strings.TrimSpace(line)
		if line == "" || line == "{" || line == "}" || line == "[" || line == "]" {
			continue
		}
		if len(line) > 80 {
			return line[:77] + "..."
		}
		return line
	}
	return "content"
}

// BlockStats returns a summary of block counts and tokens by label.
func (m *Manager) BlockStats() map[string]int {
	stats := make(map[string]int)
	for _, b := range m.blocks {
		stats[b.Label] += b.Tokens
	}
	return stats
}
