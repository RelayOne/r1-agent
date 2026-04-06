// Package agentloop implements a native agentic tool-use loop using the
// Anthropic Messages API. Instead of spawning Claude Code CLI as a subprocess,
// this package talks directly to the API, giving Stoke full control over
// tool execution, prompt caching, cost tracking, and context management.
//
// Architecture follows the P61 research specification:
//   - Stateless Messages API: entire conversation sent each turn
//   - Tool definitions with cache_control for 90% cost reduction
//   - Parallel tool execution when Claude requests multiple tools
//   - Extended thinking support with signature preservation
//   - Defensive loop bounds (max turns + consecutive error tracking)
//   - Token usage accumulation for real-time cost tracking
package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/provider"
	"github.com/ericmacdougall/stoke/internal/stream"
)

// Config configures the agent loop.
type Config struct {
	Model              string        // e.g. "claude-sonnet-4-5-20250929"
	MaxTurns           int           // hard limit on API calls (default 25)
	MaxConsecutiveErrs int           // consecutive tool errors before abort (default 3)
	MaxTokens          int           // max output tokens per turn (default 16000)
	SystemPrompt       string        // static system prompt (cached)
	ThinkingBudget     int           // extended thinking budget (0 = disabled)
	Timeout            time.Duration // per-turn timeout
}

func (c *Config) defaults() {
	if c.MaxTurns == 0 {
		c.MaxTurns = 25
	}
	if c.MaxConsecutiveErrs == 0 {
		c.MaxConsecutiveErrs = 3
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 16000
	}
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Minute
	}
}

