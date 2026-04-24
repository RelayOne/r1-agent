package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClient(t *testing.T) {
	c := NewClient(Config{
		Provider: ProviderAnthropic,
		APIKey:   "test-key",
		Model:    "claude-sonnet-4-20250514",
	})

	if c.Provider() != ProviderAnthropic {
		t.Errorf("expected anthropic, got %s", c.Provider())
	}
	if c.Model() != "claude-sonnet-4-20250514" {
		t.Errorf("unexpected model: %s", c.Model())
	}
}

func TestCompleteAnthropic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Error("missing api key")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("missing version header")
		}

		resp := map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "Hello, World!"},
			},
			"stop_reason": "end_turn",
			"model":       "claude-sonnet-4-20250514",
			"usage": map[string]any{
				"input_tokens":  100,
				"output_tokens": 20,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(Config{
		Provider: ProviderAnthropic,
		APIKey:   "test-key",
		BaseURL:  server.URL,
		Model:    "claude-sonnet-4-20250514",
	})

	resp, err := c.Complete(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Hello, World!" {
		t.Errorf("expected 'Hello, World!', got %q", resp.Content)
	}
	if resp.Usage.InputTokens != 100 {
		t.Errorf("expected 100 input tokens, got %d", resp.Usage.InputTokens)
	}
}

func TestCompleteOpenAI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing auth header")
		}

		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message":       map[string]any{"content": "Hi there!"},
					"finish_reason": "stop",
				},
			},
			"model": "gpt-4o",
			"usage": map[string]any{
				"prompt_tokens":     50,
				"completion_tokens": 10,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(Config{
		Provider: ProviderOpenAI,
		APIKey:   "test-key",
		BaseURL:  server.URL,
		Model:    "gpt-4o",
	})

	resp, err := c.Complete(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "Hello"}},
		System:   "You are helpful",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Hi there!" {
		t.Errorf("expected 'Hi there!', got %q", resp.Content)
	}
}

func TestStreamAnthropic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n"))
		w.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\" World\"}}\n\n"))
		w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	c := NewClient(Config{
		Provider: ProviderAnthropic,
		APIKey:   "test-key",
		BaseURL:  server.URL,
	})

	var chunks []string
	usage, err := c.Stream(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "Hi"}},
	}, func(e StreamEvent) {
		if e.Type == "text" {
			chunks = append(chunks, e.Text)
		}
	})

	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 || chunks[0] != "Hello" || chunks[1] != " World" {
		t.Errorf("unexpected chunks: %v", chunks)
	}
	if usage.OutputTokens != 5 {
		t.Errorf("expected 5 output tokens, got %d", usage.OutputTokens)
	}
}

func TestAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"error": {"message": "rate limited"}}`))
	}))
	defer server.Close()

	c := NewClient(Config{
		Provider: ProviderAnthropic,
		APIKey:   "key",
		BaseURL:  server.URL,
	})

	_, err := c.Complete(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("should error on 429")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T", err)
	}
	if !apiErr.IsRateLimit() {
		t.Error("should be rate limit error")
	}
	if !apiErr.IsRetryable() {
		t.Error("rate limit should be retryable")
	}
}

func TestAPIErrorAuth(t *testing.T) {
	e := &APIError{StatusCode: 401, Provider: ProviderAnthropic}
	if !e.IsAuth() {
		t.Error("401 should be auth error")
	}
	if e.IsRetryable() {
		t.Error("auth error should not be retryable")
	}
}

func TestDefaultConfigs(t *testing.T) {
	for _, p := range []Provider{ProviderAnthropic, ProviderOpenAI, ProviderOpenRouter} {
		cfg, ok := DefaultConfigs[p]
		if !ok {
			t.Errorf("missing default config for %s", p)
		}
		if cfg.BaseURL == "" {
			t.Errorf("empty base URL for %s", p)
		}
	}
}

func TestBuildAnthropicBody(t *testing.T) {
	c := NewClient(Config{Provider: ProviderAnthropic, Model: "test"})
	temp := 0.5
	body, err := c.buildRequestBody(Request{
		Messages:    []Message{{Role: "user", Content: "hi"}},
		System:      "be helpful",
		Temperature: &temp,
	})
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	json.Unmarshal(body, &parsed)
	if parsed["system"] != "be helpful" {
		t.Error("should include system prompt")
	}
}

func TestBuildOpenAIBody(t *testing.T) {
	c := NewClient(Config{Provider: ProviderOpenAI, Model: "gpt-4o"})
	body, err := c.buildRequestBody(Request{
		Messages: []Message{{Role: "user", Content: "hi"}},
		System:   "be helpful",
	})
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	json.Unmarshal(body, &parsed)
	msgs, _ := parsed["messages"].([]any)
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages (system + user), got %d", len(msgs))
	}
}
