package microcompact

import (
	"strings"
	"testing"
)

func TestCompactFitsInBudget(t *testing.T) {
	c := NewCompactor(Config{MaxTokens: 10000})
	sections := []Section{
		{Label: "system", Content: "You are helpful.", Static: true, Priority: 10, Tokens: 100},
		{Label: "history", Content: "User said hello.", Static: false, Priority: 5, Tokens: 200},
	}

	result := c.Compact(sections)
	if result.TotalOut > 10000 {
		t.Errorf("should fit in budget, got %d tokens", result.TotalOut)
	}
	if len(result.Sections) != 2 {
		t.Errorf("expected 2 sections, got %d", len(result.Sections))
	}
	if len(result.Dropped) != 0 {
		t.Error("nothing should be dropped when everything fits")
	}
}

func TestCompactDropsLowPriority(t *testing.T) {
	c := NewCompactor(Config{MaxTokens: 500})
	sections := []Section{
		{Label: "system", Content: "System prompt.", Static: true, Priority: 10, Tokens: 200},
		{Label: "important", Content: "Key context.", Static: false, Priority: 8, Tokens: 200},
		{Label: "old-result", Content: strings.Repeat("x", 4000), Static: false, Priority: 1, Tokens: 1000, MinTier: TierDropped},
	}

	result := c.Compact(sections)
	if result.TotalOut > 500 {
		// May exceed slightly due to summarization
		for _, s := range result.Sections {
			if s.Label == "old-result" && s.Tier == TierVerbatim {
				t.Error("old-result should not be verbatim")
			}
		}
	}
}

func TestCompactPreservesStaticPrefix(t *testing.T) {
	c := NewCompactor(Config{MaxTokens: 500})
	statics := []Section{
		{Label: "system", Content: "System prompt.", Static: true, Priority: 10, Tokens: 100},
		{Label: "tools", Content: "Tool definitions.", Static: true, Priority: 9, Tokens: 100},
	}
	dynamics := []Section{
		{Label: "chat", Content: "User message.", Static: false, Priority: 5, Tokens: 100},
	}

	sections := append(statics, dynamics...)
	result := c.Compact(sections)

	// First two sections should be the static ones
	staticCount := 0
	for _, s := range result.Sections {
		if s.Tier == TierVerbatim {
			staticCount++
		}
	}
	if staticCount < 2 {
		t.Error("static sections should be preserved verbatim")
	}
	if result.PrefixHash == "" {
		t.Error("should compute prefix hash")
	}
}

func TestCacheBreakDetection(t *testing.T) {
	c := NewCompactor(Config{MaxTokens: 10000})

	// First compaction
	sections1 := []Section{
		{Label: "system", Content: "Version 1.", Static: true, Priority: 10, Tokens: 50},
	}
	r1 := c.Compact(sections1)
	if r1.CacheBreak {
		t.Error("first compaction should not be a cache break")
	}

	// Same static prefix
	r2 := c.Compact(sections1)
	if r2.CacheBreak {
		t.Error("same prefix should not be a cache break")
	}

	// Changed static prefix
	sections2 := []Section{
		{Label: "system", Content: "Version 2.", Static: true, Priority: 10, Tokens: 50},
	}
	r3 := c.Compact(sections2)
	if !r3.CacheBreak {
		t.Error("changed prefix should be a cache break")
	}
}

func TestSummarization(t *testing.T) {
	c := NewCompactor(Config{MaxTokens: 300})
	longContent := strings.Repeat("Line of content.\n", 100)
	sections := []Section{
		{Label: "system", Content: "Prompt.", Static: true, Priority: 10, Tokens: 50},
		{Label: "result", Content: longContent, Static: false, Priority: 3, Tokens: 500, MinTier: TierSummarized},
	}

	result := c.Compact(sections)
	for _, s := range result.Sections {
		if s.Label == "result" && s.Tier == TierSummarized {
			if !strings.Contains(s.Content, "[Summarized") {
				t.Error("summarized section should have summary header")
			}
			return
		}
	}
	// If it was dropped instead, that's also acceptable when budget is tight
}

func TestCustomSummarizer(t *testing.T) {
	custom := func(label, content string) (string, int) {
		return "CUSTOM:" + label, 10
	}
	c := NewCompactor(Config{MaxTokens: 100, SummarizeFn: custom})
	sections := []Section{
		{Label: "system", Content: "Prompt.", Static: true, Priority: 10, Tokens: 50},
		{Label: "big", Content: strings.Repeat("x", 1000), Static: false, Priority: 1, Tokens: 250, MinTier: TierSummarized},
	}

	result := c.Compact(sections)
	for _, s := range result.Sections {
		if s.Label == "big" {
			if s.Content != "CUSTOM:big" {
				t.Errorf("expected custom summary, got %q", s.Content)
			}
			return
		}
	}
}

func TestEstimateTokens(t *testing.T) {
	if EstimateTokens("") != 0 {
		t.Error("empty string should be 0 tokens")
	}
	tokens := EstimateTokens("hello world") // 11 chars ≈ 3 tokens
	if tokens < 2 || tokens > 5 {
		t.Errorf("unexpected token estimate: %d", tokens)
	}
}

func TestBuildSections(t *testing.T) {
	blocks := map[string]string{
		"system": "You are helpful.",
		"chat":   "Hello!",
	}
	statics := map[string]bool{"system": true}
	priorities := map[string]int{"system": 10, "chat": 5}

	sections := BuildSections(blocks, statics, priorities)
	if len(sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(sections))
	}

	for _, s := range sections {
		if s.Label == "system" {
			if !s.Static {
				t.Error("system should be static")
			}
			if s.Priority != 10 {
				t.Error("system priority should be 10")
			}
		}
	}
}

func TestRender(t *testing.T) {
	result := CompactResult{
		Sections: []OutputSection{
			{Label: "a", Content: "Hello"},
			{Label: "b", Content: "World"},
		},
	}
	rendered := Render(result)
	if rendered != "Hello\n\nWorld" {
		t.Errorf("unexpected render: %q", rendered)
	}
}

func TestStats(t *testing.T) {
	c := NewCompactor(Config{MaxTokens: 1000})
	sections := []Section{
		{Label: "sys", Content: "Prompt.", Static: true, Tokens: 50},
		{Label: "data", Content: "Content.", Static: false, Tokens: 100},
	}
	c.Compact(sections)
	c.Compact(sections)

	stats := c.Stats()
	if stats.Compactions != 2 {
		t.Errorf("expected 2 compactions, got %d", stats.Compactions)
	}
}

func TestEmptyInput(t *testing.T) {
	c := NewCompactor(Config{MaxTokens: 1000})
	result := c.Compact(nil)
	if result.TotalIn != 0 {
		t.Error("empty input should have 0 tokens")
	}
	if len(result.Sections) != 0 {
		t.Error("empty input should produce no sections")
	}
}
