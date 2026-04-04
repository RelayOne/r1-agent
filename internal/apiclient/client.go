// Package apiclient implements a multi-provider SSE streaming API client.
// Inspired by claw-code's direct API access and Aider's multi-provider support:
//
// Instead of spawning CLI subprocesses (claude, codex), this client talks
// directly to provider APIs via HTTP with Server-Sent Events streaming:
// - Lower latency (no process startup overhead)
// - Better error handling (structured API errors vs stderr parsing)
// - Token-level streaming (see output as it generates)
// - Multi-provider with unified interface (Anthropic, OpenAI-compat, OpenRouter)
//
// This is the foundation for moving beyond CLI wrappers to native API integration.
package apiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Provider identifies an API provider.
type Provider string

const (
	ProviderAnthropic  Provider = "anthropic"
	ProviderOpenAI     Provider = "openai"
	ProviderOpenRouter Provider = "openrouter"
)

// Config holds provider-specific configuration.
type Config struct {
	Provider    Provider `json:"provider"`
	APIKey      string   `json:"-"` // never serialize
	BaseURL     string   `json:"base_url"`
	Model       string   `json:"model"`
	MaxTokens   int      `json:"max_tokens"`
	Temperature float64  `json:"temperature"`
	Timeout     time.Duration `json:"timeout"`
}

// DefaultConfigs for each provider.
var DefaultConfigs = map[Provider]Config{
	ProviderAnthropic: {
		Provider: ProviderAnthropic,
		BaseURL:  "https://api.anthropic.com",
		Model:    "claude-sonnet-4-20250514",
		MaxTokens: 16000,
		Timeout:   5 * time.Minute,
	},
	ProviderOpenAI: {
		Provider: ProviderOpenAI,
		BaseURL:  "https://api.openai.com",
		Model:    "gpt-4o",
		MaxTokens: 16000,
		Timeout:   5 * time.Minute,
	},
	ProviderOpenRouter: {
		Provider:  ProviderOpenRouter,
		BaseURL:   "https://openrouter.ai",
		Model:     "anthropic/claude-sonnet-4",
		MaxTokens: 16000,
		Timeout:   5 * time.Minute,
	},
}

// Message is a chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is a completion request.
type Request struct {
	Messages    []Message      `json:"messages"`
	System      string         `json:"system,omitempty"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
	Temperature *float64       `json:"temperature,omitempty"`
	Stream      bool           `json:"stream"`
	Tools       []ToolDef      `json:"tools,omitempty"`
	StopSeqs    []string       `json:"stop_sequences,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// ToolDef defines a tool available to the model.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// Response is a non-streaming completion response.
type Response struct {
	Content    string         `json:"content"`
	StopReason string         `json:"stop_reason"`
	Model      string         `json:"model"`
	Usage      Usage          `json:"usage"`
	ToolCalls  []ToolCall     `json:"tool_calls,omitempty"`
}

// ToolCall is a tool invocation from the model.
type ToolCall struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

// Usage tracks token counts.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CacheRead    int `json:"cache_read_input_tokens,omitempty"`
	CacheWrite   int `json:"cache_creation_input_tokens,omitempty"`
}

// StreamEvent is a single SSE event during streaming.
type StreamEvent struct {
	Type  string `json:"type"` // "text", "tool_use", "stop", "error", "usage"
	Text  string `json:"text,omitempty"`
	Tool  *ToolCall `json:"tool,omitempty"`
	Usage *Usage    `json:"usage,omitempty"`
	Error string `json:"error,omitempty"`
}

// StreamHandler receives streaming events.
type StreamHandler func(event StreamEvent)

// Client is a multi-provider API client.
type Client struct {
	config Config
	http   *http.Client
}

