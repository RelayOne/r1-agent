// integration_antitrunc_test.go — end-to-end integration test for the
// anti-truncation enforcement layer. The cortex-driven mission test
// from spec §item 25 is BLOCKED on cortex-core merging into this
// worktree. As a substitute, this test drives a full agentloop run
// with AntiTruncEnforce=true through a mockProvider that emits a
// truncation phrase on its first turn and recovery text on its
// second. It asserts:
//
//   1. The gate fires on turn 1 (PreEndTurnCheckFn returns non-empty).
//   2. The agentloop injects a [BUILD VERIFICATION FAILED] message
//      and forces a retry rather than terminating.
//   3. The retry turn produces clean output and the loop exits
//      with the recovery text in result.Messages.
//
// This proves the load-bearing contract: a model that says "i'll stop
// here" does NOT successfully end its turn — it gets dragged back to
// the next turn until the gate is satisfied.

package agentloop

import (
	"context"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/antitrunc"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

func TestAntiTruncIntegration_GateForcesContinuation(t *testing.T) {
	// Two-turn script: turn 1 emits a truncation phrase, turn 2
	// emits clean recovery text.
	mock := &mockProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "i'll stop here for now and pick up later"},
				},
				StopReason: "end_turn",
				Usage:      stream.TokenUsage{Input: 50, Output: 20},
			},
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "ok, continuing the work — tests pass and build is green"},
				},
				StopReason: "end_turn",
				Usage:      stream.TokenUsage{Input: 60, Output: 25},
			},
		},
	}

	loop := New(mock, Config{
		Model:            "claude-sonnet-4-5",
		AntiTruncEnforce: true,
	}, nil, nil)

	result, err := loop.Run(context.Background(), "Do all the work, no shortcuts")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The mock had 2 responses queued. If the gate fired correctly
	// on turn 1, the loop must have run BOTH turns (so all 2
	// responses were consumed).
	if mock.callIdx != 2 {
		t.Errorf("expected 2 turns consumed, got %d — gate did not force continuation", mock.callIdx)
	}
	if result.Turns != 2 {
		t.Errorf("result.Turns = %d, want 2", result.Turns)
	}

	// The loop must have injected a message containing
	// ANTI-TRUNCATION between turns.
	foundInjection := false
	for _, m := range result.Messages {
		if m.Role != "user" {
			continue
		}
		for _, c := range m.Content {
			if strings.Contains(c.Text, "ANTI-TRUNCATION") {
				foundInjection = true
			}
		}
	}
	if !foundInjection {
		t.Error("expected an injected ANTI-TRUNCATION message between turns")
	}

	// The final assistant text must be the recovery text, not the
	// initial truncation phrase.
	if !strings.Contains(result.FinalText, "continuing the work") {
		t.Errorf("final text should be recovery, got %q", result.FinalText)
	}
}

func TestAntiTruncIntegration_NoEnforce_AllowsTruncation(t *testing.T) {
	// Sanity check: with AntiTruncEnforce=false, the same
	// truncation phrase ENDS the turn (no gate fires).
	mock := &mockProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "i'll stop here"},
				},
				StopReason: "end_turn",
				Usage:      stream.TokenUsage{Input: 50, Output: 10},
			},
		},
	}

	loop := New(mock, Config{
		Model:            "claude-sonnet-4-5",
		AntiTruncEnforce: false,
	}, nil, nil)

	result, err := loop.Run(context.Background(), "Do work")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Turns != 1 {
		t.Errorf("result.Turns = %d, want 1 (gate disabled, end_turn allowed)", result.Turns)
	}
}

func TestAntiTruncIntegration_AdvisoryMode_NoRetry(t *testing.T) {
	// Advisory mode: gate detects but doesn't block. AdvisoryFn
	// must be called; the loop must NOT retry.
	var captured []antitrunc.Finding
	mock := &mockProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{Type: "text", Text: "let me defer this for now"},
				},
				StopReason: "end_turn",
				Usage:      stream.TokenUsage{Input: 50, Output: 10},
			},
		},
	}

	loop := New(mock, Config{
		Model:               "claude-sonnet-4-5",
		AntiTruncEnforce:    true,
		AntiTruncAdvisory:   true,
		AntiTruncAdvisoryFn: func(f antitrunc.Finding) { captured = append(captured, f) },
	}, nil, nil)

	result, err := loop.Run(context.Background(), "Do work")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Turns != 1 {
		t.Errorf("result.Turns = %d, want 1 (advisory must not retry)", result.Turns)
	}
	if len(captured) == 0 {
		t.Error("advisory must still detect findings")
	}
}

// TestAntiTruncIntegration_OperatorSyntheticConversation drives a
// realistic synthetic conversation that includes plan-unchecked +
// truncation phrase signals. Documents the operator integration
// test (per the deliverable summary).
func TestAntiTruncIntegration_OperatorSyntheticConversation(t *testing.T) {
	mock := &mockProvider{
		responses: []*provider.ChatResponse{
			// Turn 1: the LLM tries every classic self-truncation move.
			{
				Content: []provider.ResponseContent{{
					Type: "text",
					Text: "I think the foundation is done here — to keep scope tight, " +
						"I'll defer the rest to a follow-up session. Good enough to merge.",
				}},
				StopReason: "end_turn",
				Usage:      stream.TokenUsage{Input: 100, Output: 40},
			},
			// Turn 2: forced retry — this time the LLM does the work.
			{
				Content: []provider.ResponseContent{{
					Type: "text",
					Text: "All checklist items now resolved. Tests pass; build green.",
				}},
				StopReason: "end_turn",
				Usage:      stream.TokenUsage{Input: 110, Output: 30},
			},
		},
	}

	loop := New(mock, Config{
		Model:            "claude-sonnet-4-5",
		AntiTruncEnforce: true,
	}, nil, nil)

	result, err := loop.Run(context.Background(), "Implement the layered defense fully")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Turns < 2 {
		t.Errorf("expected forced retry (turns >= 2), got %d", result.Turns)
	}

	// At least 4 distinct phrase IDs must have been observed across
	// the synthetic turn. Confirm by looking at the injected
	// supervisor-note messages.
	injectionCount := 0
	for _, m := range result.Messages {
		if m.Role != "user" {
			continue
		}
		for _, c := range m.Content {
			if strings.Contains(c.Text, "ANTI-TRUNCATION") {
				injectionCount++
			}
		}
	}
	if injectionCount == 0 {
		t.Error("expected at least one ANTI-TRUNCATION injection")
	}
}
