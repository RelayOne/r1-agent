package stream

import (
	"testing"
)

func TestSSEParserBasicText(t *testing.T) {
	parser := NewSSEParser()

	// Simulate a message_start followed by content_block_delta with text
	chunk := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-6\",\"usage\":{\"input_tokens\":100,\"output_tokens\":0,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":50}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello world\"}}\n\n")

	events, err := parser.Push(chunk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// First event: message_start -> assistant with token usage
	if events[0].Type != "assistant" {
		t.Errorf("expected assistant, got %s", events[0].Type)
	}
	if events[0].Tokens.Input != 100 {
		t.Errorf("expected 100 input tokens, got %d", events[0].Tokens.Input)
	}
	if events[0].Tokens.CacheRead != 50 {
		t.Errorf("expected 50 cache read tokens, got %d", events[0].Tokens.CacheRead)
	}

	// Second event: text delta
	if events[1].DeltaText != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", events[1].DeltaText)
	}
}

func TestSSEParserToolUse(t *testing.T) {
	parser := NewSSEParser()

	// Anthropic streams a tool_use as: start (empty input) → input_json_delta
	// fragments → stop. The complete ToolUse with assembled input is emitted
	// on content_block_stop, not on start.
	chunks := [][]byte{
		[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu_1\",\"name\":\"Read\",\"input\":{}}}\n\n"),
		[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\"}}\n\n"),
		[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"/tmp/x.go\\\"}\"}}\n\n"),
		[]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"),
	}

	var allEvents []Event
	for _, c := range chunks {
		events, err := parser.Push(c)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		allEvents = append(allEvents, events...)
	}

	// Find the assistant event with the completed tool use.
	var toolEv *Event
	for i := range allEvents {
		if len(allEvents[i].ToolUses) > 0 {
			toolEv = &allEvents[i]
			break
		}
	}
	if toolEv == nil {
		t.Fatalf("expected an event with ToolUses, got %d events: %+v", len(allEvents), allEvents)
	}
	if toolEv.ToolUses[0].Name != "Read" {
		t.Errorf("expected Read, got %s", toolEv.ToolUses[0].Name)
	}
	if toolEv.ToolUses[0].ID != "tu_1" {
		t.Errorf("expected tu_1, got %s", toolEv.ToolUses[0].ID)
	}
	path, _ := toolEv.ToolUses[0].Input["path"].(string)
	if path != "/tmp/x.go" {
		t.Errorf("expected input.path=/tmp/x.go, got %q (full input: %+v)", path, toolEv.ToolUses[0].Input)
	}
}

func TestSSEParserError(t *testing.T) {
	parser := NewSSEParser()

	chunk := []byte("event: error\ndata: {\"error\":{\"type\":\"rate_limit_error\",\"message\":\"Too many requests\"}}\n\n")

	events, err := parser.Push(chunk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Subtype != "rate_limited" {
		t.Errorf("expected rate_limited, got %s", events[0].Subtype)
	}
	if !events[0].IsError {
		t.Error("expected IsError=true")
	}
}

func TestSSEParserPingIgnored(t *testing.T) {
	parser := NewSSEParser()

	chunk := []byte("event: ping\ndata: {}\n\n")

	events, err := parser.Push(chunk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events for ping, got %d", len(events))
	}
}

func TestSSEParserDone(t *testing.T) {
	parser := NewSSEParser()

	chunk := []byte("data: [DONE]\n\n")

	events, err := parser.Push(chunk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events for [DONE], got %d", len(events))
	}
}

func TestSSEParserPartialBuffer(t *testing.T) {
	parser := NewSSEParser()

	// First chunk: incomplete frame
	events, err := parser.Push([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"claude\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events from partial, got %d", len(events))
	}

	// Second chunk: completes the frame
	events, err = parser.Push([]byte(",\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0}}}\n\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Tokens.Input != 10 {
		t.Errorf("expected 10 input tokens, got %d", events[0].Tokens.Input)
	}
}

func TestSSEParserFinish(t *testing.T) {
	parser := NewSSEParser()

	// Push a partial frame without trailing \n\n
	parser.Push([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":42}}"))

	events, err := parser.Finish()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event from Finish, got %d", len(events))
	}
	if events[0].StopReason != "end_turn" {
		t.Errorf("expected end_turn, got %s", events[0].StopReason)
	}
}

func TestSSEParserMessageDelta(t *testing.T) {
	parser := NewSSEParser()

	chunk := []byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":250}}\n\n")

	events, err := parser.Push(chunk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].StopReason != "tool_use" {
		t.Errorf("expected tool_use, got %s", events[0].StopReason)
	}
	if events[0].Tokens.Output != 250 {
		t.Errorf("expected 250 output tokens, got %d", events[0].Tokens.Output)
	}
}
