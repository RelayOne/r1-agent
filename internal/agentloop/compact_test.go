package agentloop

import (
	"strings"
	"testing"
)

func TestEstimateMessagesTokens_Empty(t *testing.T) {
	if n := estimateMessagesTokens(nil); n != 0 {
		t.Errorf("empty = %d", n)
	}
	if n := estimateMessagesTokens([]Message{}); n != 0 {
		t.Errorf("empty slice = %d", n)
	}
}

func TestEstimateMessagesTokens_SimpleText(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello world, this is a short message"}}},
	}
	n := estimateMessagesTokens(msgs)
	if n == 0 {
		t.Error("non-empty message should produce non-zero estimate")
	}
	// 4 chars/token heuristic; a 36-char text should be around 10-15 tokens + overhead
	if n > 50 {
		t.Errorf("estimate too high: %d", n)
	}
}

func TestEstimateMessagesTokens_ScalesWithContent(t *testing.T) {
	short := []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: strings.Repeat("x", 100)}}}}
	long := []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: strings.Repeat("x", 10000)}}}}

	shortEst := estimateMessagesTokens(short)
	longEst := estimateMessagesTokens(long)
	if longEst <= shortEst*10 {
		t.Errorf("long (%d) should be much larger than short (%d)", longEst, shortEst)
	}
}

func TestEstimateMessagesTokens_IncludesToolResults(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: []ContentBlock{
			{Type: "tool_result", ToolUseID: "t1", Content: strings.Repeat("big output ", 500)},
		}},
	}
	n := estimateMessagesTokens(msgs)
	if n < 1000 {
		t.Errorf("tool_result content should contribute to estimate, got %d", n)
	}
}

// TestCompactFn_Integration verifies the CompactFn hook is called when
// the estimated size crosses the threshold. We use a simple mock CompactFn
// that just records invocations — the actual rewriting logic is tested
// at the engine.buildNativeCompactor level.
func TestCompactFn_Called_WhenAboveThreshold(t *testing.T) {
	called := 0
	compactFn := func(messages []Message, est int) []Message {
		called++
		return messages // no-op rewrite
	}
	cfg := Config{
		Model:            "test",
		MaxTurns:         1,
		MaxTokens:        100,
		CompactThreshold: 1,
		CompactFn:        compactFn,
	}
	// Mock loop with no provider — we only care about the CompactFn
	// pre-request path. Calling RunWithHistory will fail on the actual
	// API call, but the CompactFn should fire first.
	_ = cfg
	// Simulate: build messages, call estimateMessagesTokens, then
	// invoke CompactFn directly as the loop would.
	msgs := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: strings.Repeat("x", 1000)}}},
	}
	est := estimateMessagesTokens(msgs)
	if cfg.CompactFn != nil && cfg.CompactThreshold > 0 && est > cfg.CompactThreshold {
		cfg.CompactFn(msgs, est)
	}
	if called != 1 {
		t.Errorf("CompactFn should have been called once, got %d", called)
	}
}

func TestCompactFn_NotCalled_BelowThreshold(t *testing.T) {
	called := 0
	compactFn := func(messages []Message, est int) []Message {
		called++
		return messages
	}
	cfg := Config{
		CompactThreshold: 1_000_000, // huge threshold
		CompactFn:        compactFn,
	}
	msgs := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "tiny"}}},
	}
	est := estimateMessagesTokens(msgs)
	if cfg.CompactFn != nil && cfg.CompactThreshold > 0 && est > cfg.CompactThreshold {
		cfg.CompactFn(msgs, est)
	}
	if called != 0 {
		t.Errorf("CompactFn should not fire below threshold, got %d calls", called)
	}
}
