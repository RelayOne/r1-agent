// Package provider implements direct AI model API clients for when Claude Code CLI
// is unavailable or undesirable. Inspired by claw-code-parity's multi-provider
// architecture (Anthropic, XAI, OpenAI-compatible endpoints).
// This gives Stoke a fallback path that doesn't require Claude Code CLI installation.
package provider

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/stream"
)

// Provider is a direct API client for model inference.
type Provider interface {
	Name() string
	Chat(req ChatRequest) (*ChatResponse, error)
	ChatStream(req ChatRequest, onEvent func(stream.Event)) (*ChatResponse, error)
}

// ChatRequest is a model-agnostic chat completion request.
type ChatRequest struct {
	Model       string            `json:"model"`
	System      string            `json:"system,omitempty"`
	Messages    []ChatMessage     `json:"messages"`
	MaxTokens   int               `json:"max_tokens"`
	Tools       []ToolDef         `json:"tools,omitempty"`
	Temperature *float64          `json:"temperature,omitempty"`
	Metadata    map[string]string `json:"-"` // not sent to API
}

// ChatMessage is a single message in a chat.
type ChatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ToolDef is a tool definition for the API.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ChatResponse is the response from a chat completion.
type ChatResponse struct {
	ID         string             `json:"id"`
	Model      string             `json:"model"`
	Content    []ResponseContent  `json:"content"`
	StopReason string             `json:"stop_reason"`
	Usage      stream.TokenUsage  `json:"usage"`
}

