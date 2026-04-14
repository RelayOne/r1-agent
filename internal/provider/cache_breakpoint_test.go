package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessagesWithCacheControlStringContent(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: json.RawMessage(`"first"`)},
		{Role: "assistant", Content: json.RawMessage(`"second"`)},
		{Role: "user", Content: json.RawMessage(`"third"`)},
	}
	out := messagesWithCacheControl(msgs, 2)
	// First message is outside the cache tail — passes through.
	if _, ok := out[0].(ChatMessage); !ok {
		t.Fatalf("message 0 must pass through as ChatMessage; got %T", out[0])
	}
	// Last two messages must be wrapped maps with cache_control on the
	// final content block.
	for i := 1; i < 3; i++ {
		m, ok := out[i].(map[string]interface{})
		if !ok {
			t.Fatalf("message %d: expected wrapped map, got %T", i, out[i])
		}
		blocks, ok := m["content"].([]interface{})
		if !ok || len(blocks) == 0 {
			t.Fatalf("message %d: expected content array", i)
		}
		last, ok := blocks[len(blocks)-1].(map[string]interface{})
		if !ok {
			t.Fatalf("message %d: last block not a map", i)
		}
		cc, ok := last["cache_control"].(map[string]interface{})
		if !ok {
			t.Fatalf("message %d: missing cache_control; got %+v", i, last)
		}
		if cc["type"] != "ephemeral" {
			t.Fatalf("message %d: expected ephemeral, got %v", i, cc["type"])
		}
	}
}

func TestMessagesWithCacheControlBlockArray(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"hello"}]`)},
	}
	out := messagesWithCacheControl(msgs, 1)
	m := out[0].(map[string]interface{})
	blocks := m["content"].([]interface{})
	last := blocks[len(blocks)-1].(map[string]interface{})
	if _, ok := last["cache_control"]; !ok {
		t.Fatalf("expected cache_control on last block: %+v", last)
	}
	if last["text"] != "hello" {
		t.Fatalf("block text mangled: %+v", last)
	}
}

func TestBuildRequestBodyCacheEnabledAddsCacheControl(t *testing.T) {
	p := NewAnthropicProvider("test-key", "https://api.anthropic.com")
	req := ChatRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 100,
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
			{Role: "assistant", Content: json.RawMessage(`"hello"`)},
			{Role: "user", Content: json.RawMessage(`"follow up"`)},
		},
		CacheEnabled: true,
	}
	body := p.buildRequestBody(req, false)
	msgsRaw, _ := json.Marshal(body["messages"])
	got := string(msgsRaw)
	// At least one "cache_control":{"type":"ephemeral"} should appear
	// in the message window — that's the whole point of the feature.
	if !strings.Contains(got, `"cache_control":{"type":"ephemeral"}`) {
		t.Fatalf("cache_control not present in messages:\n%s", got)
	}
}

func TestBuildRequestBodyCacheDisabledPassesThrough(t *testing.T) {
	p := NewAnthropicProvider("test-key", "https://api.anthropic.com")
	req := ChatRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 100,
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
		CacheEnabled: false,
	}
	body := p.buildRequestBody(req, false)
	msgsRaw, _ := json.Marshal(body["messages"])
	got := string(msgsRaw)
	// When disabled, messages pass through unchanged — no cache_control
	// should be injected.
	if strings.Contains(got, "cache_control") {
		t.Fatalf("cache_control should not be set when disabled:\n%s", got)
	}
}
