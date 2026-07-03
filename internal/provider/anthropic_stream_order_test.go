package provider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/stream"
)

// sseFrame formats a single Anthropic SSE frame.
func sseFrame(event, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// TestChatStreamPreservesBlockOrder scripts an SSE stream whose assistant
// content blocks interleave thinking -> tool_use -> text -> tool_use and
// asserts the reassembled ChatResponse.Content matches the wire order.
//
// The API replays assistant blocks verbatim on the next turn and requires
// extended-thinking + tool_use blocks in their original order. The old
// reassembly bucketed by type (all thinking hoisted to the front, all text
// merged into one block placed ahead of every tool_use), which would have
// produced [thinking, text, Read, Write] here. Wire order is
// [thinking, Read, text, Write].
func TestChatStreamPreservesBlockOrder(t *testing.T) {
	frames := []string{
		sseFrame("message_start", `{"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":0}}}`),

		// index 0: thinking
		sseFrame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		sseFrame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"weigh the options"}}`),
		sseFrame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}}`),
		sseFrame("content_block_stop", `{"type":"content_block_stop","index":0}`),

		// index 1: tool_use Read
		sseFrame("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_read","name":"Read","input":{}}}`),
		sseFrame("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/a\"}"}}`),
		sseFrame("content_block_stop", `{"type":"content_block_stop","index":1}`),

		// index 2: text
		sseFrame("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`),
		sseFrame("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"here is the "}}`),
		sseFrame("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"answer"}}`),
		sseFrame("content_block_stop", `{"type":"content_block_stop","index":2}`),

		// index 3: tool_use Write
		sseFrame("content_block_start", `{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"tu_write","name":"Write","input":{}}}`),
		sseFrame("content_block_delta", `{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/b\"}"}}`),
		sseFrame("content_block_stop", `{"type":"content_block_stop","index":3}`),

		sseFrame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}`),
		sseFrame("message_stop", `{"type":"message_stop"}`),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, f := range frames {
			_, _ = w.Write([]byte(f))
		}
	}))
	defer srv.Close()

	p := NewAnthropicProvider("test-key", srv.URL)
	resp, err := p.ChatStream(ChatRequest{
		Model:     "claude-opus-4-8",
		MaxTokens: 1000,
		Messages:  []ChatMessage{{Role: roleUser, Content: []byte(`"go"`)}},
	}, func(stream.Event) {})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	type want struct {
		typ  string
		name string // tool name, for tool_use
		body string // substring, for text
	}
	wants := []want{
		{typ: blockTypeThinking},
		{typ: blockTypeToolUse, name: "Read"},
		{typ: blockTypeText, body: "here is the answer"},
		{typ: blockTypeToolUse, name: "Write"},
	}

	if len(resp.Content) != len(wants) {
		t.Fatalf("got %d content blocks, want %d: %+v", len(resp.Content), len(wants), resp.Content)
	}
	for i, w := range wants {
		got := resp.Content[i]
		if got.Type != w.typ {
			t.Errorf("block %d: type=%q, want %q (full: %+v)", i, got.Type, w.typ, resp.Content)
			continue
		}
		switch w.typ {
		case blockTypeToolUse:
			if got.Name != w.name {
				t.Errorf("block %d: tool name=%q, want %q", i, got.Name, w.name)
			}
		case blockTypeText:
			if !strings.Contains(got.Text, w.body) {
				t.Errorf("block %d: text=%q, want contains %q", i, got.Text, w.body)
			}
		case blockTypeThinking:
			if got.Thinking == "" {
				t.Errorf("block %d: thinking text empty", i)
			}
			if got.Signature == "" {
				t.Errorf("block %d: thinking signature empty", i)
			}
		}
	}

	if resp.StopReason != stopReasonToolUse {
		t.Errorf("stop reason=%q, want %q", resp.StopReason, stopReasonToolUse)
	}
}
