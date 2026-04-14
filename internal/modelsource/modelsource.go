// Package modelsource resolves the operator's intent — expressed as
// env vars and/or CLI flags — into a concrete provider.Provider plus
// the exact model ID the caller should pass in each ChatRequest.
//
// Two roles are supported:
//
//   - Builder: the model that writes the code (workers, task dispatch).
//   - Reviewer: the model that judges the builder's output (per-task
//     reviewer, decomposer, meta-judge, AC semantic judge, content
//     judge, integration reviewer).
//
// Each role has three inputs that can be stacked from highest to
// lowest precedence: CLI flag → env var → default. The default
// behavior is "route everything through the local LiteLLM gateway"
// so existing deployments do not change when these flags are absent.
//
// Sources:
//
//   - litellm: route through the LiteLLM proxy. URL auto-discovered
//     from ~/.litellm/proxy.port, or overridden via BUILDER_URL /
//     REVIEWER_URL / --builder-url / --reviewer-url. API key:
//     LITELLM_MASTER_KEY. The existing default.
//
//   - openrouter: route through OpenRouter (https://openrouter.ai).
//     Uses the OpenAI-compatible /v1/chat/completions path already
//     supported by provider.OpenAICompatProvider. API key:
//     OPENROUTER_API_KEY.
//
//   - direct: talk straight to the vendor endpoint for the chosen
//     model family. Endpoint + auth vary per family:
//       sonnet / opus  → https://api.anthropic.com via
//                         provider.AnthropicProvider + ANTHROPIC_API_KEY
//       codex / gpt-*  → https://api.openai.com via OpenAICompat +
//                         OPENAI_API_KEY
//       gemini / pro   → https://generativelanguage.googleapis.com/
//                         v1beta/openai via OpenAICompat + GEMINI_KEY
//                         or GEMINI_API_KEY
//
// Model aliases map short names (sonnet, opus, gemini, codex, litellm)
// to concrete model IDs. An operator who needs a model not in the
// alias list can pass its full ID directly; we only apply the alias
// table when the input matches an alias.
//
// "litellm" as a Model value means "whatever the local gateway is
// configured to serve by default" — the BUILDER_MODEL=litellm case.
// In practice that's the same as leaving the flag unset and letting
// the legacy --native-model / --reasoning-model pick up, so we simply
// don't rewrite the model ID when source=litellm and model=litellm.
package modelsource

