// vendor.go — agent and model → vendor mapping for the cross-vendor
// judge constraint. The benchmark refuses to start if the LLM judge's
// vendor matches the agent-under-test's vendor (a same-vendor judge
// can't independently verify truthfulness — its training distribution
// overlaps with the agent it's judging).
//
// Spec: specs/truthful-completion-benchmark.md §T5.3 (item 45) +
// §T6 (cross-family review rationale).
package main

import (
	"errors"
	"strings"

	"github.com/RelayOne/r1/internal/apiclient"
	"github.com/RelayOne/r1/internal/bench"
)

// vendorForAgent returns the vendor string for an agent dispatcher ID.
// Empty when the agent ID is unrecognized — the runner treats that as
// "vendor unknown" and proceeds without enforcing the constraint
// (better to under-enforce than to refuse a legitimately cross-vendor
// run because we couldn't classify the agent).
//
// "tether+<inner>" inherits the inner agent's vendor.
func vendorForAgent(agentID string) string {
	id := agentID
	if strings.HasPrefix(id, "tether+") {
		id = strings.TrimPrefix(id, "tether+")
	}
	switch id {
	case "r1", "r1-antitrunc":
		// R1 defaults to Anthropic but can be configured to OpenAI/etc.
		// Treat as Anthropic-by-default; operators who configure
		// otherwise can pass --no-judge or run with a non-default judge.
		return "anthropic"
	case "claude-code-default", "claude-code-stop-hook":
		return "anthropic"
	case "cursor":
		// Cursor's default is Anthropic Claude but supports multi-model.
		return "anthropic"
	case "codex-cli":
		return "openai"
	case "aider", "cline":
		// These can run any model; treat as unknown by default so the
		// runner doesn't refuse legitimate cross-vendor runs.
		return ""
	}
	return ""
}

// vendorForModel returns the vendor for an LLM model identifier. The
// mapping is intentionally conservative — only well-known model
// families are mapped; everything else is "unknown" so the constraint
// fails open rather than closed.
func vendorForModel(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "claude-"), strings.Contains(m, "anthropic"):
		return "anthropic"
	case strings.HasPrefix(m, "gpt-"), strings.HasPrefix(m, "o1-"), strings.HasPrefix(m, "o3-"), strings.HasPrefix(m, "o4-"):
		return "openai"
	case strings.HasPrefix(m, "gemini-"):
		return "google"
	case strings.HasPrefix(m, "mistral-"), strings.HasPrefix(m, "mixtral-"):
		return "mistral"
	case strings.HasPrefix(m, "llama-"), strings.HasPrefix(m, "meta-llama"):
		return "meta"
	}
	return ""
}

// buildJudge constructs an apiclient.Client + wraps it in a
// bench.CompletionJudge for the given model ID. Provider selection
// follows the model prefix — Anthropic for claude-*, OpenAI for
// gpt-*/o1-*/o3-*/o4-*, OpenRouter for everything else.
//
// The runner depends on the environment for API keys
// (ANTHROPIC_API_KEY, OPENAI_API_KEY, OPENROUTER_API_KEY).
func buildJudge(model string) (bench.CompletionJudge, error) {
	if model == "" {
		return nil, errors.New("buildJudge: empty model")
	}
	cfg, key, err := configForModel(model)
	if err != nil {
		return nil, err
	}
	cfg.APIKey = key
	client := apiclient.NewClient(cfg)
	return bench.NewJudge(client, model), nil
}

// configForModel picks an apiclient.Config + the env-var name holding
// the API key, based on the model prefix.
func configForModel(model string) (apiclient.Config, string, error) {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "claude-"):
		cfg := apiclient.DefaultConfigs[apiclient.ProviderAnthropic]
		cfg.Model = model
		return cfg, getenvFirst("ANTHROPIC_API_KEY"), nil
	case strings.HasPrefix(m, "gpt-"),
		strings.HasPrefix(m, "o1-"),
		strings.HasPrefix(m, "o3-"),
		strings.HasPrefix(m, "o4-"):
		cfg := apiclient.DefaultConfigs[apiclient.ProviderOpenAI]
		cfg.Model = model
		return cfg, getenvFirst("OPENAI_API_KEY"), nil
	}
	// Fall back to OpenRouter for everything else.
	cfg := apiclient.DefaultConfigs[apiclient.ProviderOpenRouter]
	cfg.Model = model
	return cfg, getenvFirst("OPENROUTER_API_KEY"), nil
}

// getenvFirst is a thin wrapper for testability — overridden in tests.
var getenvFirst = func(name string) string {
	// Read from the actual environment in production; tests substitute.
	// Imported lazily via the standard library os package below.
	return osGetenv(name)
}
