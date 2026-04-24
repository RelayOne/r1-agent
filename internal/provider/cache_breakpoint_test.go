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
	// Invariant: every message must wrap to the same shape (array-of-
	// blocks) so Anthropic's byte-prefix cache reads are stable across
	// turns. Only the last nTail carry cache_control.
	for i := 0; i < len(out); i++ {
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
		_, hasCC := last["cache_control"]
		shouldHaveCC := i >= 1 // nTail=2 → last two messages
		if shouldHaveCC && !hasCC {
			t.Fatalf("message %d should have cache_control: %+v", i, last)
		}
		if !shouldHaveCC && hasCC {
			t.Fatalf("message %d must NOT have cache_control (would shift the prefix bytes): %+v", i, last)
		}
	}
}

// TestPrefixStabilityAcrossTurns is the regression test for the
// codex-review P1 on bf940c6: on turn N+1 the same message should
// serialize to the same bytes as on turn N (minus its own cache_control
// if it fell out of the tail), so the cache prefix hash matches.
func TestPrefixStabilityAcrossTurns(t *testing.T) {
	turnN := []ChatMessage{
		{Role: "user", Content: json.RawMessage(`"first"`)},
		{Role: "assistant", Content: json.RawMessage(`"second"`)},
	}
	turnN1 := []ChatMessage{
		{Role: "user", Content: json.RawMessage(`"first"`)},
		{Role: "assistant", Content: json.RawMessage(`"second"`)},
		{Role: "user", Content: json.RawMessage(`"third"`)},
	}
	// On turn N, messages 0 + 1 are both in the tail (nTail=2).
	outN := messagesWithCacheControl(turnN, 2)
	// On turn N+1, message 0 falls out of the tail; message 1 & 2 are in.
	outN1 := messagesWithCacheControl(turnN1, 2)

	// Strip cache_control from both and compare: the non-cache-control
	// parts must be byte-identical so Anthropic's prefix cache can hit.
	raw := func(v interface{}) string {
		b, _ := json.Marshal(v)
		s := string(b)
		// Remove cache_control entries; Anthropic ignores their presence
		// for the purposes of byte-prefix matching, so we normalize
		// them out of the comparison.
		for strings.Contains(s, `,"cache_control":{"type":"ephemeral"}`) {
			s = strings.Replace(s, `,"cache_control":{"type":"ephemeral"}`, "", 1)
		}
		for strings.Contains(s, `"cache_control":{"type":"ephemeral"},`) {
			s = strings.Replace(s, `"cache_control":{"type":"ephemeral"},`, "", 1)
		}
		return s
	}
	msg0N := raw(outN[0])
	msg0N1 := raw(outN1[0])
	if msg0N != msg0N1 {
		t.Fatalf("message 0 encoding changed between turns — cache will miss:\nturnN: %s\nturnN1:%s", msg0N, msg0N1)
	}
	msg1N := raw(outN[1])
	msg1N1 := raw(outN1[1])
	if msg1N != msg1N1 {
		t.Fatalf("message 1 encoding changed between turns — cache will miss:\nturnN: %s\nturnN1:%s", msg1N, msg1N1)
	}
}

func TestMessagesWithCacheControlBlockArray(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"hello"}]`)},
	}
	out := messagesWithCacheControl(msgs, 1)
	m, ok := out[0].(map[string]interface{})
	if !ok {
		t.Fatalf("out[0]: unexpected type: %T", out[0])
	}
	blocks, ok := m["content"].([]interface{})
	if !ok {
		t.Fatalf("content: unexpected type: %T", m["content"])
	}
	last, ok := blocks[len(blocks)-1].(map[string]interface{})
	if !ok {
		t.Fatalf("last block: unexpected type: %T", blocks[len(blocks)-1])
	}
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
