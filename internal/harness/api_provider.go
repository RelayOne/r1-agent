package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/costtrack"
	"github.com/RelayOne/r1/internal/provider"
)

// defaultMaxTokens bounds a single stance turn when the caller does not
// override it. Matches the order of magnitude the direct API clients use.
const defaultMaxTokens = 8192

// APIProvider adapts an internal/provider direct API client (Anthropic,
// OpenAI-compat, ...) to the harness Provider seam. It is the first real
// Provider implementation; MockProvider remains the test double.
type APIProvider struct {
	inner     provider.Provider
	maxTokens int
}

// NewAPIProvider wraps an internal/provider client. maxTokens <= 0 uses
// defaultMaxTokens.
func NewAPIProvider(p provider.Provider, maxTokens int) *APIProvider {
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	return &APIProvider{inner: p, maxTokens: maxTokens}
}

// NewAPIProviderForModel resolves the direct API client for the given model
// name via provider.ResolveProvider and wraps it.
func NewAPIProviderForModel(model string) *APIProvider {
	return NewAPIProvider(provider.ResolveProvider(model), 0)
}

// Name implements Provider.
func (a *APIProvider) Name() string { return a.inner.Name() }

// Chat implements Provider. It converts the harness request to the wire
// client's shape, invokes the client, and normalizes the response: text
// blocks are concatenated, tool_use blocks become ToolCalls, and CostUSD is
// computed from usage via costtrack.ComputeCost (the wire clients report
// tokens, not dollars).
func (a *APIProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// The wire clients are synchronous and context-free; honor an
	// already-cancelled context before spending a network round trip.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	wireReq := provider.ChatRequest{
		Model:     req.Model,
		System:    req.SystemPrompt,
		MaxTokens: a.maxTokens,
	}
	for _, m := range req.Messages {
		content, err := json.Marshal(m.Content)
		if err != nil {
			return nil, fmt.Errorf("harness: api provider: marshal message content: %w", err)
		}
		wireReq.Messages = append(wireReq.Messages, provider.ChatMessage{
			Role:    m.Role,
			Content: content,
		})
	}
	for _, t := range req.Tools {
		wireReq.Tools = append(wireReq.Tools, provider.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			// Harness tool calls carry free-form args; advertise a
			// permissive object schema.
			InputSchema: json.RawMessage(`{"type":"object"}`),
		})
	}

	wireResp, err := a.inner.Chat(wireReq)
	if err != nil {
		return nil, fmt.Errorf("harness: api provider %s: %w", a.inner.Name(), err)
	}

	resp := &ChatResponse{
		TokensIn:  int64(wireResp.Usage.Input),
		TokensOut: int64(wireResp.Usage.Output),
		CostUSD: costtrack.ComputeCost(req.Model,
			wireResp.Usage.Input, wireResp.Usage.Output,
			wireResp.Usage.CacheRead, wireResp.Usage.CacheCreation),
	}

	var text strings.Builder
	for _, block := range wireResp.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			args, err := json.Marshal(block.Input)
			if err != nil {
				return nil, fmt.Errorf("harness: api provider: marshal tool_use input: %w", err)
			}
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				Name: block.Name,
				Args: args,
			})
		}
	}
	resp.Content = text.String()
	return resp, nil
}