// NewClient creates a client for the given provider.
func NewClient(cfg Config) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}
	return &Client{
		config: cfg,
		http: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// Complete sends a non-streaming request and returns the full response.
func (c *Client) Complete(ctx context.Context, req Request) (*Response, error) {
	req.Stream = false
	body, err := c.buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	httpReq, err := c.buildHTTPRequest(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("build http request: %w", err)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Message:    string(respBody),
			Provider:   c.config.Provider,
		}
	}

	return c.parseResponse(resp.Body)
}

// Stream sends a streaming request and calls handler for each event.
func (c *Client) Stream(ctx context.Context, req Request, handler StreamHandler) (*Usage, error) {
	req.Stream = true
	body, err := c.buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	httpReq, err := c.buildHTTPRequest(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("build http request: %w", err)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Message:    string(respBody),
			Provider:   c.config.Provider,
		}
	}

	return c.parseSSE(resp.Body, handler)
}

// Provider returns the client's provider.
func (c *Client) Provider() Provider {
	return c.config.Provider
}

// Model returns the configured model.
func (c *Client) Model() string {
	return c.config.Model
}

// APIError represents a provider API error.
type APIError struct {
	StatusCode int      `json:"status_code"`
	Message    string   `json:"message"`
	Provider   Provider `json:"provider"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s API error %d: %s", e.Provider, e.StatusCode, e.Message)
}

// IsRateLimit returns true if this is a rate limit error.
func (e *APIError) IsRateLimit() bool {
	return e.StatusCode == 429
}

// IsAuth returns true if this is an authentication error.
func (e *APIError) IsAuth() bool {
	return e.StatusCode == 401 || e.StatusCode == 403
}

// IsRetryable returns true if the request could succeed on retry.
func (e *APIError) IsRetryable() bool {
	return e.StatusCode == 429 || e.StatusCode == 500 || e.StatusCode == 502 || e.StatusCode == 503 || e.StatusCode == 504
}

// --- internals ---

func (c *Client) buildRequestBody(req Request) ([]byte, error) {
	switch c.config.Provider {
	case ProviderAnthropic:
		return c.buildAnthropicBody(req)
	case ProviderOpenAI, ProviderOpenRouter:
		return c.buildOpenAIBody(req)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", c.config.Provider)
	}
}

func (c *Client) buildAnthropicBody(req Request) ([]byte, error) {
	body := map[string]any{
		"model":      c.config.Model,
		"messages":   req.Messages,
		"max_tokens": firstNonZero(req.MaxTokens, c.config.MaxTokens),
		"stream":     req.Stream,
	}
	if req.System != "" {
		body["system"] = req.System
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	if len(req.StopSeqs) > 0 {
		body["stop_sequences"] = req.StopSeqs
	}
	return json.Marshal(body)
}

func (c *Client) buildOpenAIBody(req Request) ([]byte, error) {
	messages := make([]map[string]any, 0, len(req.Messages)+1)
	if req.System != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		messages = append(messages, map[string]any{"role": m.Role, "content": m.Content})
	}

	body := map[string]any{
		"model":      c.config.Model,
		"messages":   messages,
		"max_tokens": firstNonZero(req.MaxTokens, c.config.MaxTokens),
		"stream":     req.Stream,
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	return json.Marshal(body)
}

func (c *Client) buildHTTPRequest(ctx context.Context, body []byte) (*http.Request, error) {
	var url string
	switch c.config.Provider {
	case ProviderAnthropic:
		url = c.config.BaseURL + "/v1/messages"
	case ProviderOpenAI:
		url = c.config.BaseURL + "/v1/chat/completions"
	case ProviderOpenRouter:
		url = c.config.BaseURL + "/api/v1/chat/completions"
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	switch c.config.Provider {
	case ProviderAnthropic:
		req.Header.Set("x-api-key", c.config.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	case ProviderOpenAI, ProviderOpenRouter:
		req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}

	return req, nil
}

func (c *Client) parseResponse(body io.Reader) (*Response, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	switch c.config.Provider {
	case ProviderAnthropic:
		return parseAnthropicResponse(data)
	case ProviderOpenAI, ProviderOpenRouter:
		return parseOpenAIResponse(data)
	default:
		return nil, fmt.Errorf("unsupported provider")
	}
}

func parseAnthropicResponse(data []byte) (*Response, error) {
	var raw struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Model      string `json:"model"`
		Usage      Usage  `json:"usage"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse anthropic response: %w", err)
	}

	var text strings.Builder
	for _, c := range raw.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}

	return &Response{
		Content:    text.String(),
		StopReason: raw.StopReason,
		Model:      raw.Model,
		Usage:      raw.Usage,
	}, nil
}

