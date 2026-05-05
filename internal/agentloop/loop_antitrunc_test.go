package agentloop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/antitrunc"
)

// TestAntiTrunc_GateFires_RefusesEndTurn covers the load-bearing
// path: AntiTruncEnforce=true + truncation phrase in the latest
// assistant turn => PreEndTurnCheckFn returns the [ANTI-TRUNCATION]
// refusal so the agentloop forces another turn.
func TestAntiTrunc_GateFires_RefusesEndTurn(t *testing.T) {
	cfg := &Config{
		AntiTruncEnforce: true,
	}
	cfg.defaults()
	if cfg.PreEndTurnCheckFn == nil {
		t.Fatal("expected PreEndTurnCheckFn to be installed")
	}

	got := cfg.PreEndTurnCheckFn([]Message{
		{Role: "user", Content: []ContentBlock{{Type: blockText, Text: "do everything"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: blockText, Text: "i'll stop here"}}},
	})
	if got == "" {
		t.Fatal("expected gate to refuse on truncation phrase")
	}
	if !strings.Contains(got, "[ANTI-TRUNCATION]") {
		t.Errorf("missing [ANTI-TRUNCATION] prefix: %s", got)
	}
}

// TestAntiTrunc_CleanTurn_AllowsEndTurn covers the negative path:
// no truncation phrase, no plan -> the gate returns "" and the
// loop is free to end_turn.
func TestAntiTrunc_CleanTurn_AllowsEndTurn(t *testing.T) {
	cfg := &Config{AntiTruncEnforce: true}
	cfg.defaults()
	got := cfg.PreEndTurnCheckFn([]Message{
		{Role: "assistant", Content: []ContentBlock{{Type: blockText, Text: "tests pass; build green"}}},
	})
	if got != "" {
		t.Errorf("expected clean gate, got: %s", got)
	}
}

// TestAntiTrunc_NotEnforced_NoGate covers the disabled path:
// AntiTruncEnforce=false leaves PreEndTurnCheckFn nil.
func TestAntiTrunc_NotEnforced_NoGate(t *testing.T) {
	cfg := &Config{}
	cfg.defaults()
	if cfg.PreEndTurnCheckFn != nil {
		t.Error("expected PreEndTurnCheckFn=nil when AntiTruncEnforce=false")
	}
}

// TestAntiTrunc_ComposesWithUserHook covers the composition contract:
// when the gate is silent, the user-supplied hook still runs. When
// the gate fires, the user hook MUST NOT run (load-bearing).
func TestAntiTrunc_ComposesWithUserHook(t *testing.T) {
	userCalled := 0
	userMsg := "user hook fired: build broken"
	cfg := &Config{
		AntiTruncEnforce: true,
		PreEndTurnCheckFn: func(messages []Message) string {
			userCalled++
			return userMsg
		},
	}
	cfg.defaults()

	// Case A: gate fires -> user hook NOT called.
	got := cfg.PreEndTurnCheckFn([]Message{
		{Role: "assistant", Content: []ContentBlock{{Type: blockText, Text: "i'll defer"}}},
	})
	if got == "" {
		t.Fatal("gate should have fired")
	}
	if strings.Contains(got, userMsg) {
		t.Errorf("gate fire returned user-hook message; user hook leaked: %s", got)
	}
	if userCalled != 0 {
		t.Errorf("user hook called despite gate fire: count=%d", userCalled)
	}

	// Case B: gate silent -> user hook IS called.
	got = cfg.PreEndTurnCheckFn([]Message{
		{Role: "assistant", Content: []ContentBlock{{Type: blockText, Text: "ok done"}}},
	})
	if got != userMsg {
		t.Errorf("expected user hook return %q, got %q", userMsg, got)
	}
	if userCalled != 1 {
		t.Errorf("user hook call count = %d, want 1", userCalled)
	}
}

// TestAntiTrunc_PlanPath_Composes covers the plan signal end-to-end
// from agentloop perspective: a config with AntiTruncPlanPath
// pointing at a partial plan refuses end_turn even on a clean
// assistant turn.
func TestAntiTrunc_PlanPath_Composes(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(plan, []byte("- [x] one\n- [ ] two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		AntiTruncEnforce:  true,
		AntiTruncPlanPath: plan,
	}
	cfg.defaults()
	got := cfg.PreEndTurnCheckFn([]Message{
		{Role: "assistant", Content: []ContentBlock{{Type: blockText, Text: "build green"}}},
	})
	if got == "" {
		t.Fatal("expected gate to refuse on partial plan")
	}
	if !strings.Contains(got, "1/2 plan items unchecked") {
		t.Errorf("missing detail: %s", got)
	}
}

// TestAntiTrunc_AdvisoryMode_DoesNotBlock covers the operator
// override: AntiTruncAdvisory=true forwards findings to AdvisoryFn
// but the wrapper returns "" so the loop is not blocked.
func TestAntiTrunc_AdvisoryMode_DoesNotBlock(t *testing.T) {
	var captured []antitrunc.Finding
	cfg := &Config{
		AntiTruncEnforce:    true,
		AntiTruncAdvisory:   true,
		AntiTruncAdvisoryFn: func(f antitrunc.Finding) { captured = append(captured, f) },
	}
	cfg.defaults()
	got := cfg.PreEndTurnCheckFn([]Message{
		{Role: "assistant", Content: []ContentBlock{{Type: blockText, Text: "i'll stop here"}}},
	})
	if got != "" {
		t.Errorf("advisory mode must not block, got: %s", got)
	}
	if len(captured) == 0 {
		t.Error("advisory mode must still detect and forward findings")
	}
}

// TestMessagesToAntiTrunc verifies the conversion helper preserves
// role + text and skips empty/tool-only blocks.
func TestMessagesToAntiTrunc(t *testing.T) {
	in := []Message{
		{Role: "user", Content: []ContentBlock{{Type: blockText, Text: "hi"}}},
		{Role: "assistant", Content: []ContentBlock{
			{Type: blockToolUse, Name: "x"},
			{Type: blockText, Text: "thinking out loud"},
		}},
		{Role: "assistant", Content: []ContentBlock{{Type: blockToolUse, Name: "x"}}}, // tool-only
	}
	out := messagesToAntiTrunc(in)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (skipped tool-only message)", len(out))
	}
	if out[0].Role != "user" || out[0].Text != "hi" {
		t.Errorf("user msg mismatch: %+v", out[0])
	}
	if out[1].Role != "assistant" || out[1].Text == "" {
		t.Errorf("assistant msg mismatch: %+v", out[1])
	}
}
