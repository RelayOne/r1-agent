package main

import (
	"strings"
	"testing"
)

func TestNormalizeSowRunnerMode_NoNativeFlagsPassesThrough(t *testing.T) {
	// Without native flags, default "claude" mode must stay "claude".
	// This is the path users who don't want native take. Mutating it
	// silently would break their Claude-CLI setups.
	in := sowRunnerState{
		RunnerMode:     "claude",
		NativeAPIKey:   "",
		NativeBaseURL:  "",
		ReviewerSource: "codex",
	}
	out, messages := normalizeSowRunnerMode(in)
	if out.RunnerMode != "claude" {
		t.Fatalf("runner mode must not be upgraded without native flags: got %q", out.RunnerMode)
	}
	if out.ReviewerSource != "codex" {
		t.Fatalf("reviewer source must not be remapped without native flags: got %q", out.ReviewerSource)
	}
	if len(messages) != 0 {
		t.Fatalf("no messages expected in passthrough path: got %v", messages)
	}
}

func TestNormalizeSowRunnerMode_OnlyAPIKeyInsufficient(t *testing.T) {
	// Both --native-api-key AND --native-base-url must be set before
	// the upgrade fires. Key-only is a common footgun (operator
	// exported ANTHROPIC_API_KEY but didn't point at a proxy); we
	// must not silently rewrite runnerMode in that case.
	in := sowRunnerState{
		RunnerMode:    "claude",
		NativeAPIKey:  "sk-test",
		NativeBaseURL: "",
	}
	out, messages := normalizeSowRunnerMode(in)
	if out.RunnerMode != "claude" {
		t.Fatalf("runner must not upgrade with only api key: got %q", out.RunnerMode)
	}
	if len(messages) != 0 {
		t.Fatalf("no messages expected: got %v", messages)
	}
}

func TestNormalizeSowRunnerMode_OnlyBaseURLInsufficient(t *testing.T) {
	// Symmetric: URL-without-key must also not trigger the upgrade,
	// because the downstream native fast path will fatal at runtime
	// "SOW fast path requires a native API key" (main.go:2176).
	// Upgrading in this state would convert a clear error into a
	// confusing one at a later stage.
	in := sowRunnerState{
		RunnerMode:    "claude",
		NativeAPIKey:  "",
		NativeBaseURL: "http://localhost:4000",
	}
	out, messages := normalizeSowRunnerMode(in)
	if out.RunnerMode != "claude" {
		t.Fatalf("runner must not upgrade with only base url: got %q", out.RunnerMode)
	}
	if len(messages) != 0 {
		t.Fatalf("no messages expected: got %v", messages)
	}
}

func TestNormalizeSowRunnerMode_ExplicitNativeSkipsLogic(t *testing.T) {
	// If the operator already typed --runner=native, don't double-
	// fire the upgrade banner. The gate condition is on
	// RunnerMode=="claude" specifically.
	in := sowRunnerState{
		RunnerMode:    "native",
		NativeAPIKey:  "sk-test",
		NativeBaseURL: "http://localhost:4000",
	}
	out, messages := normalizeSowRunnerMode(in)
	if out.RunnerMode != "native" {
		t.Fatalf("native mode must stay native: got %q", out.RunnerMode)
	}
	if len(messages) != 0 {
		t.Fatalf("no banner expected when already native: got %v", messages)
	}
}

func TestNormalizeSowRunnerMode_ExplicitCodexSkipsLogic(t *testing.T) {
	// --runner=codex is an explicit choice; don't override it.
	in := sowRunnerState{
		RunnerMode:    "codex",
		NativeAPIKey:  "sk-test",
		NativeBaseURL: "http://localhost:4000",
	}
	out, _ := normalizeSowRunnerMode(in)
	if out.RunnerMode != "codex" {
		t.Fatalf("codex mode must not be auto-upgraded: got %q", out.RunnerMode)
	}
}

func TestNormalizeSowRunnerMode_UpgradesWhenBothFlagsSet(t *testing.T) {
	// The primary fix: both flags set → runnerMode becomes native
	// and the upgrade banner is emitted.
	in := sowRunnerState{
		RunnerMode:    "claude",
		NativeAPIKey:  "sk-test",
		NativeBaseURL: "http://localhost:4000",
	}
	out, messages := normalizeSowRunnerMode(in)
	if out.RunnerMode != "native" {
		t.Fatalf("runner mode should upgrade to native: got %q", out.RunnerMode)
	}
	if len(messages) == 0 {
		t.Fatalf("upgrade banner expected")
	}
	if !strings.Contains(messages[0], "auto-upgrading --runner claude → native") {
		t.Fatalf("banner wording wrong: got %q", messages[0])
	}
}

func TestNormalizeSowRunnerMode_RemapsCodexReviewerSource(t *testing.T) {
	// --reviewer-source codex is invalid in the new modelsource
	// taxonomy (litellm/openrouter/direct). Remap to direct+codex
	// model so modelsource.ResolveRole accepts it instead of
	// fataling with "unknown source codex".
	in := sowRunnerState{
		RunnerMode:     "claude",
		NativeAPIKey:   "sk-test",
		NativeBaseURL:  "http://localhost:4000",
		ReviewerSource: "codex",
	}
	out, messages := normalizeSowRunnerMode(in)
	if out.ReviewerSource != "direct" {
		t.Fatalf("reviewer-source codex must be remapped to direct: got %q", out.ReviewerSource)
	}
	if out.ReviewerModel != "codex" {
		t.Fatalf("reviewer-model should be seeded to codex: got %q", out.ReviewerModel)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages (upgrade + remap), got %d: %v", len(messages), messages)
	}
	if !strings.Contains(messages[1], "remapped --reviewer-source codex") {
		t.Fatalf("remap banner wording wrong: got %q", messages[1])
	}
}

