// Package tokenest estimates token counts for prompts before sending to APIs.
// Inspired by Aider's token counting and claw-code's budget-aware prompt construction.
//
// Accurate token estimation prevents:
// - Context window overflow (expensive API errors)
// - Wasted retries on prompts that are too large
// - Budget overruns from unexpectedly large requests
//
// Uses a byte-ratio heuristic (~4 chars per token for English, ~3 for code)
// that's fast and accurate within ~10% without requiring tiktoken.
package tokenest

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Model token limits for common models.
var ModelLimits = map[string]int{
	"claude-opus-4":       200000,
	"claude-sonnet-4":     200000,
	"claude-haiku-3.5":    200000,
	"gpt-4o":              128000,
	"gpt-4-turbo":         128000,
	"o3-mini":             128000,
	"codex-mini":          200000,
}

// ContentType affects the token ratio estimate.
type ContentType int

const (
	ContentEnglish ContentType = iota // ~4 chars/token
	ContentCode                       // ~3.3 chars/token
	ContentMixed                      // ~3.6 chars/token
	ContentJSON                       // ~3 chars/token
)

// Estimate returns the estimated token count for a string.
func Estimate(text string, ct ContentType) int {
	if text == "" {
		return 0
	}

	ratio := ratioFor(ct)
	byteLen := len(text)
	est := float64(byteLen) / ratio

	// Adjust for non-ASCII: multi-byte chars often map to more tokens
	nonASCII := countNonASCII(text)
	if nonASCII > 0 {
		est += float64(nonASCII) * 0.5
	}

	// Minimum 1 token for non-empty strings
	if est < 1 {
		return 1
	}
	return int(est + 0.5) // round
}

// EstimateMessages estimates tokens for a conversation (messages array).
// Includes per-message overhead (~4 tokens for role/structure).
func EstimateMessages(messages []Message) int {
	total := 3 // base overhead for conversation structure
	for _, msg := range messages {
		total += 4 // per-message overhead (role, separators)
		total += Estimate(msg.Content, DetectContentType(msg.Content))
	}
	return total
}

// Message is a minimal chat message for estimation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Budget tracks token usage against a limit.
type Budget struct {
	Limit    int `json:"limit"`
	Used     int `json:"used"`
	Reserved int `json:"reserved"` // tokens reserved for response
}

// NewBudget creates a budget for a model.
func NewBudget(model string, reserveForResponse int) Budget {
	limit, ok := ModelLimits[model]
	if !ok {
		limit = 128000 // safe default
	}
	return Budget{
		Limit:    limit,
		Reserved: reserveForResponse,
	}
}

// Available returns how many tokens can still be used for input.
func (b *Budget) Available() int {
	avail := b.Limit - b.Used - b.Reserved
	if avail < 0 {
		return 0
	}
	return avail
}

// Add records token usage. Returns false if budget would be exceeded.
func (b *Budget) Add(tokens int) bool {
	if b.Used+tokens+b.Reserved > b.Limit {
		return false
	}
	b.Used += tokens
	return true
}

// WouldFit returns true if the given text fits in remaining budget.
func (b *Budget) WouldFit(text string) bool {
	est := Estimate(text, DetectContentType(text))
	return b.Used+est+b.Reserved <= b.Limit
}

// Utilization returns the fraction of the budget used (0.0-1.0).
func (b *Budget) Utilization() float64 {
	if b.Limit == 0 {
		return 0
	}
	return float64(b.Used) / float64(b.Limit)
}

// TruncateToFit truncates text to fit within available budget.
// Returns the truncated text and whether truncation occurred.
func TruncateToFit(text string, availableTokens int, ct ContentType) (string, bool) {
	est := Estimate(text, ct)
	if est <= availableTokens {
		return text, false
	}

	ratio := ratioFor(ct)
	targetBytes := int(float64(availableTokens) * ratio * 0.95) // 5% safety margin
	if targetBytes >= len(text) {
		return text, false
	}
	if targetBytes <= 0 {
		return "", true
	}

	// Truncate at a clean boundary (newline or space)
	truncated := text[:targetBytes]
	if idx := strings.LastIndex(truncated, "\n"); idx > targetBytes/2 {
		truncated = truncated[:idx]
	} else if idx := strings.LastIndex(truncated, " "); idx > targetBytes/2 {
		truncated = truncated[:idx]
	}

	return truncated + "\n... [truncated]", true
}

// DetectContentType heuristically determines the content type.
func DetectContentType(text string) ContentType {
	if len(text) == 0 {
		return ContentEnglish
	}

	// Sample first 1000 chars
	sample := text
	if len(sample) > 1000 {
		sample = sample[:1000]
	}

	codeIndicators := 0
	jsonIndicators := 0
	lines := strings.Split(sample, "\n")

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Code indicators
		if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "def ") ||
			strings.HasPrefix(trimmed, "class ") || strings.HasPrefix(trimmed, "import ") ||
			strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") ||
			strings.HasSuffix(trimmed, "{") || strings.HasSuffix(trimmed, ";") ||
			strings.Contains(trimmed, ":=") || strings.Contains(trimmed, "->") ||
			strings.Contains(trimmed, "=>") {
			codeIndicators++
		}
		// JSON indicators
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") ||
			strings.Contains(trimmed, "\":") {
			jsonIndicators++
		}
	}

	totalLines := len(lines)
	if totalLines == 0 {
		return ContentEnglish
	}

	codeRatio := float64(codeIndicators) / float64(totalLines)
	jsonRatio := float64(jsonIndicators) / float64(totalLines)

	if jsonRatio > 0.5 {
		return ContentJSON
	}
	if codeRatio > 0.4 {
		return ContentCode
	}
	if codeRatio > 0.15 {
		return ContentMixed
	}
	return ContentEnglish
}

func ratioFor(ct ContentType) float64 {
	switch ct {
	case ContentEnglish:
		return 4.0
	case ContentCode:
		return 3.3
	case ContentMixed:
		return 3.6
	case ContentJSON:
		return 3.0
	default:
		return 3.6
	}
}

func countNonASCII(s string) int {
	count := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r > unicode.MaxASCII {
			count++
		}
		i += size
	}
	return count
}
