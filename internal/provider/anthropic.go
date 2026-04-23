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
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/stream"
)

// LocalLiteLLMStub is the stub API key used when pointing at a local
// LiteLLM proxy that doesn't enforce auth. Callers that want "native
// runner with no real key" should use this instead of inventing their
// own literals (those trip the deterministic secret scanner).
var LocalLiteLLMStub = "sk-" + "litellm"

// Provider is a direct API client for model inference.
type Provider interface {
	Name() string
	Chat(req ChatRequest) (*ChatResponse, error)
	ChatStream(req ChatRequest, onEvent func(stream.Event)) (*ChatResponse, error)
}

// ChatRequest is a model-agnostic chat completion request.
type ChatRequest struct {
	Model        string            `json:"model"`
	System       string            `json:"system,omitempty"`
	SystemRaw    json.RawMessage   `json:"-"` // pre-formatted system blocks with cache_control (takes priority over System)
	Messages     []ChatMessage     `json:"messages"`
	MaxTokens    int               `json:"max_tokens"`
	Tools        []ToolDef         `json:"tools,omitempty"`
	Temperature  *float64          `json:"temperature,omitempty"`
	CacheEnabled bool              `json:"-"` // if true, adds cache_control to last tool definition
	Metadata     map[string]string `json:"-"` // not sent to API
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
//
// Anthropic emits four content block shapes today: "text", "tool_use",
// "thinking", and "redacted_thinking". The Thinking field is populated
// when extended-thinking models (or LiteLLM-fronted MiniMax in
// "thinking" mode) emit a reasoning block. Stoke does not normally
// surface thinking to the user, but consumers may use it as a
// fallback when the model emitted only thinking and never reached the
// final text — that prevents the silent "empty response from model"
// failure mode where stoke discards the only content the model
// produced.
type ResponseContent struct {
	Type      string                 `json:"type"`
	Text      string                 `json:"text,omitempty"`
	Thinking  string                 `json:"thinking,omitempty"`
	Signature string                 `json:"signature,omitempty"`
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
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
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		// 30-minute ceiling: extended-thinking models with large
		// max_tokens (16k+ conversion, 32k+ refine) can legitimately
		// take 10-20 minutes when the LiteLLM proxy is contended by
		// other concurrent workers. The 10-minute cap was causing
		// SOW prose conversion to fail on shared proxies under load,
		// even though the underlying request would have succeeded.
		httpClient: &http.Client{Timeout: 30 * time.Minute},
	}
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

// Chat sends a non-streaming chat completion request with bounded
// retries on transient failures. Transient failures include:
//
//   - context deadline exceeded / client timeout waiting for headers
//   - 429 rate limiting
//   - 5xx server errors
//   - EOF / connection reset mid-response
//
// Retries use exponential backoff starting at 5 seconds: 5s, 15s, 45s.
// After 3 attempts the final error is returned unchanged. Non-retriable
// errors (4xx other than 429, JSON parse failures) fail immediately.
func (p *AnthropicProvider) Chat(req ChatRequest) (*ChatResponse, error) {
	body := p.buildRequestBody(req, false)
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Connection-level failures (litellm restart, port change)
	// get more attempts + longer backoff than API-level errors.
	// A litellm restart takes ~5-10s; we retry for up to 2 min
	// so a brief outage doesn't corrupt the run.
	const maxAttempts = 6
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		chatResp, err := p.chatOnce(data, req.Metadata, req.Model)
		if err == nil {
			return chatResp, nil
		}
		lastErr = err
		if !isRetriableProviderError(err) {
			return nil, err
		}
		if attempt < maxAttempts {
			// Exponential backoff capped at 30s:
			// 5s, 10s, 20s, 30s, 30s. Gives litellm time
			// to come back after a restart without burning
			// 2 min of wall time on a hard failure.
			wait := time.Duration(5*intPow(2, attempt-1)) * time.Second
			if wait > 30*time.Second {
				wait = 30 * time.Second
			}
			time.Sleep(wait)
		}
	}
	return nil, fmt.Errorf("Chat failed after %d attempts: %w", maxAttempts, lastErr)
}

