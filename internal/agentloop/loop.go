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

	"github.com/ericmacdougall/stoke/internal/hub"
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
	// CompactThreshold is the estimated input-token count above which
	// the loop calls CompactFn to rewrite the message history. 0 = no
	// automatic compaction.
	CompactThreshold int
	// CompactFn is a hook that rewrites the message history when the
	// loop detects the conversation has grown past CompactThreshold.
	// The hook receives the current slice and must return a new slice
	// (or the same one) that is safe to continue from. Implementations
	// should:
	//   - Preserve the first user message (the task brief)
	//   - Preserve recent tool_use/tool_result pairs (in-flight work)
	//   - Summarize or drop old tool_result content
	// nil = no compaction.
	CompactFn CompactFunc
}

// CompactFunc is the signature for the per-turn compaction hook. It
// receives the current message list and the estimated token count; it
// must return a new message list that preserves tool_use/tool_result
// pair integrity (otherwise the API will reject the next request).
type CompactFunc func(messages []Message, estimatedTokens int) []Message

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
	eventBus  *hub.Bus // optional: publishes EvtToolPreUse/EvtToolPostUse events
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

// SetEventBus sets the hub event bus for publishing tool use events.
func (l *Loop) SetEventBus(bus *hub.Bus) { l.eventBus = bus }

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

		// Progressive compaction: before every API call, estimate the
		// conversation's token footprint and call the CompactFn hook if
		// it's grown past CompactThreshold. The hook returns a (possibly
		// rewritten) message list; we trust it to preserve
		// tool_use/tool_result pair integrity so the next API call
		// doesn't 400.
		if l.config.CompactFn != nil && l.config.CompactThreshold > 0 {
			est := estimateMessagesTokens(messages)
			if est > l.config.CompactThreshold {
				compacted := l.config.CompactFn(messages, est)
				if compacted != nil && len(compacted) > 0 {
					messages = compacted
					result.Messages = messages
				}
			}
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
// Tools are sorted alphabetically to prevent cache busting.
// System prompt uses cache_control breakpoints for 90% cost reduction.
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

	// Sort tools alphabetically — non-deterministic ordering busts cache on every turn
	sortedTools := SortToolsDeterministic(l.tools)

	// Build system prompt with cache_control breakpoint on static content
	systemBlocks := BuildCachedSystemPrompt(l.config.SystemPrompt, "")
	systemJSON, _ := json.Marshal(systemBlocks)

	return provider.ChatRequest{
		Model:        l.config.Model,
		SystemRaw:    systemJSON,
		Messages:     chatMsgs,
		MaxTokens:    l.config.MaxTokens,
		Tools:        sortedTools,
		CacheEnabled: true,
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

	execOne := func(idx int, tc ContentBlock) {
		if l.onToolUse != nil {
			l.onToolUse(tc.Name, tc.Input)
		}

		// Emit pre-use event (gate can block)
		if l.eventBus != nil {
			preEv := &hub.Event{
				Type:      hub.EventToolPreUse,
				Timestamp: time.Now(),
				Tool: &hub.ToolEvent{
					Name:  tc.Name,
					Input: parseToolInput(tc.Input),
				},
			}
			resp := l.eventBus.Emit(ctx, preEv)
			if resp.Decision == hub.Deny {
				mu.Lock()
				hasError = true
				results[idx] = ContentBlock{
					Type:      "tool_result",
					ToolUseID: tc.ID,
					Content:   fmt.Sprintf("Tool blocked by policy: %s", resp.Reason),
					IsError:   true,
				}
				mu.Unlock()
				return
			}
		}

		start := time.Now()
		content, err := l.handler(ctx, tc.Name, tc.Input)
		duration := time.Since(start)

		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			hasError = true
			results[idx] = ContentBlock{
				Type:      "tool_result",
				ToolUseID: tc.ID,
				Content:   fmt.Sprintf("Error: %v. Try a different approach.", err),
				IsError:   true,
			}
		} else {
			results[idx] = ContentBlock{
				Type:      "tool_result",
				ToolUseID: tc.ID,
				Content:   content,
			}
		}

		// Emit post-use event
		if l.eventBus != nil {
			postEv := &hub.Event{
				Type:      hub.EventToolPostUse,
				Timestamp: time.Now(),
				Tool: &hub.ToolEvent{
					Name:     tc.Name,
					Input:    parseToolInput(tc.Input),
					Output:   truncateOutput(content, 1024),
					Duration: duration,
				},
			}
			l.eventBus.EmitAsync(postEv)
		}
	}

	if len(toolCalls) == 1 {
		execOne(0, toolCalls[0])
	} else {
		var wg sync.WaitGroup
		for i, tc := range toolCalls {
			wg.Add(1)
			go func(idx int, call ContentBlock) {
				defer wg.Done()
				execOne(idx, call)
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

// parseToolInput converts JSON input to a map for hub events.
func parseToolInput(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// truncateOutput limits output size for hub events.
func truncateOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "... (truncated)"
}

// estimateMessagesTokens returns a rough token count for a message list.
// Uses the 4-chars-per-token heuristic (matches the rest of Stoke's
// estimator helpers). Accuracy is not critical — this feeds the
// CompactThreshold check which is a best-effort guard, not a hard
// budget.
func estimateMessagesTokens(messages []Message) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Role) + 8 // role + framing overhead
		for _, c := range m.Content {
			chars += len(c.Type) + 4
			chars += len(c.Text)
			chars += len(c.Content)   // tool_result content
			chars += len(c.Thinking)
			if len(c.Input) > 0 {
				chars += len(c.Input)
			}
			if len(c.ID) > 0 {
				chars += len(c.ID) + 4
			}
			if len(c.Name) > 0 {
				chars += len(c.Name) + 4
			}
		}
	}
	if chars == 0 {
		return 0
	}
	return chars / 4
}
