// Auto-upgrade rationale:
//
// `stoke sow` defaults --runner to "claude". When the operator
// passes --native-base-url + --native-api-key but forgets (or
// isn't expected to know) to also pass --runner=native, runnerMode
// stays "claude" and the workflow engine falls through to
// task-type-aware routing in internal/workflow.pickRunner
// (internal/model.Resolve). That routing maps TaskTypeArchitecture
// and TaskTypeDevOps to ProviderCodex primary — which means those
// tasks dispatch to the codex CLI runner. That runner then tries
// to acquire a codex subscription pool at workflow.go:638, which
// fails in a native/LiteLLM-only deployment where no codex pool is
// provisioned. Result: every "architecture" / "devops" task fails
// in ~0.7s with cost $0 and the error
// "all pools exhausted for codex: no available pool for codex"
// before the LLM is ever called. Tasks typed "typesafety" /
// "refactor" / "docs" happen to route to Claude primary and
// coincidentally succeed.
//
// Fix: when the operator has wired a native backend (both base URL
// and API key set), force runnerMode=native so pickRunner short-
// circuits into the native runner for all phases. The native
// runner talks directly to the configured endpoint (LiteLLM proxy,
// direct Anthropic, etc.) and doesn't need a subscription pool.
//
// Side fix: --reviewer-source was historically tolerated as a
// model-family hint ("codex", "gemini", "claude"). The native fast
// path's modelsource.ResolveRole validator rejects anything that
// isn't litellm / openrouter / direct. Without remapping, the
// auto-upgrade would convert a silent no-op into a fatal error.
// Remap legacy family names to the new source+model taxonomy.

package main

import (
	"fmt"
	"strings"
)

// sowRunnerState is the subset of sow command flags the auto-upgrade
// logic reads and writes. Keeping it as a struct lets the pure
// function return a new state without touching the ambient flag
// pointers, which makes it unit-testable.
type sowRunnerState struct {
	RunnerMode     string
	NativeAPIKey   string
	NativeBaseURL  string
	ReviewerSource string
	ReviewerModel  string
}

// normalizeSowRunnerMode returns the state with the auto-upgrade
// applied, plus a slice of operator-visible log lines describing
// every change that was made. The input state is NOT mutated.
//
// The function is deterministic and has no side effects beyond
// string construction, so tests can pin exact output.
func normalizeSowRunnerMode(in sowRunnerState) (sowRunnerState, []string) {
	out := in
	var messages []string

	// Only act when runnerMode is the default "claude" AND both
	// native-backend flags are set. Any explicit --runner choice
	// (native, codex, hybrid, claude with no native-*) passes
	// through unchanged.
	if !(out.RunnerMode == "claude" && out.NativeAPIKey != "" && out.NativeBaseURL != "") {
		return out, messages
	}

	out.RunnerMode = "native"
	messages = append(messages,
		"  🔁 auto-upgrading --runner claude → native (--native-base-url and --native-api-key both set)")

	origReviewerSource := strings.ToLower(strings.TrimSpace(out.ReviewerSource))
	switch origReviewerSource {
	case "codex", "gpt", "gpt-5":
		if out.ReviewerModel == "" {
			out.ReviewerModel = "codex"
		}
		out.ReviewerSource = "direct"
		messages = append(messages, fmt.Sprintf(
			"  🔁 remapped --reviewer-source %s → --reviewer-source direct --reviewer-model %s",
			origReviewerSource, out.ReviewerModel))
	case "gemini", "flash":
		if out.ReviewerModel == "" {
			out.ReviewerModel = "gemini"
		}
		out.ReviewerSource = "direct"
		messages = append(messages, fmt.Sprintf(
			"  🔁 remapped --reviewer-source %s → --reviewer-source direct --reviewer-model %s",
			origReviewerSource, out.ReviewerModel))
	case "claude", "sonnet", "opus":
		if out.ReviewerModel == "" {
			out.ReviewerModel = "sonnet"
		}
		out.ReviewerSource = "litellm"
		messages = append(messages, fmt.Sprintf(
			"  🔁 remapped --reviewer-source %s → --reviewer-source litellm --reviewer-model %s",
			origReviewerSource, out.ReviewerModel))
	}

	return out, messages
}
