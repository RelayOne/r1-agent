// Package conversation provides multi-turn conversation state management.
// Inspired by claw-code-parity's ConversationRuntime. Maintains message history,
// handles tool use/result pairing, and supports session continuity across retries.
package conversation

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Role identifies the sender of a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

// ContentBlock is a single piece of content within a message.
type ContentBlock struct {
	Type      string                 `json:"type"`
	Text      string                 `json:"text,omitempty"`
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
	ToolUseID string                 `json:"tool_use_id,omitempty"`
	Content   string                 `json:"content,omitempty"`
	IsError   bool                   `json:"is_error,omitempty"`
}

// Message is a single turn in the conversation.
type Message struct {
	Role      Role           `json:"role"`
	Content   []ContentBlock `json:"content"`
	Timestamp time.Time      `json:"timestamp"`
	TokensIn  int            `json:"tokens_in,omitempty"`
	TokensOut int            `json:"tokens_out,omitempty"`
}

// TextMessage creates a simple text message.
func TextMessage(role Role, text string) Message {
	return Message{
		Role: role,
		Content: []ContentBlock{
			{Type: "text", Text: text},
		},
		Timestamp: time.Now(),
	}
}

// ToolResultMessage creates a tool result message.
func ToolResultMessage(toolUseID, content string, isError bool) Message {
	return Message{
		Role: RoleUser,
		Content: []ContentBlock{
			{Type: "tool_result", ToolUseID: toolUseID, Content: content, IsError: isError},
		},
		Timestamp: time.Now(),
	}
}

// Runtime manages multi-turn conversation state.
type Runtime struct {
	mu       sync.RWMutex
	messages []Message
	systemPrompt string
	maxTokens    int
	totalTokensIn  int
	totalTokensOut int
}

// NewRuntime creates a conversation runtime with a system prompt and token budget.
func NewRuntime(systemPrompt string, maxTokens int) *Runtime {
	return &Runtime{
		systemPrompt: systemPrompt,
		maxTokens:    maxTokens,
	}
}

// AddMessage appends a message to the conversation.
func (r *Runtime) AddMessage(msg Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, msg)
	r.totalTokensIn += msg.TokensIn
	r.totalTokensOut += msg.TokensOut
}

// Messages returns a copy of the conversation history.
func (r *Runtime) Messages() []Message {
	r.mu.RLock()
	defer r.mu.RUnlock()
	msgs := make([]Message, len(r.messages))
	copy(msgs, r.messages)
	return msgs
}

// SystemPrompt returns the system prompt.
func (r *Runtime) SystemPrompt() string {
	return r.systemPrompt
}

// TurnCount returns the number of turns in the conversation.
func (r *Runtime) TurnCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.messages)
}

// TotalTokens returns cumulative token usage.
func (r *Runtime) TotalTokens() (in, out int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.totalTokensIn, r.totalTokensOut
}

// EstimatedTokens returns the estimated token count of the full conversation.
func (r *Runtime) EstimatedTokens() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	total := len(r.systemPrompt) / 4
	for _, msg := range r.messages {
		for _, block := range msg.Content {
			total += len(block.Text) / 4
			total += len(block.Content) / 4
			if block.Input != nil {
				data, _ := json.Marshal(block.Input)
				total += len(data) / 4
			}
		}
	}
	return total
}

// PendingToolUses returns tool use blocks from the last assistant message
// that haven't been matched with a tool result yet.
func (r *Runtime) PendingToolUses() []ContentBlock {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.messages) == 0 {
		return nil
	}

	// Find tool use IDs in the last assistant message
	var toolUseIDs map[string]ContentBlock
	for i := len(r.messages) - 1; i >= 0; i-- {
		if r.messages[i].Role == RoleAssistant {
			toolUseIDs = make(map[string]ContentBlock)
			for _, block := range r.messages[i].Content {
				if block.Type == "tool_use" {
					toolUseIDs[block.ID] = block
				}
			}
			break
		}
	}

	if len(toolUseIDs) == 0 {
		return nil
	}

	// Remove any that have been answered
	for i := len(r.messages) - 1; i >= 0; i-- {
		if r.messages[i].Role == RoleUser {
			for _, block := range r.messages[i].Content {
				if block.Type == "tool_result" {
					delete(toolUseIDs, block.ToolUseID)
				}
			}
		}
	}

	pending := make([]ContentBlock, 0, len(toolUseIDs))
	for _, block := range toolUseIDs {
		pending = append(pending, block)
	}
	return pending
}

// Compact reduces conversation history by summarizing older turns.
// Keeps the system prompt, last N messages, and a summary of earlier messages.
func (r *Runtime) Compact(keepLast int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.messages) <= keepLast {
		return
	}

	// Summarize older messages
	older := r.messages[:len(r.messages)-keepLast]
	summary := fmt.Sprintf("[Compacted %d earlier turns. ", len(older))
	toolCount := 0
	textCount := 0
	for _, msg := range older {
		for _, block := range msg.Content {
			if block.Type == "tool_use" {
				toolCount++
			} else if block.Type == "text" {
				textCount++
			}
		}
	}
	summary += fmt.Sprintf("%d text blocks, %d tool calls.]", textCount, toolCount)

	compactedMsg := Message{
		Role:      RoleUser,
		Content:   []ContentBlock{{Type: "text", Text: summary}},
		Timestamp: time.Now(),
	}

	r.messages = append([]Message{compactedMsg}, r.messages[len(r.messages)-keepLast:]...)
}

// SaveTo persists the conversation to a JSON file.
func (r *Runtime) SaveTo(path string) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	data, err := json.MarshalIndent(struct {
		SystemPrompt string    `json:"system_prompt"`
		Messages     []Message `json:"messages"`
		TotalTokensIn  int     `json:"total_tokens_in"`
		TotalTokensOut int     `json:"total_tokens_out"`
	}{
		SystemPrompt: r.systemPrompt,
		Messages:     r.messages,
		TotalTokensIn:  r.totalTokensIn,
		TotalTokensOut: r.totalTokensOut,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// LoadFrom restores conversation state from a JSON file.
func (r *Runtime) LoadFrom(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var saved struct {
		SystemPrompt string    `json:"system_prompt"`
		Messages     []Message `json:"messages"`
		TotalTokensIn  int     `json:"total_tokens_in"`
		TotalTokensOut int     `json:"total_tokens_out"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.systemPrompt = saved.SystemPrompt
	r.messages = saved.Messages
	r.totalTokensIn = saved.TotalTokensIn
	r.totalTokensOut = saved.TotalTokensOut
	return nil
}
