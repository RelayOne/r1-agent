package agentloop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/provider"
)

// TestContentBlockMarshalEmptyThinking pins that a thinking block with
// empty text (the adaptive / display:"omitted" shape — signature only)
// still serializes the required `thinking` field, so the assistant turn
// round-trips without a 400 on the next request.
func TestContentBlockMarshalEmptyThinking(t *testing.T) {
	b := ContentBlock{Type: blockThinking, Thinking: "", Signature: "sig-abc"}
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["thinking"]; !ok {
		t.Errorf("thinking field must be present even when empty; got %s", out)
	}
	if got["signature"] != "sig-abc" {
		t.Errorf("signature must round-trip; got %s", out)
	}
	if got["type"] != blockThinking {
		t.Errorf("type must be thinking; got %s", out)
	}
	// A non-thinking block with empty text must stay minimal (no thinking key).
	txt, _ := json.Marshal(ContentBlock{Type: blockText, Text: "hi"})
	if strings.Contains(string(txt), "thinking") {
		t.Errorf("text block leaked a thinking field: %s", txt)
	}
}

func TestBuildRequestThinkingBudget(t *testing.T) {
	msgs := []Message{{Role: "user", Content: []ContentBlock{{Type: blockText, Text: "hi"}}}}

	t.Run("budget set", func(t *testing.T) {
		loop := New(&mockProvider{}, Config{Model: "claude-sonnet-4-5", ThinkingBudget: 4096}, nil, nil)
		req := loop.buildRequest(msgs)
		if req.Thinking == nil {
			t.Fatal("Thinking=nil, want spec with budget 4096")
		}
		if req.Thinking.BudgetTokens != 4096 {
			t.Errorf("BudgetTokens=%d, want 4096", req.Thinking.BudgetTokens)
		}
	})

	t.Run("zero budget disabled", func(t *testing.T) {
		loop := New(&mockProvider{}, Config{Model: "claude-sonnet-4-5"}, nil, nil)
		req := loop.buildRequest(msgs)
		if req.Thinking != nil {
			t.Errorf("Thinking=%+v, want nil when budget is 0", req.Thinking)
		}
	})

	t.Run("kill switch", func(t *testing.T) {
		t.Setenv("R1_DISABLE_THINKING", "1")
		loop := New(&mockProvider{}, Config{Model: "claude-sonnet-4-5", ThinkingBudget: 4096}, nil, nil)
		req := loop.buildRequest(msgs)
		if req.Thinking != nil {
			t.Errorf("Thinking=%+v, want nil under R1_DISABLE_THINKING=1", req.Thinking)
		}
	})
}

func TestThinkingRoundTrip(t *testing.T) {
	// With extended thinking + tool use the API requires the assistant
	// turn's thinking blocks (with signature) to be replayed verbatim
	// on the next request. Script a thinking → tool_use → end_turn
	// exchange and assert the SECOND request's history carries the
	// blocks in wire form, ordered before the tool_use.
	mock := &mockProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{Type: "thinking", Thinking: "plan the read", Signature: "sig1"},
					{Type: "redacted_thinking", Data: "opaque-bytes"},
					{Type: "tool_use", ID: "t1", Name: "echo",
						Input: map[string]interface{}{"s": "x"}},
				},
				StopReason: "tool_use",
			},
			{
				Content:    []provider.ResponseContent{{Type: "text", Text: "done"}},
				StopReason: "end_turn",
			},
		},
	}
	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return "ok", nil
	}

	loop := New(mock, Config{Model: "claude-sonnet-4-5", ThinkingBudget: 2048}, nil, handler)
	result, err := loop.Run(context.Background(), "do the thing")
	if err != nil {
		t.Fatal(err)
	}
	if len(mock.requests) != 2 {
		t.Fatalf("requests=%d, want 2", len(mock.requests))
	}

	// The second request's assistant message is the replayed turn.
	var assistant *provider.ChatMessage
	for i := range mock.requests[1].Messages {
		if mock.requests[1].Messages[i].Role == "assistant" {
			assistant = &mock.requests[1].Messages[i]
		}
	}
	if assistant == nil {
		t.Fatal("no assistant message in second request")
	}
	wire := string(assistant.Content)
	for _, want := range []string{
		`"type":"thinking"`,
		`"thinking":"plan the read"`,
		`"signature":"sig1"`,
		`"type":"redacted_thinking"`,
		`"data":"opaque-bytes"`,
	} {
		if !strings.Contains(wire, want) {
			t.Errorf("replayed assistant content missing %s\nwire: %s", want, wire)
		}
	}
	if ti, tu := strings.Index(wire, `"type":"thinking"`), strings.Index(wire, `"type":"tool_use"`); ti < 0 || tu < 0 || ti > tu {
		t.Errorf("thinking block must precede tool_use in the replayed turn\nwire: %s", wire)
	}

	// Thinking never leaks into the final visible text.
	if result.FinalText != "done" {
		t.Errorf("FinalText=%q, want 'done'", result.FinalText)
	}
}

func TestThinkingNotInFinalText(t *testing.T) {
	mock := &mockProvider{
		responses: []*provider.ChatResponse{{
			Content: []provider.ResponseContent{
				{Type: "thinking", Thinking: "internal reasoning", Signature: "s"},
				{Type: "text", Text: "the answer"},
			},
			StopReason: "end_turn",
		}},
	}
	loop := New(mock, Config{Model: "claude-sonnet-4-5", ThinkingBudget: 2048}, nil, nil)
	result, err := loop.Run(context.Background(), "question")
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "the answer" {
		t.Errorf("FinalText=%q, want 'the answer'", result.FinalText)
	}

	// History carries the thinking block in the typed fields, not Text.
	assistant := result.Messages[1]
	if assistant.Content[0].Type != "thinking" {
		t.Fatalf("first block type=%s, want thinking", assistant.Content[0].Type)
	}
	if assistant.Content[0].Thinking != "internal reasoning" || assistant.Content[0].Signature != "s" {
		t.Errorf("thinking block=%+v, want Thinking+Signature populated", assistant.Content[0])
	}
	if assistant.Content[0].Text != "" {
		t.Errorf("thinking block Text=%q, want empty (content lives in Thinking)", assistant.Content[0].Text)
	}
}