func TestNormalizeSowRunnerMode_ReviewerSourceCodexCaseInsensitive(t *testing.T) {
	// Operator might pass --reviewer-source CODEX or Codex; remap
	// must tolerate case variation.
	in := sowRunnerState{
		RunnerMode:     "claude",
		NativeAPIKey:   "sk-test",
		NativeBaseURL:  "http://localhost:4000",
		ReviewerSource: "Codex",
	}
	out, _ := normalizeSowRunnerMode(in)
	if out.ReviewerSource != "direct" {
		t.Fatalf("case-insensitive remap failed: got %q", out.ReviewerSource)
	}
	if out.ReviewerModel != "codex" {
		t.Fatalf("reviewer-model seed failed: got %q", out.ReviewerModel)
	}
}

func TestNormalizeSowRunnerMode_RemapsGeminiReviewerSource(t *testing.T) {
	in := sowRunnerState{
		RunnerMode:     "claude",
		NativeAPIKey:   "sk-test",
		NativeBaseURL:  "http://localhost:4000",
		ReviewerSource: "gemini",
	}
	out, _ := normalizeSowRunnerMode(in)
	if out.ReviewerSource != "direct" {
		t.Fatalf("gemini source must remap to direct: got %q", out.ReviewerSource)
	}
	if out.ReviewerModel != "gemini" {
		t.Fatalf("gemini model must be seeded: got %q", out.ReviewerModel)
	}
}

func TestNormalizeSowRunnerMode_RemapsClaudeReviewerSource(t *testing.T) {
	// --reviewer-source claude/sonnet/opus routes through litellm
	// (the reviewer was already going through the gateway; preserve
	// the endpoint choice).
	in := sowRunnerState{
		RunnerMode:     "claude",
		NativeAPIKey:   "sk-test",
		NativeBaseURL:  "http://localhost:4000",
		ReviewerSource: "sonnet",
	}
	out, _ := normalizeSowRunnerMode(in)
	if out.ReviewerSource != "litellm" {
		t.Fatalf("sonnet source must remap to litellm: got %q", out.ReviewerSource)
	}
	if out.ReviewerModel != "sonnet" {
		t.Fatalf("sonnet model must be seeded: got %q", out.ReviewerModel)
	}
}

func TestNormalizeSowRunnerMode_PreservesExistingReviewerModel(t *testing.T) {
	// If the operator already supplied --reviewer-model=opus, don't
	// clobber it with the alias default. The source remap still
	// fires (opus→litellm) but the model name stays verbatim.
	in := sowRunnerState{
		RunnerMode:     "claude",
		NativeAPIKey:   "sk-test",
		NativeBaseURL:  "http://localhost:4000",
		ReviewerSource: "opus",
		ReviewerModel:  "claude-opus-4-7",
	}
	out, _ := normalizeSowRunnerMode(in)
	if out.ReviewerModel != "claude-opus-4-7" {
		t.Fatalf("explicit reviewer-model must be preserved: got %q", out.ReviewerModel)
	}
	if out.ReviewerSource != "litellm" {
		t.Fatalf("source remap must still fire: got %q", out.ReviewerSource)
	}
}

func TestNormalizeSowRunnerMode_ValidReviewerSourcePassesThrough(t *testing.T) {
	// A reviewer-source that's already valid in the new taxonomy
	// (direct/litellm/openrouter) must not be rewritten.
	for _, src := range []string{"direct", "litellm", "openrouter"} {
		in := sowRunnerState{
			RunnerMode:     "claude",
			NativeAPIKey:   "sk-test",
			NativeBaseURL:  "http://localhost:4000",
			ReviewerSource: src,
			ReviewerModel:  "sonnet",
		}
		out, _ := normalizeSowRunnerMode(in)
		if out.ReviewerSource != src {
			t.Fatalf("valid source %q must pass through: got %q", src, out.ReviewerSource)
		}
		if out.ReviewerModel != "sonnet" {
			t.Fatalf("reviewer-model must not change for valid source %q: got %q", src, out.ReviewerModel)
		}
	}
}

func TestNormalizeSowRunnerMode_InputStructIsNotMutated(t *testing.T) {
	// Contract: the input struct must not be mutated. Callers rely
	// on this to log before/after state.
	in := sowRunnerState{
		RunnerMode:     "claude",
		NativeAPIKey:   "sk-test",
		NativeBaseURL:  "http://localhost:4000",
		ReviewerSource: "codex",
	}
	_, _ = normalizeSowRunnerMode(in)
	if in.RunnerMode != "claude" {
		t.Fatalf("input struct mutated: RunnerMode became %q", in.RunnerMode)
	}
	if in.ReviewerSource != "codex" {
		t.Fatalf("input struct mutated: ReviewerSource became %q", in.ReviewerSource)
	}
}