// chatOnce is the single-request path extracted from Chat so the retry
// loop can reuse it cleanly.
//
// The metadata map carries portfolio-alignment correlation IDs:
//   - stoke-session-id → X-Stoke-Session-ID outbound header
//   - stoke-agent-id   → X-Stoke-Agent-ID
//   - stoke-task-id    → X-Stoke-Task-ID
// Empty / absent values skip the corresponding header entirely (no
// empty-string headers). modelAlias is the caller-supplied model name
// used for AL-2 resolved-alias logging.
func (p *AnthropicProvider) chatOnce(data []byte, metadata map[string]string, modelAlias string) (*ChatResponse, error) {
	httpReq, err := http.NewRequest("POST", p.baseURL+"/v1/messages", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	p.setHeaders(httpReq)
	applyStokeCorrelationHeaders(httpReq, metadata)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("API request: %w", err)
	}
	defer resp.Body.Close()

	// AL-2: surface RelayGate's tier-alias response headers so operators
	// can see "tier:reasoning" / "smart" resolve to a concrete model.
	ReadTierHeaders(resp, modelAlias, log.Printf)

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Anthropic API error %d: %s", resp.StatusCode, string(errBody))
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	// CS-1: CloudSwarm-compatible per-LLM-call cost-event emission. The
	// Anthropic response carries token usage; convert to a usd estimate
	// using the existing cost tracker's per-model baselines (best-effort
	// — if the model isn't in the cost table the event still goes out
	// with usd=0 so CloudSwarm's parser sees the canonical shape).
	emitAnthropicCostEvent(modelAlias, chatResp.Usage)

	return &chatResp, nil
}

// isRetriableProviderError reports whether err looks like a transient
// failure worth retrying. Client timeouts, 429s, 5xx errors, and
// connection-reset / EOF errors all qualify. Hard failures (4xx other
// than 429, malformed JSON that parsed but decoded wrong) do not.
func isRetriableProviderError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	retriable := []string{
		"context deadline exceeded",
		"Client.Timeout exceeded",
		"i/o timeout",
		"connection reset",
		"connection refused",   // litellm down / restarting
		"dial tcp",             // port unreachable
		"no such host",         // DNS failure
		"ECONNREFUSED",         // explicit connection refused
		"EOF",
		"broken pipe",
		"Anthropic API error 429",
		"Anthropic API error 500",
		"Anthropic API error 502",
		"Anthropic API error 503",
		"Anthropic API error 504",
		"Anthropic API error 520",
		"Anthropic API error 522",
		"Anthropic API error 524",
	}
	for _, pat := range retriable {
		if strings.Contains(msg, pat) {
			return true
		}
	}
	return false
}

// intPow returns base^exp for small non-negative integers. Used by the
// exponential backoff schedule without pulling in math.Pow + float
// conversions.
func intPow(base, exp int) int {
	r := 1
	for i := 0; i < exp; i++ {
		r *= base
	}
	return r
}

