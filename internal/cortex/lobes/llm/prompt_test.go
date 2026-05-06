package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/provider"
)

func TestLobePromptBuilder_SortsToolsAlphabetically(t *testing.T) {
	b := LobePromptBuilder{
		Model:        "claude-haiku-4-5",
		SystemPrompt: "you are a lobe",
		Tools: []provider.ToolDef{
			{Name: "zebra"},
			{Name: "apple"},
			{Name: "mango"},
		},
	}
	req := b.Build("hi", nil)
	if len(req.Tools) != 3 {
		t.Fatalf("len(tools)=%d, want 3", len(req.Tools))
	}
	got := []string{req.Tools[0].Name, req.Tools[1].Name, req.Tools[2].Name}
	want := []string{"apple", "mango", "zebra"}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("tool[%d]=%s, want %s", i, got[i], want[i])
		}
	}
}

func TestLobePromptBuilder_SetsCacheControl1h(t *testing.T) {
	b := LobePromptBuilder{Model: "claude-haiku-4-5", SystemPrompt: "you are a lobe"}
	req := b.Build("hi", nil)
	// Decode SystemRaw to find cache_control marker.
	var blocks []map[string]any
	if err := json.Unmarshal(req.SystemRaw, &blocks); err != nil {
		t.Fatalf("decode system: %v", err)
	}
	// Find any block with cache_control set to ephemeral 1h.
	found := false
	for _, blk := range blocks {
		cc, ok := blk["cache_control"].(map[string]any)
		if !ok {
			continue
		}
		if cc["type"] == "ephemeral" && cc["ttl"] == "1h" {
			found = true
			break
		}
	}
	if !found {
		raw, _ := json.MarshalIndent(blocks, "", "  ")
		t.Fatalf("expected cache_control type=ephemeral ttl=1h on a system block; got:\n%s", string(raw))
	}
}

func TestLobePromptBuilder_DefaultMaxTokens500(t *testing.T) {
	b := LobePromptBuilder{Model: "claude-haiku-4-5", SystemPrompt: "x"}
	req := b.Build("hi", nil)
	if req.MaxTokens != 500 {
		t.Fatalf("MaxTokens=%d, want 500", req.MaxTokens)
	}
}

func TestLobePromptBuilder_RespectsExplicitMaxTokens(t *testing.T) {
	b := LobePromptBuilder{Model: "claude-haiku-4-5", SystemPrompt: "x", MaxTokens: 800}
	req := b.Build("hi", nil)
	if req.MaxTokens != 800 {
		t.Fatalf("MaxTokens=%d, want 800", req.MaxTokens)
	}
}

func TestLobePromptBuilder_AppendsUserMessage(t *testing.T) {
	b := LobePromptBuilder{Model: "claude-haiku-4-5", SystemPrompt: "x"}
	prefix := []provider.ChatMessage{
		{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"hello"}]`)},
	}
	req := b.Build("query", prefix)
	if len(req.Messages) != 2 {
		t.Fatalf("messages=%d, want 2", len(req.Messages))
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" {
		t.Fatalf("last role=%s, want user", last.Role)
	}
	if !strings.Contains(string(last.Content), "query") {
		t.Fatalf("last message missing 'query': %s", string(last.Content))
	}
}

func TestLobePromptBuilder_CacheEnabled(t *testing.T) {
	b := LobePromptBuilder{Model: "claude-haiku-4-5", SystemPrompt: "x"}
	req := b.Build("hi", nil)
	if !req.CacheEnabled {
		t.Fatal("CacheEnabled should be true (cache key stability is the whole point of LobePromptBuilder)")
	}
}
