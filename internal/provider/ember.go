package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/RelayOne/r1/internal/env/ember"
	"github.com/RelayOne/r1/internal/stream"
)

// EmberProvider implements Provider using Ember's managed AI endpoint.
// It translates between Stoke's provider.ChatRequest format and Ember's
// OpenAI-compatible /v1/ai/chat endpoint.
type EmberProvider struct {
	client *ember.AIClient
	model  string // default model to use
}

// NewEmberProvider creates a provider backed by Ember's managed AI.
// apiURL defaults to EMBER_API_URL env var, token to EMBER_API_KEY.
func NewEmberProvider(apiURL, token, model string) *EmberProvider {
	if apiURL == "" {
		apiURL = os.Getenv("EMBER_API_URL")
	}
	if token == "" {
		token = os.Getenv("EMBER_API_KEY")
	}
	if model == "" {
		model = "anthropic/claude-sonnet-4"
	}
	return &EmberProvider{
		client: ember.NewAIClient(apiURL, token),
		model:  model,
	}
}

func (p *EmberProvider) Name() string { return "ember" }

// Chat sends a non-streaming completion request via Ember.
func (p *EmberProvider) Chat(req ChatRequest) (*ChatResponse, error) {
	messages := convertMessages(req)

	emberReq := ember.ChatRequest{
		Model:    p.model,
		Messages: messages,
	}

	resp, err := p.client.Chat(context.Background(), emberReq)
	if err != nil {
		return nil, fmt.Errorf("ember chat: %w", err)
	}

	return convertResponse(resp, p.model), nil
}

// ChatStream sends a streaming completion request via Ember.
func (p *EmberProvider) ChatStream(req ChatRequest, onEvent func(stream.Event)) (*ChatResponse, error) {
	messages := convertMessages(req)

	emberReq := ember.ChatRequest{
		Model:    p.model,
		Messages: messages,
	}

	var collected string
	_, err := p.client.ChatStream(context.Background(), emberReq, func(content string) {
		collected += content
		if onEvent != nil {
			onEvent(stream.Event{
				Type:      "assistant",
				DeltaText: content,
			})
		}
	})
	if err != nil {
		return nil, fmt.Errorf("ember chat stream: %w", err)
	}

	return &ChatResponse{
		Model: p.model,
		Content: []ResponseContent{{
			Type: "text",
			Text: collected,
		}},
		StopReason: "end_turn",
	}, nil
}

// convertMessages translates provider.ChatMessage to ember.ChatMessage.
func convertMessages(req ChatRequest) []ember.ChatMessage {
	messages := make([]ember.ChatMessage, 0, len(req.Messages)+1)

	// Add system message if present.
	if req.System != "" {
		messages = append(messages, ember.ChatMessage{
			Role:    "system",
			Content: req.System,
		})
	}

	for _, msg := range req.Messages {
		content := extractTextContent(msg.Content)
		messages = append(messages, ember.ChatMessage{
			Role:    msg.Role,
			Content: content,
		})
	}

	return messages
}

// extractTextContent converts json.RawMessage content to a plain text string.
// Handles both simple string content and structured content blocks.
func extractTextContent(raw json.RawMessage) string {
	// Try as a simple string first.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	// Try as an array of content blocks.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var text string
		for _, b := range blocks {
			if b.Type == "text" {
				text += b.Text
			}
		}
		return text
	}

	// Fallback: raw string.
	return string(raw)
}

// convertResponse translates ember.ChatResponse to provider.ChatResponse.
func convertResponse(resp *ember.ChatResponse, model string) *ChatResponse {
	if len(resp.Choices) == 0 {
		return &ChatResponse{Model: model}
	}

	choice := resp.Choices[0]
	return &ChatResponse{
		Model: model,
		Content: []ResponseContent{{
			Type: "text",
			Text: choice.Message.Content,
		}},
		StopReason: choice.FinishReason,
		Usage: stream.TokenUsage{
			Input:  resp.Usage.PromptTokens,
			Output: resp.Usage.CompletionTokens,
		},
	}
}