func parseOpenAIResponse(data []byte) (*Response, error) {
	var raw struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Model string `json:"model"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse openai response: %w", err)
	}

	resp := &Response{
		Model: raw.Model,
		Usage: Usage{
			InputTokens:  raw.Usage.PromptTokens,
			OutputTokens: raw.Usage.CompletionTokens,
		},
	}
	if len(raw.Choices) > 0 {
		resp.Content = raw.Choices[0].Message.Content
		resp.StopReason = raw.Choices[0].FinishReason
	}
	return resp, nil
}

func (c *Client) parseSSE(body io.Reader, handler StreamHandler) (*Usage, error) {
	scanner := bufio.NewScanner(body)
	var finalUsage Usage

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		event, usage := c.parseSSEData(data)
		if event != nil {
			handler(*event)
		}
		if usage != nil {
			finalUsage = *usage
		}
	}

	return &finalUsage, scanner.Err()
}

func (c *Client) parseSSEData(data string) (*StreamEvent, *Usage) {
	switch c.config.Provider {
	case ProviderAnthropic:
		return parseAnthropicSSE(data)
	case ProviderOpenAI, ProviderOpenRouter:
		return parseOpenAISSE(data)
	}
	return nil, nil
}

func parseAnthropicSSE(data string) (*StreamEvent, *Usage) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, nil
	}

	eventType, _ := raw["type"].(string)
	switch eventType {
	case "content_block_delta":
		delta, ok := raw["delta"].(map[string]any)
		if !ok {
			return nil, nil
		}
		if text, ok := delta["text"].(string); ok {
			return &StreamEvent{Type: "text", Text: text}, nil
		}
	case "message_delta":
		var event *StreamEvent
		var u *Usage
		if delta, ok := raw["delta"].(map[string]any); ok {
			if stop, ok := delta["stop_reason"].(string); ok {
				event = &StreamEvent{Type: "stop", Text: stop}
			}
		}
		if usage, ok := raw["usage"].(map[string]any); ok {
			u = &Usage{}
			if v, ok := usage["output_tokens"].(float64); ok {
				u.OutputTokens = int(v)
			}
		}
		if event != nil || u != nil {
			return event, u
		}
	case "message_start":
		if msg, ok := raw["message"].(map[string]any); ok {
			if usage, ok := msg["usage"].(map[string]any); ok {
				u := &Usage{}
				if v, ok := usage["input_tokens"].(float64); ok {
					u.InputTokens = int(v)
				}
				return nil, u
			}
		}
	case "error":
		if errData, ok := raw["error"].(map[string]any); ok {
			msg, _ := errData["message"].(string)
			return &StreamEvent{Type: "error", Error: msg}, nil
		}
	}
	return nil, nil
}

func parseOpenAISSE(data string) (*StreamEvent, *Usage) {
	var raw struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, nil
	}

	if len(raw.Choices) > 0 {
		choice := raw.Choices[0]
		if choice.Delta.Content != "" {
			return &StreamEvent{Type: "text", Text: choice.Delta.Content}, nil
		}
		if choice.FinishReason != nil {
			return &StreamEvent{Type: "stop", Text: *choice.FinishReason}, nil
		}
	}
	return nil, nil
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}
