package agentloop

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// TestPreEndTurnCheckFn verifies that the PreEndTurnCheckFn hook fires
// exactly once before end_turn is finalized. When the hook returns a
// non-empty string, the loop must NOT return from that turn — it must
// continue to a subsequent turn with the hook's message injected as
// user-role context.
//
// Spec: specs/descent-hardening.md item 3 (PRE_COMPLETION_GATE).
func TestPreEndTurnCheckFn(t *testing.T) {
	// Two-turn provider: first turn claims end_turn, second turn is
	// quiet. The hook fires on the first end_turn and forces a second
	// turn.
	mock := &mockProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "I'm done!"},
				},
				StopReason: "end_turn",
				Usage:      stream.TokenUsage{Input: 10, Output: 5},
			},
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "Ok, corrected."},
				},
				StopReason: "end_turn",
				Usage:      stream.TokenUsage{Input: 10, Output: 5},
			},
		},
	}

	var fired atomic.Int32
	loop := New(mock, Config{
		Model: "claude-sonnet-4-5",
		PreEndTurnCheckFn: func(messages []Message) string {
			n := fired.Add(1)
			if n == 1 {
				return "pre_completion_gate missing"
			}
			return ""
		},
	}, nil, nil)

	result, err := loop.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if result.Turns != 2 {
		t.Errorf("expected 2 turns (first forced to retry), got %d", result.Turns)
	}
	if fired.Load() != 2 {
		t.Errorf("expected hook to fire twice (once rejecting, once accepting), got %d", fired.Load())
	}
	if result.StopReason != "end_turn" {
		t.Errorf("expected end_turn, got %q", result.StopReason)
	}
}

// TestPreEndTurnCheckFn_AcceptFirstTry verifies that when the hook
// returns "" on the first end_turn, the loop exits immediately without
// forcing an extra turn.
func TestPreEndTurnCheckFn_AcceptFirstTry(t *testing.T) {
	mock := &mockProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "ok"},
				},
				StopReason: "end_turn",
				Usage:      stream.TokenUsage{Input: 10, Output: 5},
			},
		},
	}

	var fired atomic.Int32
	loop := New(mock, Config{
		Model: "claude-sonnet-4-5",
		PreEndTurnCheckFn: func(messages []Message) string {
			fired.Add(1)
			return ""
		},
	}, nil, nil)
	result, err := loop.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if fired.Load() != 1 {
		t.Errorf("expected hook to fire once, got %d", fired.Load())
	}
	if result.Turns != 1 {
		t.Errorf("expected 1 turn, got %d", result.Turns)
	}
}