import (
	"fmt"
	"os"
	"strings"

	"github.com/ericmacdougall/stoke/internal/litellm"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// Role names a model's job in a SOW run.
type Role string

const (
	RoleBuilder  Role = "builder"
	RoleReviewer Role = "reviewer"
)

// Source names where the request should be sent.
type Source string

const (
	SourceLiteLLM    Source = "litellm"
	SourceOpenRouter Source = "openrouter"
	SourceDirect     Source = "direct"
)

// Config is a fully-resolved model-source spec for one role.
type Config struct {
	Role    Role
	Source  Source
	// Model is a vendor-independent alias ("sonnet", "gemini") or an
	// exact vendor model ID. Build() rewrites aliases only when the
	// chosen Source matches the alias's natural home.
	Model   string
	// URL overrides the default endpoint for the chosen Source. Only
	// meaningful when Source == SourceDirect; for litellm and openrouter
	// the default endpoint is always used.
	URL     string
	APIKey  string
}

// Resolved pairs a ready-to-use provider with the concrete model ID
// the caller should pass in every ChatRequest.
type Resolved struct {
	Provider provider.Provider
	Model    string
	Source   Source
	// Endpoint is the URL the provider will actually hit. Surfaced so
	// stoke can log it at startup for operator sanity.
	Endpoint string
}

// Build turns a Config into a Resolved (provider + model ID). Errors
// only on genuinely missing config (e.g. source=direct with no URL
// and no defaults). Missing API keys are permitted — some deployments
// run entirely through a local LiteLLM that does not require one —
// and the resulting request will just fail with an auth error at
// call time if the key truly was required.
func (c Config) Build() (*Resolved, error) {
	modelID := expandModelAlias(c.Model)
	endpoint := c.URL
	apiKey := c.APIKey

	switch c.Source {
	case SourceLiteLLM, "":
		if endpoint == "" {
			endpoint = defaultLiteLLMURL()
		}
		if apiKey == "" {
			apiKey = os.Getenv("LITELLM_MASTER_KEY")
		}
		// When the operator asks for "litellm" as the model, treat
		// that as "let the gateway decide" — don't rewrite it to a
		// concrete vendor ID. The caller will typically fall back to
		// --native-model in that case.
		if strings.EqualFold(c.Model, "litellm") {
			modelID = ""
		}
		// LiteLLM exposes the Anthropic pass-through path natively,
		// so route through AnthropicProvider; that keeps cache_control,
		// tool-use, and thinking blocks working end-to-end without
		// format translation.
		return &Resolved{
			Provider: provider.NewAnthropicProvider(apiKey, endpoint),
			Model:    modelID,
			Source:   SourceLiteLLM,
			Endpoint: endpoint,
		}, nil

	case SourceOpenRouter:
		if endpoint == "" {
			endpoint = "https://openrouter.ai/api"
		}
		if apiKey == "" {
			apiKey = os.Getenv("OPENROUTER_API_KEY")
		}
		// OpenRouter uses the OpenAI /v1/chat/completions path, which
		// is the OpenAICompatProvider default — the existing message,
		// tool-call, and streaming conversions work unchanged.
		return &Resolved{
			Provider: provider.NewOpenAICompatProvider("openrouter", apiKey, endpoint),
			Model:    modelID,
			Source:   SourceOpenRouter,
			Endpoint: endpoint,
		}, nil

	case SourceDirect:
		family := familyFor(modelID, c.Model)
		switch family {
		case familyAnthropic:
			if endpoint == "" {
				endpoint = "https://api.anthropic.com"
			}
			if apiKey == "" {
				apiKey = os.Getenv("ANTHROPIC_API_KEY")
			}
			return &Resolved{
				Provider: provider.NewAnthropicProvider(apiKey, endpoint),
				Model:    modelID,
				Source:   SourceDirect,
				Endpoint: endpoint,
			}, nil
		case familyOpenAI:
			if endpoint == "" {
				endpoint = "https://api.openai.com"
			}
			if apiKey == "" {
				apiKey = os.Getenv("OPENAI_API_KEY")
			}
			return &Resolved{
				Provider: provider.NewOpenAICompatProvider("openai", apiKey, endpoint),
				Model:    modelID,
				Source:   SourceDirect,
				Endpoint: endpoint,
			}, nil
		case familyGemini:
			// Google's OpenAI-compat surface. Note the unusual path:
			// the base URL embeds /v1beta/openai/ and chat completions
			// hangs off that as /chat/completions (no second /v1).
			if endpoint == "" {
				endpoint = "https://generativelanguage.googleapis.com/v1beta/openai"
			}
			if apiKey == "" {
				// Accept both GEMINI_KEY (user's preferred name) and
				// GEMINI_API_KEY (vendor-standard). Either works.
				apiKey = firstNonEmpty(os.Getenv("GEMINI_KEY"), os.Getenv("GEMINI_API_KEY"))
			}
			return &Resolved{
				Provider: provider.NewOpenAICompatProviderWithPath("gemini", apiKey, endpoint, "/chat/completions"),
				Model:    modelID,
				Source:   SourceDirect,
				Endpoint: endpoint + "/chat/completions",
			}, nil
		default:
			return nil, fmt.Errorf("modelsource: source=direct but could not infer vendor from model %q (supported aliases: sonnet, opus, codex, gemini)", c.Model)
		}
	}
	return nil, fmt.Errorf("modelsource: unknown source %q (expected litellm / openrouter / direct)", c.Source)
}

// modelFamily is the vendor family for source=direct routing.
type modelFamily int

const (
	familyUnknown modelFamily = iota
	familyAnthropic
	familyOpenAI
	familyGemini
)

func familyFor(modelID, alias string) modelFamily {
	// Prefer the pre-expansion alias when it's one of the known
	// shorthand names — unambiguous.
	switch strings.ToLower(strings.TrimSpace(alias)) {
	case "sonnet", "opus", "haiku":
		return familyAnthropic
	case "codex", "gpt", "gpt-5":
		return familyOpenAI
	case "gemini", "flash", "pro":
		return familyGemini
	}
	// Otherwise inspect the model ID directly. This lets an operator
	// pass an exact vendor model ID that isn't in the alias map and
	// still get routed correctly.
	id := strings.ToLower(modelID)
	switch {
	case strings.HasPrefix(id, "claude-"):
		return familyAnthropic
	case strings.HasPrefix(id, "gpt-") || strings.HasPrefix(id, "o1-") || strings.HasPrefix(id, "o3-") || strings.HasPrefix(id, "codex-"):
		return familyOpenAI
	case strings.HasPrefix(id, "gemini-"):
		return familyGemini
	}
	return familyUnknown
}

// expandModelAlias turns short aliases into concrete vendor model IDs.
// Unknown inputs are returned unchanged so an operator can always
// specify a full vendor model ID by hand.
//
// Gemini aliases reflect the April 2026 lineup:
//   - gemini-3-pro-preview was shut down March 9, 2026; operators now
//     migrate to gemini-3.1-pro-preview.
//   - gemini-2.5-pro and gemini-2.5-flash are slated for shutdown
//     June 1, 2026 — still work today but the "gemini" alias points
//     at the current preview flagship rather than a soon-to-be-dead
//     stable model.
//   - gemini-2.5-flash-lite is the budget-tier stable model.
//   - gemini-3.1-flash-lite-preview is the budget-tier preview.
func expandModelAlias(in string) string {
	switch strings.ToLower(strings.TrimSpace(in)) {
	case "":
		return ""
	case "sonnet":
		return "claude-sonnet-4-6"
	case "opus":
		return "claude-opus-4-6"
	case "haiku":
		return "claude-haiku-4-5-20251001"
	case "gemini", "pro", "gemini-pro", "gemini-3", "gemini-3.1":
		return "gemini-3.1-pro-preview"
	case "gemini-2.5-pro":
		// Retained as an explicit alias for callers pinning to the
		// legacy stable family before its June 2026 shutdown.
		return "gemini-2.5-pro"
	case "flash":
		return "gemini-2.5-flash"
	case "flash-lite":
		return "gemini-2.5-flash-lite"
	case "gemini-3.1-flash-lite", "flash-lite-preview":
		return "gemini-3.1-flash-lite-preview"
	case "codex":
		return "gpt-5-codex"
	case "gpt", "gpt-5":
		return "gpt-5"
	case "litellm":
		// Sentinel value meaning "let the gateway decide"; Build()
		// clears modelID so the ChatRequest carries whatever
		// --native-model / --reasoning-model specified.
		return "litellm"
	}
	return in
}

func defaultLiteLLMURL() string {
	// Reuse the existing discovery helper so this matches the behavior
	// of --native-base-url when unset.
	if d := litellm.Discover(); d != nil && d.BaseURL != "" {
		return d.BaseURL
	}
	return "http://localhost:4001"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ResolveRole is the entry point used by cmd/stoke. flagValues takes
// precedence over envValues; both fall back to the provided legacy
// values so existing `--native-*` / `--reasoning-*` behavior is
// preserved when the new flags/env are absent.
//
// legacyModel / legacyURL / legacyAPIKey let the caller pass in what
// the old scheme resolved to, and only be overridden when the operator
// explicitly specified one of the new inputs.
func ResolveRole(role Role, flagModel, flagSource, flagURL, flagAPIKey, legacyModel, legacyURL, legacyAPIKey string) (*Resolved, bool, error) {
	envPrefix := "BUILDER_"
	if role == RoleReviewer {
		envPrefix = "REVIEWER_"
	}

	model := firstNonEmpty(flagModel, os.Getenv(envPrefix+"MODEL"))
	source := firstNonEmpty(flagSource, os.Getenv(envPrefix+"SOURCE"))
	url := firstNonEmpty(flagURL, os.Getenv(envPrefix+"URL"))
	apiKey := firstNonEmpty(flagAPIKey, os.Getenv(envPrefix+"API_KEY"))

	// Implicit default: when GEMINI_KEY is set AND the operator has
	// not specified anything for the reviewer role, auto-route the
	// reviewer to Gemini direct. This gives the "second perspective"
	// behavior the operator wants from just setting the env var,
	// without affecting the builder role.
	if role == RoleReviewer && model == "" && source == "" && os.Getenv("GEMINI_KEY") != "" {
		model = "gemini"
		source = string(SourceDirect)
	}

	if model == "" && source == "" && url == "" && apiKey == "" {
		// Fully unspecified — caller keeps legacy behavior.
		return nil, false, nil
	}

	cfg := Config{
		Role:   role,
		Source: Source(strings.ToLower(strings.TrimSpace(source))),
		Model:  model,
		URL:    url,
		APIKey: apiKey,
	}
	if cfg.Source == "" {
		cfg.Source = SourceLiteLLM
	}
	// If only URL or API key were overridden, fall back to legacy
	// model so the operator can point a legacy config at a custom
	// endpoint without re-stating the model.
	if cfg.Model == "" {
		cfg.Model = legacyModel
	}
	// Legacy URL / API key fallback applies across ALL sources, not
	// just LiteLLM. A deployment whose --native-base-url already
	// points at a direct Anthropic endpoint or an OpenRouter proxy
	// must keep using that endpoint when the operator adds only
	// --builder-source=direct or --reviewer-source=openrouter without
	// re-stating URL/key. Limiting fallback to LiteLLM silently
	// dropped those legacy endpoints for non-LiteLLM sources — the
	// codex-review P2 on fee0de4.
	if cfg.URL == "" {
		cfg.URL = legacyURL
	}
	if cfg.APIKey == "" {
		cfg.APIKey = legacyAPIKey
	}

	r, err := cfg.Build()
	if err != nil {
		return nil, true, err
	}
	return r, true, nil
}
