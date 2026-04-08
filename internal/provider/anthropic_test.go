package provider

import (
	"testing"
)

func TestResolveProviderClaude(t *testing.T) {
	p := ResolveProvider("claude-opus-4-6")
	if p.Name() != "anthropic" {
		t.Errorf("expected anthropic, got %s", p.Name())
	}
}

func TestResolveProviderGPT(t *testing.T) {
	p := ResolveProvider("gpt-4.1")
	if p.Name() != "openai" {
		t.Errorf("expected openai, got %s", p.Name())
	}
}

func TestResolveProviderGrok(t *testing.T) {
	p := ResolveProvider("grok-3")
	if p.Name() != "xai" {
		t.Errorf("expected xai, got %s", p.Name())
	}
}

func TestResolveProviderOpenRouter(t *testing.T) {
	p := ResolveProvider("anthropic/claude-sonnet-4")
	if p.Name() != "openrouter" {
		t.Errorf("expected openrouter, got %s", p.Name())
	}
}

func TestResolveProviderDefault(t *testing.T) {
	p := ResolveProvider("unknown-model")
	if p.Name() != "anthropic" {
		t.Errorf("expected anthropic default, got %s", p.Name())
	}
}

func TestAnthropicProviderBuildRequestBody(t *testing.T) {
	p := NewAnthropicProvider("test-key", "https://test.api")

	req := ChatRequest{
		Model:     "claude-opus-4-6",
		System:    "You are helpful",
		MaxTokens: 4096,
	}

	body := p.buildRequestBody(req, true)
	if body["stream"] != true {
		t.Error("expected stream=true")
	}
	if body["system"] != "You are helpful" {
		t.Error("expected system prompt")
	}
	if body["model"] != "claude-opus-4-6" {
		t.Error("expected model")
	}
}

func TestOpenAICompatProviderConvertMessages(t *testing.T) {
	p := NewOpenAICompatProvider("test", "key", "https://test")

	req := ChatRequest{
		System: "system prompt",
		Messages: []ChatMessage{
			{Role: "user", Content: []byte(`"hello"`)},
		},
	}

	msgs := p.convertMessages(req)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(msgs))
	}
	if msgs[0]["role"] != "system" {
		t.Error("first message should be system")
	}
}
