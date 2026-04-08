package engine

import (
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/agentloop"
)

func TestBuildNativeCompactor_ShortHistoryUnchanged(t *testing.T) {
	fn := buildNativeCompactor(6, 200)
	msgs := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "do a thing"}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "text", Text: "ok"}}},
	}
	out := fn(msgs, 100)
	if len(out) != 2 {
		t.Errorf("short history should be unchanged, got %d messages", len(out))
	}
}

func TestBuildNativeCompactor_SummarizesMiddleToolResults(t *testing.T) {
	fn := buildNativeCompactor(2, 50)
	bigContent := strings.Repeat("x", 500)

	msgs := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "task brief"}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "tool_use", ID: "t1", Name: "read", Input: []byte("{}")}}},
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "tool_result", ToolUseID: "t1", Content: bigContent}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "tool_use", ID: "t2", Name: "write", Input: []byte("{}")}}},
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "tool_result", ToolUseID: "t2", Content: bigContent}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "text", Text: "done"}}},
	}

	out := fn(msgs, 10000)
	if len(out) != 6 {
		t.Fatalf("compactor should preserve message count, got %d", len(out))
	}
	// First message (task brief) must be verbatim
	if out[0].Content[0].Text != "task brief" {
		t.Errorf("task brief corrupted: %q", out[0].Content[0].Text)
	}
	// Middle tool_result should be summarized
	mid := out[2].Content[0]
	if mid.Type != "tool_result" {
		t.Errorf("middle should still be tool_result")
	}
	if len(mid.Content) >= 500 {
		t.Errorf("middle tool_result should be summarized, still %d bytes", len(mid.Content))
	}
	if !strings.Contains(mid.Content, "truncated") {
		t.Errorf("summary should mention truncation")
	}
	// Last 2 messages (keepRecent=2) should be verbatim
	last := out[5].Content[0]
	if last.Type != "text" || last.Text != "done" {
		t.Errorf("last message should be verbatim: %+v", last)
	}
}

func TestBuildNativeCompactor_PreservesToolUseToolResultIntegrity(t *testing.T) {
	// Critical: the API will 400 if a tool_use appears without a
	// matching tool_result. The compactor must not break pairs.
	fn := buildNativeCompactor(2, 30)
	msgs := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "brief"}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "tool_use", ID: "t1", Name: "x", Input: []byte("{}")}}},
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "tool_result", ToolUseID: "t1", Content: strings.Repeat("y", 200)}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "text", Text: "ok"}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "text", Text: "final"}}},
	}
	out := fn(msgs, 10000)

	// Count tool_use and tool_result — they should match
	toolUses, toolResults := 0, 0
	for _, m := range out {
		for _, c := range m.Content {
			if c.Type == "tool_use" {
				toolUses++
			}
			if c.Type == "tool_result" {
				toolResults++
			}
		}
	}
	if toolUses != toolResults {
		t.Errorf("tool_use/tool_result pair broken: uses=%d results=%d", toolUses, toolResults)
	}
}

func TestBuildNativeCompactor_DefaultsApply(t *testing.T) {
	// keepRecent=0 and summaryChars=0 should fall back to the documented defaults
	fn := buildNativeCompactor(0, 0)
	msgs := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "one"}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "text", Text: "two"}}},
	}
	out := fn(msgs, 0)
	if len(out) != 2 {
		t.Errorf("tiny history should pass through: %d", len(out))
	}
}

func TestBuildNativeCompactor_LongNarrationTruncated(t *testing.T) {
	fn := buildNativeCompactor(2, 50)
	longText := strings.Repeat("narration ", 200)
	msgs := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "brief"}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "text", Text: longText}}},
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "next"}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "text", Text: "end"}}},
	}
	out := fn(msgs, 10000)
	mid := out[1].Content[0]
	if len(mid.Text) >= len(longText) {
		t.Errorf("long narration should be truncated: still %d chars", len(mid.Text))
	}
}

func TestCompactionEnabled(t *testing.T) {
	if compactionEnabled(RunSpec{}) {
		t.Error("empty spec should not have compaction enabled")
	}
	if !compactionEnabled(RunSpec{CompactThreshold: 100000}) {
		t.Error("positive threshold should enable compaction")
	}
}

