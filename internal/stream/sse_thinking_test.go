package stream

import (
	"strings"
	"testing"
)

// pushAll feeds every chunk to the parser and returns the flattened
// event list.
func pushAll(t *testing.T, parser *SSEParser, chunks []string) []Event {
	t.Helper()
	var all []Event
	for _, c := range chunks {
		events, err := parser.Push([]byte(c))
		if err != nil {
			t.Fatalf("Push(%q): %v", c, err)
		}
		all = append(all, events...)
	}
	return all
}

func TestSSEParserThinkingBlocks(t *testing.T) {
	parser := NewSSEParser()

	// Anthropic streams thinking as: start (empty) → thinking_delta
	// chunks → signature_delta → stop. The assembled block is emitted
	// on content_block_stop, like tool_use.
	chunks := []string{
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"Let me \"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"think\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"EqQB\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
	}
	all := pushAll(t, parser, chunks)

	var assembled *ThinkingBlock
	for i := range all {
		// Thinking text must never surface as DeltaText — that field
		// feeds user-visible prose accumulation.
		if strings.Contains(all[i].DeltaText, "Let me") {
			t.Errorf("thinking text leaked into DeltaText: %q", all[i].DeltaText)
		}
		if len(all[i].ThinkingBlocks) > 0 {
			if assembled != nil {
				t.Fatal("thinking block emitted more than once")
			}
			assembled = &all[i].ThinkingBlocks[0]
		}
	}
	if assembled == nil {
		t.Fatalf("no event carried ThinkingBlocks (got %d events)", len(all))
	}
	if assembled.Text != "Let me think" {
		t.Errorf("Text=%q, want 'Let me think'", assembled.Text)
	}
	if assembled.Signature != "EqQB" {
		t.Errorf("Signature=%q, want EqQB", assembled.Signature)
	}
	if assembled.Redacted {
		t.Error("Redacted=true for a plain thinking block")
	}
}

func TestSSEParserRedactedThinking(t *testing.T) {
	parser := NewSSEParser()

	// Redacted blocks arrive complete: opaque data on start, no deltas.
	chunks := []string{
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"EmwKAhgB\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
	}
	all := pushAll(t, parser, chunks)

	var assembled *ThinkingBlock
	for i := range all {
		if len(all[i].ThinkingBlocks) > 0 {
			assembled = &all[i].ThinkingBlocks[0]
		}
	}
	if assembled == nil {
		t.Fatal("no event carried ThinkingBlocks")
	}
	if !assembled.Redacted {
		t.Error("Redacted=false, want true")
	}
	if assembled.Data != "EmwKAhgB" {
		t.Errorf("Data=%q, want EmwKAhgB", assembled.Data)
	}
}

func TestSSEParserThinkingInterleavedWithTextAndTools(t *testing.T) {
	parser := NewSSEParser()

	// Thinking at index 0, text at index 1, tool_use at index 2 —
	// the standard extended-thinking + tool-use response shape.
	chunks := []string{
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"plan\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Reading now.\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu_1\",\"name\":\"Read\",\"input\":{}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"/x\\\"}\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":2}\n\n",
	}
	all := pushAll(t, parser, chunks)

	var thinking *ThinkingBlock
	var tool *ToolUse
	textDelta := ""
	for i := range all {
		if len(all[i].ThinkingBlocks) > 0 {
			thinking = &all[i].ThinkingBlocks[0]
		}
		if len(all[i].ToolUses) > 0 {
			tool = &all[i].ToolUses[0]
		}
		if all[i].DeltaType == "text_delta" {
			textDelta += all[i].DeltaText
		}
	}
	if thinking == nil || thinking.Text != "plan" || thinking.Signature != "sig" {
		t.Errorf("thinking=%+v, want Text=plan Signature=sig", thinking)
	}
	if textDelta != "Reading now." {
		t.Errorf("text deltas=%q, want 'Reading now.'", textDelta)
	}
	if tool == nil || tool.Name != "Read" || tool.ID != "tu_1" {
		t.Errorf("tool=%+v, want Read/tu_1", tool)
	}
	if tool != nil {
		if path, _ := tool.Input["path"].(string); path != "/x" {
			t.Errorf("tool input path=%q, want /x", path)
		}
	}
}
