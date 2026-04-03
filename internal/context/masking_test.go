package context

import (
	"fmt"
	"strings"
	"testing"
)

func TestMaskObservationsAgeBasedWindow(t *testing.T) {
	mgr := NewManager(Budget{MaxTokens: 100000, TargetUtil: 0.40, GentleThreshold: 0.50})

	// Add 15 tool outputs (window size 10 → first 5 should be masked)
	for i := 0; i < 15; i++ {
		mgr.Add(ContextBlock{
			Label:    "tool_output",
			Content:  fmt.Sprintf("output %d: " + strings.Repeat("x", 200), i),
			Tier:     TierSession,
			Priority: 3,
		})
	}

	cfg := DefaultMaskConfig()
	result := mgr.MaskObservations(cfg)

	if result.BlocksMasked != 5 {
		t.Errorf("expected 5 masked, got %d", result.BlocksMasked)
	}
	if result.TokensSaved <= 0 {
		t.Error("expected positive token savings")
	}

	// First 5 should be placeholders, last 10 should be original
	for i, b := range mgr.blocks {
		if i < 5 {
			if !strings.Contains(b.Content, "masked") {
				t.Errorf("block %d should be masked", i)
			}
		} else {
			if strings.Contains(b.Content, "masked") {
				t.Errorf("block %d should NOT be masked", i)
			}
		}
	}
}

func TestMaskObservationsSizeBasedMasking(t *testing.T) {
	mgr := NewManager(Budget{MaxTokens: 100000, TargetUtil: 0.40, GentleThreshold: 0.50})

	// Add one huge output within window
	mgr.Add(ContextBlock{
		Label:   "tool_output",
		Content: strings.Repeat("large content\n", 1000),
		Tier:    TierSession,
		Tokens:  5000, // over MaxOutputTokens
	})

	cfg := DefaultMaskConfig()
	cfg.MaxOutputTokens = 2000

	result := mgr.MaskObservations(cfg)
	if result.BlocksMasked != 1 {
		t.Errorf("expected 1 masked (size-based), got %d", result.BlocksMasked)
	}
}

func TestMaskObservationsPreservesLabels(t *testing.T) {
	mgr := NewManager(Budget{MaxTokens: 100000, TargetUtil: 0.40, GentleThreshold: 0.50})

	// Add system block (should be preserved)
	mgr.Add(ContextBlock{Label: "system", Content: "system prompt", Tier: TierProject})
	// Add old tool output
	mgr.Add(ContextBlock{Label: "tool_output", Content: "old output", Tier: TierSession})

	cfg := DefaultMaskConfig()
	cfg.WindowSize = 0 // mask everything outside window

	mgr.MaskObservations(cfg)

	if strings.Contains(mgr.blocks[0].Content, "masked") {
		t.Error("system block should be preserved")
	}
}

func TestMaskObservationsPreservesActiveTier(t *testing.T) {
	mgr := NewManager(Budget{MaxTokens: 100000, TargetUtil: 0.40, GentleThreshold: 0.50})

	mgr.Add(ContextBlock{Label: "tool_output", Content: "current turn output", Tier: TierActive})

	cfg := DefaultMaskConfig()
	cfg.WindowSize = 0

	result := mgr.MaskObservations(cfg)
	if result.BlocksMasked != 0 {
		t.Error("active tier should not be masked")
	}
}

func TestCompactWithMasking(t *testing.T) {
	mgr := NewManager(Budget{
		MaxTokens:        1000,
		TargetUtil:       0.40,
		GentleThreshold:  0.50,
		ModerateThresh:   0.65,
		AggressiveThresh: 0.80,
	})

	// Fill context beyond gentle threshold
	for i := 0; i < 10; i++ {
		mgr.Add(ContextBlock{
			Label:    "tool_output",
			Content:  strings.Repeat("data ", 100),
			Tier:     TierSession,
			Priority: 2,
		})
	}

	cfg := DefaultMaskConfig()
	level, maskResult := mgr.CompactWithMasking(cfg)

	// Should have masked some blocks
	if maskResult.BlocksMasked == 0 && level == "none" {
		t.Error("expected either masking or compaction to occur")
	}
	if maskResult.TokensBefore <= maskResult.TokensAfter && level == "none" {
		t.Error("expected token reduction")
	}
}

func TestMaskPlaceholder(t *testing.T) {
	b := &ContextBlock{
		Label:   "file_read",
		Content: "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n",
		Tokens:  50,
	}
	placeholder := maskPlaceholder(b)
	if !strings.Contains(placeholder, "file_read") {
		t.Error("placeholder should include label")
	}
	if !strings.Contains(placeholder, "masked") {
		t.Error("placeholder should say masked")
	}
	if !strings.Contains(placeholder, "package main") {
		t.Error("placeholder should include brief summary")
	}
}

func TestExtractBrief(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello world", "hello world"},
		{"", "content"},
		{"\n\nhello", "hello"},
		{strings.Repeat("a", 100), strings.Repeat("a", 77) + "..."},
	}
	for _, tc := range tests {
		got := extractBrief(tc.input)
		if got != tc.want {
			t.Errorf("extractBrief(%q) = %q, want %q", tc.input[:min(len(tc.input), 20)], got, tc.want)
		}
	}
}

func TestBlockStats(t *testing.T) {
	mgr := NewManager(DefaultBudget())
	mgr.Add(ContextBlock{Label: "system", Content: "sys", Tokens: 100})
	mgr.Add(ContextBlock{Label: "tool_output", Content: "out1", Tokens: 200})
	mgr.Add(ContextBlock{Label: "tool_output", Content: "out2", Tokens: 300})

	stats := mgr.BlockStats()
	if stats["system"] != 100 {
		t.Errorf("expected 100 system tokens, got %d", stats["system"])
	}
	if stats["tool_output"] != 500 {
		t.Errorf("expected 500 tool_output tokens, got %d", stats["tool_output"])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
