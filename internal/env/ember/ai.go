package ember

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

// AIClient communicates with Ember's managed AI endpoint (/v1/ai/chat).
// It provides OpenAI-compatible chat completions via Ember's OpenRouter proxy.
type AIClient struct {
	apiURL string
	token  string
	http   *http.Client
}

// NewAIClient creates an Ember managed AI client.
func NewAIClient(apiURL, token string) *AIClient {
	return &AIClient{
		apiURL: apiURL,
		token:  token,
		http:   &http.Client{Timeout: 5 * time.Minute},
	}
}

// ChatMessage is a single message in a chat conversation.
type ChatMessage struct {
	Role    string `json:"role"`    // "system", "user", "assistant"
	Content string `json:"content"`
}

// ChatRequest is the request body for /v1/ai/chat.
type ChatRequest struct {
	Model    string        `json:"model,omitempty"` // default: "anthropic/claude-sonnet-4"
	Messages []ChatMessage `json:"messages"`
	Stream   bool          `json:"stream,omitempty"`
}

// ChatChoice is a single completion choice.
type ChatChoice struct {
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// ChatUsage tracks token usage and cost.
type ChatUsage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	TotalCost        float64 `json:"total_cost"`
}

// ChatResponse is the response from /v1/ai/chat (non-streaming).
type ChatResponse struct {
	Choices []ChatChoice `json:"choices"`
	Usage   ChatUsage    `json:"usage"`
}

// StreamDelta is a partial token in a streaming response.
type StreamDelta struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// AIUsage is the response from /v1/ai/usage.
type AIUsage struct {
	Period       string  `json:"period"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	MarkupUSD    float64 `json:"markup_usd"`
	TotalUSD     float64 `json:"total_usd"`
	RequestCount int     `json:"request_count"`
}

// Chat sends a non-streaming chat completion request.
func (c *AIClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	req.Stream = false
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+"/v1/ai/chat", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ember ai %d: %s", resp.StatusCode, body)
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("ember ai decode: %w", err)
	}
	return &chatResp, nil
}

// ChatStream sends a streaming chat completion request.
// The callback is called for each delta token. Returns total usage when complete.
func (c *AIClient) ChatStream(ctx context.Context, req ChatRequest, onDelta func(content string)) (*ChatUsage, error) {
	req.Stream = true
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+"/v1/ai/chat", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ember ai stream %d: %s", resp.StatusCode, body)
	}

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

		var delta StreamDelta
		if json.Unmarshal([]byte(payload), &delta) == nil {
			for _, choice := range delta.Choices {
				if choice.Delta.Content != "" && onDelta != nil {
					onDelta(choice.Delta.Content)
				}
			}
		}
	}

	// Usage is not returned in streaming responses from Ember.
	return nil, scanner.Err()
}

// Usage retrieves AI usage statistics.
func (c *AIClient) Usage(ctx context.Context, period string) (*AIUsage, error) {
	if period == "" {
		period = "month"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL+"/v1/ai/usage?period="+period, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ember ai usage %d: %s", resp.StatusCode, body)
	}

	var usage AIUsage
	if err := json.NewDecoder(resp.Body).Decode(&usage); err != nil {
		return nil, err
	}
	return &usage, nil
}