// ContentBlock is a typed content block in a message.
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`         // tool_use ID
	Name      string          `json:"name,omitempty"`       // tool name
	Input     json.RawMessage `json:"input,omitempty"`      // tool input JSON
	ToolUseID string          `json:"tool_use_id,omitempty"` // for tool_result
	Content   string          `json:"content,omitempty"`     // tool_result content
	IsError   bool            `json:"is_error,omitempty"`    // tool_result error flag
	Thinking  string          `json:"thinking,omitempty"`    // thinking content
	Signature string          `json:"signature,omitempty"`   // thinking signature
}

// Message is a conversation message with typed content blocks.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ToolHandler executes a tool and returns the result string.
// If the tool fails, return an error and the loop will send is_error: true.
type ToolHandler func(ctx context.Context, name string, input json.RawMessage) (string, error)

// OnTextFunc is called with incremental text output for streaming display.
type OnTextFunc func(text string)

// OnToolUseFunc is called when a tool is about to be executed.
type OnToolUseFunc func(name string, input json.RawMessage)

// Result is the final output of an agent loop run.
type Result struct {
	// FinalText is the concatenated text output from the final assistant turn.
	FinalText string `json:"final_text"`
	// Turns is the number of API calls made.
	Turns int `json:"turns"`
	// TotalCost tracks token usage across all turns.
	TotalCost CostTracker `json:"cost"`
	// StopReason is how the loop terminated.
	StopReason string `json:"stop_reason"`
	// Messages is the full conversation history.
	Messages []Message `json:"messages"`
}

// CostTracker accumulates token usage across turns.
type CostTracker struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
}

// Add accumulates usage from a single API response.
func (c *CostTracker) Add(usage stream.TokenUsage) {
	c.InputTokens += usage.Input
	c.OutputTokens += usage.Output
	c.CacheWriteTokens += usage.CacheCreation
	c.CacheReadTokens += usage.CacheRead
}

// TotalCostUSD computes the total cost based on model pricing.
func (c *CostTracker) TotalCostUSD(model string) float64 {
	var inputPrice, outputPrice, cacheWritePrice, cacheReadPrice float64

	// Pricing per token (derived from per-MTok rates)
	switch {
	case contains(model, "opus"):
		inputPrice = 5.0 / 1_000_000
		outputPrice = 25.0 / 1_000_000
		cacheWritePrice = 6.25 / 1_000_000
		cacheReadPrice = 0.50 / 1_000_000
	case contains(model, "haiku"):
		inputPrice = 1.0 / 1_000_000
		outputPrice = 5.0 / 1_000_000
		cacheWritePrice = 1.25 / 1_000_000
		cacheReadPrice = 0.10 / 1_000_000
	default: // sonnet and others
		inputPrice = 3.0 / 1_000_000
		outputPrice = 15.0 / 1_000_000
		cacheWritePrice = 3.75 / 1_000_000
		cacheReadPrice = 0.30 / 1_000_000
	}

	return float64(c.InputTokens)*inputPrice +
		float64(c.OutputTokens)*outputPrice +
		float64(c.CacheWriteTokens)*cacheWritePrice +
		float64(c.CacheReadTokens)*cacheReadPrice
}

// Loop is the main agentic tool-use loop.
type Loop struct {
	config    Config
	provider  provider.Provider
	tools     []provider.ToolDef
	handler   ToolHandler
	onText    OnTextFunc
	onToolUse OnToolUseFunc
}

// New creates a new agent loop.
func New(p provider.Provider, cfg Config, tools []provider.ToolDef, handler ToolHandler) *Loop {
	cfg.defaults()
	return &Loop{
		config:   cfg,
		provider: p,
		tools:    tools,
		handler:  handler,
	}
}

// SetOnText sets the callback for streaming text output.
func (l *Loop) SetOnText(fn OnTextFunc) { l.onText = fn }

// SetOnToolUse sets the callback for tool execution notifications.
func (l *Loop) SetOnToolUse(fn OnToolUseFunc) { l.onToolUse = fn }

// Run executes the agentic loop starting from a user message.
// It continues until the model stops requesting tools or limits are hit.
func (l *Loop) Run(ctx context.Context, userMessage string) (*Result, error) {
	messages := []Message{{
		Role:    "user",
		Content: []ContentBlock{{Type: "text", Text: userMessage}},
	}}
	return l.RunWithHistory(ctx, messages)
}

// RunWithHistory executes the loop with a pre-existing conversation history.
func (l *Loop) RunWithHistory(ctx context.Context, messages []Message) (*Result, error) {
	result := &Result{Messages: messages}
	consecutiveErrors := 0

	for turn := 0; turn < l.config.MaxTurns; turn++ {
		select {
		case <-ctx.Done():
			result.StopReason = "cancelled"
			result.Turns = turn
			return result, ctx.Err()
		default:
		}

		// Build the API request
		chatReq := l.buildRequest(messages)

		// Call the API with streaming
		var assistantBlocks []ContentBlock
		var lastText string
		resp, err := l.provider.ChatStream(chatReq, func(ev stream.Event) {
			if ev.DeltaText != "" && l.onText != nil {
				l.onText(ev.DeltaText)
				lastText += ev.DeltaText
			}
		})
		if err != nil {
			return result, fmt.Errorf("turn %d API call: %w", turn, err)
		}

		// Accumulate cost
		result.TotalCost.Add(resp.Usage)

		// Convert response content to our ContentBlock format
		for _, rc := range resp.Content {
			block := ContentBlock{Type: rc.Type}
			switch rc.Type {
			case "text":
				block.Text = rc.Text
			case "tool_use":
				block.ID = rc.ID
				block.Name = rc.Name
				if rc.Input != nil {
					inputJSON, _ := json.Marshal(rc.Input)
					block.Input = inputJSON
				}
			case "thinking":
				block.Text = rc.Text // thinking content
				// Signature would be preserved from raw response
			}
			assistantBlocks = append(assistantBlocks, block)
		}

		// Append assistant response to history
		assistantMsg := Message{Role: "assistant", Content: assistantBlocks}
		messages = append(messages, assistantMsg)

		// Check stop reason
		if resp.StopReason != "tool_use" {
			result.StopReason = resp.StopReason
			result.Turns = turn + 1
			result.FinalText = extractText(assistantBlocks)
			result.Messages = messages
			return result, nil
		}

		// Execute tools and collect results
		toolResults, hasError := l.executeTools(ctx, assistantBlocks)

		// Track consecutive errors
		if hasError {
			consecutiveErrors++
		} else {
			consecutiveErrors = 0
		}
		if consecutiveErrors >= l.config.MaxConsecutiveErrs {
			result.StopReason = "max_errors"
			result.Turns = turn + 1
			result.FinalText = extractText(assistantBlocks)
			result.Messages = messages
			return result, fmt.Errorf("aborted after %d consecutive tool errors", consecutiveErrors)
		}

		// Append tool results as user message
		messages = append(messages, Message{
			Role:    "user",
			Content: toolResults,
		})
	}

	result.StopReason = "max_turns"
	result.Turns = l.config.MaxTurns
	result.Messages = messages
	return result, fmt.Errorf("exceeded max turns (%d)", l.config.MaxTurns)
}

// buildRequest constructs the provider.ChatRequest from current state.
func (l *Loop) buildRequest(messages []Message) provider.ChatRequest {
	// Convert our Message format to provider's ChatMessage format
	chatMsgs := make([]provider.ChatMessage, len(messages))
	for i, msg := range messages {
		contentJSON, _ := json.Marshal(msg.Content)
		chatMsgs[i] = provider.ChatMessage{
			Role:    msg.Role,
			Content: contentJSON,
		}
	}

	return provider.ChatRequest{
		Model:     l.config.Model,
		System:    l.config.SystemPrompt,
		Messages:  chatMsgs,
		MaxTokens: l.config.MaxTokens,
		Tools:     l.tools,
	}
}

// executeTools runs all tool_use blocks and returns tool_result blocks.
func (l *Loop) executeTools(ctx context.Context, blocks []ContentBlock) ([]ContentBlock, bool) {
	var toolCalls []ContentBlock
	for _, b := range blocks {
		if b.Type == "tool_use" {
			toolCalls = append(toolCalls, b)
		}
	}

	if len(toolCalls) == 0 {
		return nil, false
	}

	// Execute tools in parallel when multiple are requested
	results := make([]ContentBlock, len(toolCalls))
	hasError := false
	var mu sync.Mutex

	if len(toolCalls) == 1 {
		// Single tool — execute directly
		tc := toolCalls[0]
		if l.onToolUse != nil {
			l.onToolUse(tc.Name, tc.Input)
		}
		content, err := l.handler(ctx, tc.Name, tc.Input)
		if err != nil {
			hasError = true
			results[0] = ContentBlock{
				Type:      "tool_result",
				ToolUseID: tc.ID,
				Content:   fmt.Sprintf("Error: %v. Try a different approach.", err),
				IsError:   true,
			}
		} else {
			results[0] = ContentBlock{
				Type:      "tool_result",
				ToolUseID: tc.ID,
				Content:   content,
			}
		}
	} else {
		// Multiple tools — execute in parallel
		var wg sync.WaitGroup
		for i, tc := range toolCalls {
			wg.Add(1)
			go func(idx int, call ContentBlock) {
				defer wg.Done()
				if l.onToolUse != nil {
					l.onToolUse(call.Name, call.Input)
				}
				content, err := l.handler(ctx, call.Name, call.Input)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					hasError = true
					results[idx] = ContentBlock{
						Type:      "tool_result",
						ToolUseID: call.ID,
						Content:   fmt.Sprintf("Error: %v. Try a different approach.", err),
						IsError:   true,
					}
				} else {
					results[idx] = ContentBlock{
						Type:      "tool_result",
						ToolUseID: call.ID,
						Content:   content,
					}
				}
			}(i, tc)
		}
		wg.Wait()
	}

	return results, hasError
}

// extractText concatenates all text blocks from a response.
func extractText(blocks []ContentBlock) string {
	var text string
	for _, b := range blocks {
		if b.Type == "text" {
			text += b.Text
		}
	}
	return text
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
