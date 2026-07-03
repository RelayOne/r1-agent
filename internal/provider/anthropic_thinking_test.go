package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildRequestBodyNoThinkingByDefault(t *testing.T) {
	p := NewAnthropicProvider("test-key", "https://test.api")

	// No Thinking on the request → wire bytes carry no "thinking" key.
	body := p.buildRequestBody(ChatRequest{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 16000,
	}, false)
	if _, has := body["thinking"]; has {
		t.Error("request without Thinking must not emit a thinking key")
	}

	// Unknown model with Thinking set → fail closed, no key.
	body = p.buildRequestBody(ChatRequest{
		Model:     "tier:reasoning",
		MaxTokens: 16000,
		Thinking:  &ThinkingSpec{BudgetTokens: 4096},
	}, false)
	if _, has := body["thinking"]; has {
		t.Error("unknown model must not emit a thinking key")
	}
}

func TestBuildRequestBodyThinkingLegacyBudget(t *testing.T) {
	p := NewAnthropicProvider("test-key", "https://test.api")
	temp := 0.2
	body := p.buildRequestBody(ChatRequest{
		Model:       "claude-sonnet-4-5-20250929",
		MaxTokens:   16000,
		Temperature: &temp,
		Thinking:    &ThinkingSpec{BudgetTokens: 4096},
	}, false)

	th, ok := body["thinking"].(map[string]interface{})
	if !ok {
		t.Fatalf("thinking=%v, want map", body["thinking"])
	}
	if th["type"] != "enabled" {
		t.Errorf("thinking.type=%v, want enabled", th["type"])
	}
	if th["budget_tokens"] != 4096 {
		t.Errorf("budget_tokens=%v, want 4096", th["budget_tokens"])
	}
	if _, has := body["temperature"]; has {
		t.Error("temperature must be dropped when thinking is active")
	}
}

func TestBuildRequestBodyThinkingAdaptive(t *testing.T) {
	p := NewAnthropicProvider("test-key", "https://test.api")
	body := p.buildRequestBody(ChatRequest{
		Model:     "claude-opus-4-8",
		MaxTokens: 16000,
		Thinking:  &ThinkingSpec{BudgetTokens: 4096},
	}, true)

	th, ok := body["thinking"].(map[string]interface{})
	if !ok {
		t.Fatalf("thinking=%v, want map", body["thinking"])
	}
	if th["type"] != "adaptive" {
		t.Errorf("thinking.type=%v, want adaptive", th["type"])
	}
	if _, has := th["budget_tokens"]; has {
		t.Error("adaptive thinking must not carry budget_tokens")
	}
}

func TestWrapMessageCacheSkipsThinking(t *testing.T) {
	// cache_control must land on the last NON-thinking block: the API
	// rejects cache_control on thinking / redacted_thinking blocks.
	m := ChatMessage{
		Role:    roleAssistant,
		Content: []byte(`[{"type":"text","text":"hi"},{"type":"thinking","thinking":"t","signature":"s"}]`),
	}
	out := wrapMessageMaybeCache(m, true)
	blocks, ok := out["content"].([]interface{})
	if !ok || len(blocks) != 2 {
		t.Fatalf("content=%v, want 2 blocks", out["content"])
	}
	textBlock := blocks[0].(map[string]interface{})
	thinkBlock := blocks[1].(map[string]interface{})
	if _, has := textBlock["cache_control"]; !has {
		t.Error("cache_control should attach to the text block")
	}
	if _, has := thinkBlock["cache_control"]; has {
		t.Error("cache_control must never attach to a thinking block")
	}

	// Thinking-only message: no breakpoint at all (and no marker
	// block appended — the block list stays untouched).
	m2 := ChatMessage{
		Role:    roleAssistant,
		Content: []byte(`[{"type":"thinking","thinking":"t","signature":"s"},{"type":"redacted_thinking","data":"xx"}]`),
	}
	out2 := wrapMessageMaybeCache(m2, true)
	blocks2 := out2["content"].([]interface{})
	if len(blocks2) != 2 {
		t.Fatalf("thinking-only message grew to %d blocks", len(blocks2))
	}
	for i, b := range blocks2 {
		if _, has := b.(map[string]interface{})["cache_control"]; has {
			t.Errorf("block %d: cache_control on thinking-family block", i)
		}
	}
}

func TestOpenAICompatIgnoresThinking(t *testing.T) {
	// The OpenAI-compatible adapter builds its own body; ChatRequest.
	// Thinking must never reach the wire.
	var captured map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","model":"m","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	p := NewOpenAICompatProvider("test", "key", srv.URL)
	_, err := p.Chat(ChatRequest{
		Model:     "gpt-4.1",
		MaxTokens: 100,
		Messages:  []ChatMessage{{Role: roleUser, Content: []byte(`"hello"`)}},
		Thinking:  &ThinkingSpec{BudgetTokens: 4096},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if captured == nil {
		t.Fatal("no request captured")
	}
	if _, has := captured["thinking"]; has {
		t.Error("openai-compat body must not carry a thinking key")
	}
}
