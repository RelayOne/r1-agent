package harness

import (
	"context"
	"encoding/json"
)

// Provider is the model backend seam the stance runner drives. It is defined
// here — in the consuming package — rather than in a satellite package, so
// the seam cannot go dormant separately from its only consumer again (the
// old internal/harness/models package was deleted as unimported in 2111ee7c;
// this is its consumer-side reintroduction per specs/harness-activation.md).
type Provider interface {
	// Name returns the provider's identifier (e.g. "anthropic", "mock").
	Name() string
	// Chat sends a chat completion request and returns the response.
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// ChatRequest is a model invocation request issued by the stance runner.
type ChatRequest struct {
	Model        string    `json:"model"`
	SystemPrompt string    `json:"system_prompt"`
	Messages     []Message `json:"messages"`
	Tools        []ToolDef `json:"tools,omitempty"`
}

// Message is a single turn in a stance conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatResponse is the model's reply for one turn.
type ChatResponse struct {
	Content   string     `json:"content"`
	TokensIn  int64      `json:"tokens_in"`
	TokensOut int64      `json:"tokens_out"`
	CostUSD   float64    `json:"cost_usd"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall represents the model requesting a tool invocation. Name must
// match an internal/harness/tools protocol value to pass authorization.
type ToolCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ToolDef describes a tool the model can invoke.
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