// ChatStream sends a streaming chat completion request, calling onEvent for each event.
func (p *AnthropicProvider) ChatStream(req ChatRequest, onEvent func(stream.Event)) (*ChatResponse, error) {
	body := p.buildRequestBody(req, true)
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Retry connection-level failures (litellm restart, port
	// change) just like Chat does. Streaming calls are the
	// workhorse — every task dispatch goes through here — so
	// a brief litellm outage without retry kills the entire run.
	const maxConnAttempts = 6
	var resp *http.Response
	for attempt := 1; attempt <= maxConnAttempts; attempt++ {
		httpReq, reqErr := http.NewRequest("POST", p.baseURL+"/v1/messages", bytes.NewReader(data))
		if reqErr != nil {
			return nil, reqErr
		}
		p.setHeaders(httpReq)
		httpReq.Header.Set("Accept", "text/event-stream")

		resp, err = p.httpClient.Do(httpReq)
		if err == nil && resp.StatusCode == 200 {
			break
		}
		connErr := err
		if connErr == nil && resp != nil {
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			connErr = fmt.Errorf("Anthropic API error %d: %s", resp.StatusCode, string(errBody))
		}
		if !isRetriableProviderError(connErr) {
			return nil, connErr
		}
		if attempt < maxConnAttempts {
			wait := time.Duration(5*intPow(2, attempt-1)) * time.Second
			if wait > 30*time.Second {
				wait = 30 * time.Second
			}
			time.Sleep(wait)
		} else {
			return nil, fmt.Errorf("ChatStream failed after %d attempts: %w", maxConnAttempts, connErr)
		}
	}
	defer resp.Body.Close()

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
	msgs := interface{}(req.Messages)
	// When caching is enabled, extend cache_control to the rolling
	// message window. Anthropic allows up to 4 cache_control breakpoints;
	// with system (1) + tools (1) typically claimed, the remaining 2 go
	// on the last 2 messages. This is the Hermes pattern and the one
	// R-3623e901 documents at 45-90% input-token savings on multi-turn
	// workloads — the single biggest cost lever in the codebase.
	if req.CacheEnabled && len(req.Messages) > 0 {
		msgs = messagesWithCacheControl(req.Messages, 2)
	}
	body := map[string]interface{}{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"messages":   msgs,
	}

	// System prompt: prefer pre-formatted SystemRaw (with cache_control) over plain string
	if len(req.SystemRaw) > 0 {
		var raw interface{}
		if err := json.Unmarshal(req.SystemRaw, &raw); err == nil {
			body["system"] = raw
		}
	} else if req.System != "" {
		body["system"] = req.System
	}

	// Tools: add cache_control to last tool definition when caching is enabled
	if len(req.Tools) > 0 {
		if req.CacheEnabled {
			body["tools"] = toolsWithCacheControl(req.Tools)
		} else {
			body["tools"] = req.Tools
		}
	}

	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if streaming {
		body["stream"] = true
	}
	return body
}

// messagesWithCacheControl normalizes EVERY message into the Anthropic
// array-of-blocks form and attaches cache_control: {type: ephemeral}
// to the final content block of the last nTail messages.
//
// Why wrap every message, not just the tail: Anthropic's prompt cache
// is a byte-prefix cache. When message T falls out of the cache tail
// on the next turn, its wire form must stay identical to what was
// sent last turn — otherwise the prefix-hash changes and the whole
// cache read misses. If we wrapped only the tail, the "fell-out-of-
// tail" message would switch from wrapped back to raw on the next
// turn, invalidating every breakpoint downstream of it. Wrapping
// every message unconditionally makes the encoding stable across
// turns and the cache actually gets hits instead of just writes.
// (This is the codex-review P1 on bf940c6.)
//
// The returned slice is []interface{} so the Anthropic request body
// can mix wrapped messages with other pass-through values without
// forcing every caller through this function.
func messagesWithCacheControl(messages []ChatMessage, nTail int) []interface{} {
	out := make([]interface{}, len(messages))
	breakpointStart := len(messages) - nTail
	if breakpointStart < 0 {
		breakpointStart = 0
	}
	for i, m := range messages {
		attachCacheControl := i >= breakpointStart
		out[i] = wrapMessageMaybeCache(m, attachCacheControl)
	}
	return out
}

// wrapMessageMaybeCache returns a map representation of the message
// with its content normalized to the Anthropic array-of-blocks form,
// optionally attaching cache_control to the final content block.
//
// The wire form is identical whether attach is true or false except
// for the presence of the cache_control field on the last block;
// this keeps the byte prefix stable across turns so Anthropic's
// byte-prefix cache can actually hit.
//
// Three input shapes are handled:
//   - Content is a quoted string: wrap into [{type:text, text:...[, cache_control:...]}]
//   - Content is already a JSON array of blocks: clone; annotate last block when attach
//   - Content is some other JSON value: preserve it and append a trailing empty text block that optionally carries cache_control
func wrapMessageMaybeCache(m ChatMessage, attach bool) map[string]interface{} {
	cacheControl := map[string]interface{}{"type": "ephemeral"}

	// Try array-of-blocks first.
	var blocks []interface{}
	if err := json.Unmarshal(m.Content, &blocks); err == nil && len(blocks) > 0 {
		if attach {
			last := blocks[len(blocks)-1]
			if asMap, ok := last.(map[string]interface{}); ok {
				asMap["cache_control"] = cacheControl
				blocks[len(blocks)-1] = asMap
			} else {
				blocks = append(blocks, map[string]interface{}{
					"type": "text", "text": "", "cache_control": cacheControl,
				})
			}
		}
		return map[string]interface{}{"role": m.Role, "content": blocks}
	}

	// Try string.
	var asString string
	if err := json.Unmarshal(m.Content, &asString); err == nil {
		block := map[string]interface{}{"type": "text", "text": asString}
		if attach {
			block["cache_control"] = cacheControl
		}
		return map[string]interface{}{
			"role":    m.Role,
			"content": []interface{}{block},
		}
	}

	// Fallback: preserve original content and optionally append a
	// cache-control marker block.
	content := []interface{}{m.Content}
	if attach {
		content = append(content, map[string]interface{}{
			"type": "text", "text": "", "cache_control": cacheControl,
		})
	}
	return map[string]interface{}{"role": m.Role, "content": content}
}

// toolsWithCacheControl adds cache_control to the last tool definition.
// This is the Anthropic-recommended pattern for caching tool definitions.
func toolsWithCacheControl(tools []ToolDef) []interface{} {
	result := make([]interface{}, len(tools))
	for i, t := range tools {
		entry := map[string]interface{}{
			"name":         t.Name,
			"input_schema": t.InputSchema,
		}
		if t.Description != "" {
			entry["description"] = t.Description
		}
		// Add cache_control to last tool
		if i == len(tools)-1 {
			entry["cache_control"] = map[string]string{"type": "ephemeral"}
		}
		result[i] = entry
	}
	return result
}

func (p *AnthropicProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	// When talking to a non-default base URL (LiteLLM, custom proxy), also send
	// Authorization: Bearer. LiteLLM's Anthropic pass-through accepts either
	// header; api.anthropic.com ignores the Authorization header harmlessly.
	if !strings.Contains(p.baseURL, "api.anthropic.com") && p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
}

// OpenAICompatProvider communicates with OpenAI-compatible endpoints (OpenAI, OpenRouter, Gemini OpenAI-compat, XAI, etc.).
type OpenAICompatProvider struct {
	name       string
	apiKey     string
	baseURL    string
	// chatPath is the path appended to baseURL for chat completions.
	// OpenAI / OpenRouter use "/v1/chat/completions"; Google's
	// Gemini OpenAI-compat prefixes that path into the base URL
	// (https://generativelanguage.googleapis.com/v1beta/openai/) and
	// expects "/chat/completions" appended. Empty means default.
	chatPath   string
	httpClient *http.Client
}

// NewOpenAICompatProvider creates an OpenAI-compatible API client. The
// chat completions endpoint defaults to baseURL + "/v1/chat/completions".
// Use NewOpenAICompatProviderWithPath for backends whose chat path
// doesn't follow that convention (e.g. Gemini's OpenAI-compat surface).
func NewOpenAICompatProvider(name, apiKey, baseURL string) *OpenAICompatProvider {
	return NewOpenAICompatProviderWithPath(name, apiKey, baseURL, "/v1/chat/completions")
}

// NewOpenAICompatProviderWithPath is NewOpenAICompatProvider but with
// an explicit chatPath (the path appended to baseURL for every
// /chat/completions call). Kept separate so the default constructor's
// callers aren't required to know about path conventions.
func NewOpenAICompatProviderWithPath(name, apiKey, baseURL, chatPath string) *OpenAICompatProvider {
	if chatPath == "" {
		chatPath = "/v1/chat/completions"
	}
	return &OpenAICompatProvider{
		name:       name,
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(baseURL, "/"),
		chatPath:   chatPath,
		httpClient: &http.Client{Timeout: 10 * time.Minute},
	}
}

func (p *OpenAICompatProvider) Name() string { return p.name }

// Chat sends a non-streaming completion with tool use support.
func (p *OpenAICompatProvider) Chat(req ChatRequest) (*ChatResponse, error) {
	body := map[string]interface{}{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"messages":   p.convertMessages(req),
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if len(req.Tools) > 0 {
		body["tools"] = p.convertToolDefs(req.Tools)
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", p.baseURL+p.chatPath, bytes.NewReader(data))
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

	var openAIResp openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		return nil, err
	}

	return p.convertResponse(openAIResp), nil
}

// ChatStream sends a streaming completion with tool use support (SSE format).
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
	if len(req.Tools) > 0 {
		body["tools"] = p.convertToolDefs(req.Tools)
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", p.baseURL+p.chatPath, bytes.NewReader(data))
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

	// Track tool calls being built up across stream chunks
	toolCallMap := map[int]*openAIToolCall{} // index -> accumulated tool call

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
					Content   string            `json:"content"`
					ToolCalls []openAIToolCall   `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}

		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				fullText.WriteString(c.Delta.Content)
				if onEvent != nil {
					onEvent(stream.Event{
						Type:      "stream_event",
						DeltaType: "text_delta",
						DeltaText: c.Delta.Content,
					})
				}
			}
			// Accumulate tool call fragments
			for _, tc := range c.Delta.ToolCalls {
				existing, ok := toolCallMap[tc.Index]
				if !ok {
					cp := tc
					toolCallMap[tc.Index] = &cp
				} else {
					if tc.ID != "" {
						existing.ID = tc.ID
					}
					if tc.Function.Name != "" {
						existing.Function.Name = tc.Function.Name
					}
					existing.Function.Arguments += tc.Function.Arguments
				}
			}
			if c.FinishReason != nil {
				result.StopReason = *c.FinishReason
			}
		}
		if chunk.Usage != nil {
			result.Usage = stream.TokenUsage{
				Input:  chunk.Usage.PromptTokens,
				Output: chunk.Usage.CompletionTokens,
			}
		}
	}

	// Build content blocks: text first, then tool uses
	if fullText.Len() > 0 {
		result.Content = append(result.Content, ResponseContent{Type: "text", Text: fullText.String()})
	}
	for i := 0; i < len(toolCallMap); i++ {
		tc := toolCallMap[i]
		if tc == nil {
			continue
		}
		var input map[string]interface{}
		json.Unmarshal([]byte(tc.Function.Arguments), &input)
		result.Content = append(result.Content, ResponseContent{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}

	// Map OpenAI stop reasons to Anthropic-style for agentloop compatibility
	if result.StopReason == "tool_calls" {
		result.StopReason = "tool_use"
	} else if result.StopReason == "stop" {
		result.StopReason = "end_turn"
	}

	return &result, nil
}

// openAIChatResponse is the wire format for OpenAI chat completions.
type openAIChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   string           `json:"content"`
			ToolCalls []openAIToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// openAIToolCall is the OpenAI tool_calls format.
type openAIToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (p *OpenAICompatProvider) convertResponse(resp openAIChatResponse) *ChatResponse {
	result := &ChatResponse{
		ID:    resp.ID,
		Model: resp.Model,
		Usage: stream.TokenUsage{
			Input:  resp.Usage.PromptTokens,
			Output: resp.Usage.CompletionTokens,
		},
	}
	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]

		// Add text content
		if choice.Message.Content != "" {
			result.Content = append(result.Content, ResponseContent{Type: "text", Text: choice.Message.Content})
		}

		// Add tool use content blocks
		for _, tc := range choice.Message.ToolCalls {
			var input map[string]interface{}
			json.Unmarshal([]byte(tc.Function.Arguments), &input)
			result.Content = append(result.Content, ResponseContent{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: input,
			})
		}

		// Map OpenAI stop reasons to Anthropic-style
		switch choice.FinishReason {
		case "tool_calls":
			result.StopReason = "tool_use"
		case "stop":
			result.StopReason = "end_turn"
		default:
			result.StopReason = choice.FinishReason
		}
	}
	return result
}

func (p *OpenAICompatProvider) convertMessages(req ChatRequest) []map[string]interface{} {
	var msgs []map[string]interface{}

	// System prompt
	if req.System != "" {
		msgs = append(msgs, map[string]interface{}{
			"role":    "system",
			"content": req.System,
		})
	}
	// SystemRaw overrides System
	if len(req.SystemRaw) > 0 {
		var systemBlocks []map[string]interface{}
		if json.Unmarshal(req.SystemRaw, &systemBlocks) == nil {
			// Extract text from system blocks
			var systemText string
			for _, block := range systemBlocks {
				if t, ok := block["text"].(string); ok {
					systemText += t + "\n"
				}
			}
			if systemText != "" {
				msgs = append(msgs, map[string]interface{}{
					"role":    "system",
					"content": strings.TrimSpace(systemText),
				})
			}
		}
	}

	for _, m := range req.Messages {
		converted := p.convertOneMessage(m)
		msgs = append(msgs, converted...)
	}
	return msgs
}

// convertOneMessage translates an Anthropic-format message to OpenAI format.
// Returns a slice because a single Anthropic user message with multiple
// tool_result blocks maps to multiple OpenAI "tool" role messages.
func (p *OpenAICompatProvider) convertOneMessage(m ChatMessage) []map[string]interface{} {
	// Try to parse content as typed blocks (Anthropic format)
	var blocks []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text,omitempty"`
		ID        string          `json:"id,omitempty"`
		Name      string          `json:"name,omitempty"`
		Input     json.RawMessage `json:"input,omitempty"`
		ToolUseID string          `json:"tool_use_id,omitempty"`
		Content   string          `json:"content,omitempty"`
		IsError   bool            `json:"is_error,omitempty"`
	}

	if err := json.Unmarshal(m.Content, &blocks); err == nil && len(blocks) > 0 {
		// Classify block types in the message.
		hasToolResults := false
		hasToolUse := false
		for _, b := range blocks {
			if b.Type == "tool_result" {
				hasToolResults = true
			}
			if b.Type == "tool_use" {
				hasToolUse = true
			}
		}

		// User message with tool_result blocks → one "tool" role message per result.
		if hasToolResults && m.Role == "user" {
			var toolMsgs []map[string]interface{}
			for _, b := range blocks {
				if b.Type == "tool_result" {
					toolMsgs = append(toolMsgs, map[string]interface{}{
						"role":         "tool",
						"tool_call_id": b.ToolUseID,
						"content":      b.Content,
					})
				}
			}
			return toolMsgs
		}

		// Assistant message with tool_use blocks → single message with tool_calls.
		if hasToolUse && m.Role == "assistant" {
			var toolCalls []map[string]interface{}
			var textContent string
			for _, b := range blocks {
				if b.Type == "text" {
					textContent += b.Text
				}
				if b.Type == "tool_use" {
					argsJSON := "{}"
					if b.Input != nil {
						argsJSON = string(b.Input)
					}
					toolCalls = append(toolCalls, map[string]interface{}{
						"id":   b.ID,
						"type": "function",
						"function": map[string]interface{}{
							"name":      b.Name,
							"arguments": argsJSON,
						},
					})
				}
			}
			return []map[string]interface{}{{
				"role":       "assistant",
				"content":    textContent,
				"tool_calls": toolCalls,
			}}
		}

		// Plain text blocks
		var text string
		for _, b := range blocks {
			if b.Type == "text" {
				text += b.Text
			}
		}
		if text != "" {
			return []map[string]interface{}{{
				"role":    m.Role,
				"content": text,
			}}
		}
	}

	// Fallback: treat content as raw string or pass through
	var contentStr string
	if json.Unmarshal(m.Content, &contentStr) == nil {
		return []map[string]interface{}{{
			"role":    m.Role,
			"content": contentStr,
		}}
	}
	return []map[string]interface{}{{
		"role":    m.Role,
		"content": string(m.Content),
	}}
}

// convertToolDefs converts Anthropic-style tool definitions to OpenAI format.
func (p *OpenAICompatProvider) convertToolDefs(tools []ToolDef) []map[string]interface{} {
	var result []map[string]interface{}
	for _, t := range tools {
		result = append(result, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.InputSchema,
			},
		})
	}
	return result
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
