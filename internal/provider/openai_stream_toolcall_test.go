package provider

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RelayOne/r1/internal/stream"
)

// TestOpenAICompatStreamSparseToolIndices proves the streaming tool-call
// reassembler keys by the provider's tool_call index rather than slice
// position. Some OpenAI-compatible backends emit sparse / non-zero-based
// indices; the old `for i := 0; i < len(map); i++` loop read the wrong keys
// (index 0 was nil and skipped, the high index was never visited), silently
// dropping tool calls. Here indices are 1 and 3 (non-contiguous, non-zero
// start) with arguments split across frames.
func TestOpenAICompatStreamSparseToolIndices(t *testing.T) {
	frames := []string{
		// index 1 opens with its id+name and the first half of its arguments.
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_one","function":{"name":"search","arguments":"{\"q\":"}}]}}]}` + "\n\n",
		// index 3 opens.
		`data: {"choices":[{"delta":{"tool_calls":[{"index":3,"id":"call_two","function":{"name":"write","arguments":"{\"path\":"}}]}}]}` + "\n\n",
		// second half of index 1 arguments (accumulation across frames).
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"cats\"}"}}]}}]}` + "\n\n",
		// second half of index 3 arguments.
		`data: {"choices":[{"delta":{"tool_calls":[{"index":3,"function":{"arguments":"\"/b\"}"}}]}}]}` + "\n\n",
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":9}}` + "\n\n",
		"data: [DONE]\n\n",
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, f := range frames {
			_, _ = w.Write([]byte(f))
		}
	}))
	defer srv.Close()

	p := NewOpenAICompatProvider("test", "key", srv.URL)
	resp, err := p.ChatStream(ChatRequest{
		Model:     "gpt-4.1",
		MaxTokens: 100,
		Messages:  []ChatMessage{{Role: roleUser, Content: []byte(`"go"`)}},
	}, func(stream.Event) {})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var tools []ResponseContent
	for _, c := range resp.Content {
		if c.Type == blockTypeToolUse {
			tools = append(tools, c)
		}
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tool_use blocks, want 2 (sparse indices dropped one): %+v", len(tools), resp.Content)
	}

	// Ascending provider-index order: index 1 (search) then index 3 (write).
	if tools[0].Name != "search" || tools[0].ID != "call_one" {
		t.Errorf("tool[0] = %q/%q, want search/call_one", tools[0].Name, tools[0].ID)
	}
	if tools[1].Name != "write" || tools[1].ID != "call_two" {
		t.Errorf("tool[1] = %q/%q, want write/call_two", tools[1].Name, tools[1].ID)
	}
	// Arguments accumulated across frames and parsed into input.
	if q, _ := tools[0].Input["q"].(string); q != "cats" {
		t.Errorf("tool[0] input q = %q, want cats (args not accumulated): %+v", q, tools[0].Input)
	}
	if path, _ := tools[1].Input["path"].(string); path != "/b" {
		t.Errorf("tool[1] input path = %q, want /b: %+v", path, tools[1].Input)
	}
	if resp.StopReason != stopReasonToolUse {
		t.Errorf("stop reason = %q, want %q", resp.StopReason, stopReasonToolUse)
	}
}
