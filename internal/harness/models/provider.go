// Package models defines the model provider interface used by the harness
// to communicate with LLMs.
package models

import (
	"context"
	"encoding/json"
)

// Provider is the interface that model backends must satisfy.
type Provider interface {
	// Name returns the provider's identifier (e.g. "anthropic", "mock").
	Name() string
	// Chat sends a chat completion request and returns the response.
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// ChatRequest is a model invocation request.
type ChatRequest struct {
	Model        string    `json:"model"`
	SystemPrompt string    `json:"system_prompt"`
	Messages     []Message `json:"messages"`
	Tools        []ToolDef `json:"tools,omitempty"`
}

// Message is a single turn in a conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatResponse is the model's reply.
type ChatResponse struct {
	Content   string     `json:"content"`
	TokensIn  int64      `json:"tokens_in"`
	TokensOut int64      `json:"tokens_out"`
	CostUSD   float64    `json:"cost_usd"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall represents the model requesting a tool invocation.
type ToolCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ToolDef describes a tool the model can invoke.
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
