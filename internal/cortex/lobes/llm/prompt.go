// Package llm provides shared infrastructure for LLM-driven Lobes:
// cache-aligned prompt building, escalation policy, semaphore wrapping.
package llm

import (
	"encoding/json"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/provider"
)

// defaultMaxTokens is the per-request output cap when MaxTokens is unset.
// Lobes typically emit short structured responses, so 500 is a safe default
// that keeps latency and cost low.
const defaultMaxTokens = 500

// LobePromptBuilder constructs a cache-aligned ChatRequest for an LLM Lobe.
// The system prompt is wrapped with a 1-hour ephemeral cache_control breakpoint
// and tools are sorted alphabetically to keep the cache key stable across calls.
//
// Note: the cortex-concerns spec (item 1) names the type
// apiclient.MessagesRequest. In r1 the canonical wire types live in
// internal/provider — provider.ChatRequest is the equivalent. Build
// returns provider.ChatRequest so callers can pass the result straight
// to provider.Provider.Chat / ChatStream.
type LobePromptBuilder struct {
	Model        string
	SystemPrompt string
	Tools        []provider.ToolDef
	MaxTokens    int
}

// Build constructs the ChatRequest. userMessage is appended as the final
// user message; history is the running conversation prefix (typically the
// last 10 turns).
//
// Cache-key stability invariants (see agentloop/cache.go and spec gotcha #8):
//   - Tools are sorted alphabetically by Name.
//   - The system prompt is rendered as a single block with
//     cache_control: {type: "ephemeral", ttl: "1h"}.
//   - CacheEnabled=true so the provider adds cache_control to the last
//     tool definition and to the tail of the message list.
func (b LobePromptBuilder) Build(userMessage string, history []provider.ChatMessage) provider.ChatRequest {
	maxTok := b.MaxTokens
	if maxTok <= 0 {
		maxTok = defaultMaxTokens
	}

	// Sort tools alphabetically by Name to keep the cache key stable.
	// Re-uses agentloop.SortToolsDeterministic so the byte layout matches
	// what the main agent loop sends — a 1-byte drift busts the cache.
	tools := agentloop.SortToolsDeterministic(b.Tools)

	// Cache-aligned system prompt with 1-hour TTL.
	systemBlocks := agentloop.BuildCachedSystemPrompt1h(b.SystemPrompt, "")
	systemJSON, _ := json.Marshal(systemBlocks)

	// Append the user message to history.
	msgs := make([]provider.ChatMessage, 0, len(history)+1)
	msgs = append(msgs, history...)
	userJSON, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": userMessage},
	})
	msgs = append(msgs, provider.ChatMessage{Role: "user", Content: userJSON})

	return provider.ChatRequest{
		Model:        b.Model,
		SystemRaw:    systemJSON,
		Messages:     msgs,
		MaxTokens:    maxTok,
		Tools:        tools,
		CacheEnabled: true,
	}
}