// ResponseContent is a content block in a response.
type ResponseContent struct {
	Type  string                 `json:"type"`
	Text  string                 `json:"text,omitempty"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
}

// AnthropicProvider communicates directly with the Anthropic Messages API.
type AnthropicProvider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	sseParser  *stream.SSEParser
	mu         sync.Mutex
}

// NewAnthropicProvider creates a direct Anthropic API client.
func NewAnthropicProvider(apiKey, baseURL string) *AnthropicProvider {
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if baseURL == "" {
		baseURL = os.Getenv("ANTHROPIC_BASE_URL")
	}
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	return &AnthropicProvider{
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 10 * time.Minute},
	}
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

// Chat sends a non-streaming chat completion request.
func (p *AnthropicProvider) Chat(req ChatRequest) (*ChatResponse, error) {
	body := p.buildRequestBody(req, false)
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", p.baseURL+"/v1/messages", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Anthropic API error %d: %s", resp.StatusCode, string(errBody))
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &chatResp, nil
}

// ChatStream sends a streaming chat completion request, calling onEvent for each event.
func (p *AnthropicProvider) ChatStream(req ChatRequest, onEvent func(stream.Event)) (*ChatResponse, error) {
	body := p.buildRequestBody(req, true)
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", p.baseURL+"/v1/messages", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	p.setHeaders(httpReq)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Anthropic API error %d: %s", resp.StatusCode, string(errBody))
	}

	// Parse SSE stream using our SSEParser
	parser := stream.NewSSEParser()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var result ChatResponse
	var fullText strings.Builder

	// Read line by line and feed to SSE parser
	var lineBuffer strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		lineBuffer.WriteString(line)
		lineBuffer.WriteString("\n")

		// Check for frame boundary (empty line)
		if line == "" {
			events, parseErr := parser.Push([]byte(lineBuffer.String()))
			lineBuffer.Reset()
			if parseErr != nil {
				continue
			}
			for _, ev := range events {
				if onEvent != nil {
					onEvent(ev)
				}
				// Accumulate into result
				if ev.DeltaText != "" {
					fullText.WriteString(ev.DeltaText)
				}
				if ev.Tokens.Input > 0 || ev.Tokens.Output > 0 {
					if ev.Tokens.Input > result.Usage.Input {
						result.Usage.Input = ev.Tokens.Input
					}
					if ev.Tokens.Output > result.Usage.Output {
						result.Usage.Output = ev.Tokens.Output
					}
					result.Usage.CacheRead = ev.Tokens.CacheRead
					result.Usage.CacheCreation = ev.Tokens.CacheCreation
				}
				if ev.StopReason != "" {
					result.StopReason = ev.StopReason
				}
				if len(ev.ToolUses) > 0 {
					for _, tu := range ev.ToolUses {
						result.Content = append(result.Content, ResponseContent{
							Type: "tool_use", ID: tu.ID, Name: tu.Name, Input: tu.Input,
						})
					}
				}
			}
		}
	}

	// Flush remaining
	if lineBuffer.Len() > 0 {
		events, _ := parser.Finish()
		for _, ev := range events {
			if onEvent != nil {
				onEvent(ev)
			}
		}
	}

	if text := fullText.String(); text != "" {
		result.Content = append([]ResponseContent{{Type: "text", Text: text}}, result.Content...)
	}

	return &result, nil
}

func (p *AnthropicProvider) buildRequestBody(req ChatRequest, streaming bool) map[string]interface{} {
	body := map[string]interface{}{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"messages":   req.Messages,
	}
	if req.System != "" {
		body["system"] = req.System
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if streaming {
		body["stream"] = true
	}
	return body
}

func (p *AnthropicProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
}

// OpenAICompatProvider communicates with OpenAI-compatible endpoints (OpenAI, OpenRouter, XAI, etc.).
type OpenAICompatProvider struct {
	name       string
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// NewOpenAICompatProvider creates an OpenAI-compatible API client.
func NewOpenAICompatProvider(name, apiKey, baseURL string) *OpenAICompatProvider {
	return &OpenAICompatProvider{
		name:       name,
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 10 * time.Minute},
	}
}

func (p *OpenAICompatProvider) Name() string { return p.name }

// Chat sends a non-streaming completion.
func (p *OpenAICompatProvider) Chat(req ChatRequest) (*ChatResponse, error) {
	body := map[string]interface{}{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"messages":   p.convertMessages(req),
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", p.baseURL+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s API error %d: %s", p.name, resp.StatusCode, string(errBody))
	}

	var openAIResp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		return nil, err
	}

	result := &ChatResponse{
		ID:    openAIResp.ID,
		Model: openAIResp.Model,
		Usage: stream.TokenUsage{
			Input:  openAIResp.Usage.PromptTokens,
			Output: openAIResp.Usage.CompletionTokens,
		},
	}
	if len(openAIResp.Choices) > 0 {
		result.Content = []ResponseContent{{Type: "text", Text: openAIResp.Choices[0].Message.Content}}
		result.StopReason = openAIResp.Choices[0].FinishReason
	}
	return result, nil
}

// ChatStream sends a streaming completion (SSE format).
func (p *OpenAICompatProvider) ChatStream(req ChatRequest, onEvent func(stream.Event)) (*ChatResponse, error) {
	body := map[string]interface{}{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"messages":   p.convertMessages(req),
		"stream":     true,
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", p.baseURL+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s API error %d: %s", p.name, resp.StatusCode, string(errBody))
	}

	var result ChatResponse
	var fullText strings.Builder

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}

		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				fullText.WriteString(c.Delta.Content)
				ev := stream.Event{
					Type:      "stream_event",
					DeltaType: "text_delta",
					DeltaText: c.Delta.Content,
				}
				if onEvent != nil {
					onEvent(ev)
				}
			}
			if c.FinishReason != nil {
				result.StopReason = *c.FinishReason
			}
		}
	}

	result.Content = []ResponseContent{{Type: "text", Text: fullText.String()}}
	return &result, nil
}

func (p *OpenAICompatProvider) convertMessages(req ChatRequest) []map[string]interface{} {
	var msgs []map[string]interface{}
	if req.System != "" {
		msgs = append(msgs, map[string]interface{}{
			"role":    "system",
			"content": req.System,
		})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, map[string]interface{}{
			"role":    m.Role,
			"content": m.Content,
		})
	}
	return msgs
}

// ResolveProvider creates the appropriate provider based on model name.
func ResolveProvider(modelName string) Provider {
	switch {
	case strings.HasPrefix(modelName, "claude"):
		return NewAnthropicProvider("", "")
	case strings.HasPrefix(modelName, "gpt") || strings.HasPrefix(modelName, "o1") || strings.HasPrefix(modelName, "o3"):
		return NewOpenAICompatProvider("openai", os.Getenv("OPENAI_API_KEY"), "https://api.openai.com")
	case strings.HasPrefix(modelName, "grok"):
		return NewOpenAICompatProvider("xai", os.Getenv("XAI_API_KEY"), "https://api.x.ai")
	case strings.Contains(modelName, "/"):
		// OpenRouter format: provider/model
		return NewOpenAICompatProvider("openrouter", os.Getenv("OPENROUTER_API_KEY"), "https://openrouter.ai/api")
	default:
		return NewAnthropicProvider("", "")
	}
}
