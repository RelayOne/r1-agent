package engine

import (
	"testing"

	"github.com/RelayOne/r1/internal/agentloop"
)

// assistantWithTools builds an assistant message carrying n tool_use blocks.
func assistantWithTools(n int) agentloop.Message {
	m := agentloop.Message{Role: "assistant"}
	for i := 0; i < n; i++ {
		m.Content = append(m.Content, agentloop.ContentBlock{Type: "tool_use", Name: "write_file"})
	}
	return m
}

func TestCountToolUseBlocks(t *testing.T) {
	msgs := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "go"}}},
		assistantWithTools(2),
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "tool_result"}}},
		assistantWithTools(1),
	}
	// tool_use blocks only count on assistant messages: 2 + 1 = 3.
	if got := countToolUseBlocks(msgs); got != 3 {
		t.Fatalf("countToolUseBlocks = %d, want 3", got)
	}
}

// TestPreEndTurnGateReverifiesUntilGreen is the regression for the once-per-
// dispatch latch: the build gate must re-run on subsequent end_turn attempts
// so a still-broken build is NOT accepted after the model has been told to fix
// it. It also must NOT re-run when the model performed no new tool activity.
func TestPreEndTurnGateReverifiesUntilGreen(t *testing.T) {
	var gateRuns int
	buildBroken := true
	runGate := func() string {
		gateRuns++
		if buildBroken {
			return "Build command failed: go build\n\nErrors:\nundefined: Foo"
		}
		return ""
	}

	gate := buildPreEndTurnGate(runGate, nil)

	// Turn 1: model wrote a file then declared done, build is broken.
	msgs := []agentloop.Message{assistantWithTools(1)}
	if got := gate(msgs); got == "" {
		t.Fatal("first end_turn: broken build must block, got empty")
	}
	if gateRuns != 1 {
		t.Fatalf("expected 1 gate run, got %d", gateRuns)
	}

	// The model redeclares done WITHOUT editing anything (same tool count).
	// The gate must stay blocked but must NOT recompile.
	if got := gate(msgs); got == "" {
		t.Fatal("re-declared done with no edits: must still block")
	}
	if gateRuns != 1 {
		t.Fatalf("gate recompiled with no tool activity: runs=%d, want 1", gateRuns)
	}

	// The model makes a fix (a new tool_use) and declares done again. The
	// gate MUST re-run — the old latch would have skipped it and accepted a
	// broken build. This time the fix worked.
	buildBroken = false
	msgs = append(msgs, assistantWithTools(1))
	if got := gate(msgs); got != "" {
		t.Fatalf("after fix the build passes, gate should accept end_turn, got %q", got)
	}
	if gateRuns != 2 {
		t.Fatalf("expected gate to re-run after new tool activity, runs=%d, want 2", gateRuns)
	}
}

// TestPreEndTurnGateBrokenBuildOverridesExtraCheck ensures the extra completion
// gate only runs after the build is green, and that a broken build on the
// second attempt still blocks even if the extra check would have passed.
func TestPreEndTurnGateBrokenBuildOverridesExtraCheck(t *testing.T) {
	buildBroken := true
	runGate := func() string {
		if buildBroken {
			return "build error"
		}
		return ""
	}
	extraCalled := 0
	extra := func(finalText string) (bool, string) {
		extraCalled++
		return false, "" // extra check always passes
	}

	gate := buildPreEndTurnGate(runGate, extra)

	msgs := []agentloop.Message{assistantWithTools(1)}
	if got := gate(msgs); got != "build error" {
		t.Fatalf("broken build must return build error, got %q", got)
	}
	if extraCalled != 0 {
		t.Fatalf("extra check must not run while build is broken, called=%d", extraCalled)
	}

	// Fix + new activity → build green → extra check now runs.
	buildBroken = false
	msgs = append(msgs, assistantWithTools(1))
	if got := gate(msgs); got != "" {
		t.Fatalf("green build + passing extra check should accept, got %q", got)
	}
	if extraCalled != 1 {
		t.Fatalf("extra check should run once after build green, called=%d", extraCalled)
	}
}
