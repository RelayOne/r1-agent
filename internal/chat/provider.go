package chat

import (
	"errors"
	"os"
	"strings"

	"github.com/ericmacdougall/stoke/internal/provider"
)

// ProviderOptions describes the inputs needed to stand up a chat-capable
// provider. It intentionally mirrors the subset of SmartDefaults that is
// relevant to conversational use: base URL, API key, and model. This keeps
// the chat package free of a dependency on cmd/stoke.
type ProviderOptions struct {
	// BaseURL is the Anthropic-protocol endpoint. Leave empty to use
	// the default api.anthropic.com.
	BaseURL string
	// APIKey is the credential. If empty, ANTHROPIC_API_KEY env is used.
	APIKey string
	// Model is the model ID (e.g. "claude-sonnet-4-6"). Required.
	Model string
}

// ErrNoProvider is returned when the environment has no credentials that
// would let chat mode work. Callers should surface a friendly message
// explaining how to configure one (e.g. "set ANTHROPIC_API_KEY or run a
// LiteLLM proxy").
var ErrNoProvider = errors.New("chat: no provider available — set ANTHROPIC_API_KEY, point LITELLM_BASE_URL at a proxy, or pass BaseURL/APIKey explicitly")

// NewProviderFromOptions returns an Anthropic-protocol provider.Provider
// built from the given options. LiteLLM proxies speak Anthropic's
// /v1/messages protocol, so a LiteLLM BaseURL goes through the same
// AnthropicProvider — the only difference is the Authorization header,
// which AnthropicProvider already handles.
//
// Returns ErrNoProvider when neither APIKey nor ANTHROPIC_API_KEY env is
// set AND BaseURL is empty — in that case chat has nothing to talk to.
func NewProviderFromOptions(opts ProviderOptions) (provider.Provider, error) {
	apiKey := strings.TrimSpace(opts.APIKey)
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	baseURL := strings.TrimSpace(opts.BaseURL)

	// We need either (a) a real API key to hit api.anthropic.com, or
	// (b) a BaseURL pointing at a proxy. LiteLLM on localhost doesn't
	// require a real key, so BaseURL alone is enough.
	if apiKey == "" && baseURL == "" {
		return nil, ErrNoProvider
	}
	// Using a local LiteLLM with no auth: inject the stub key so the
	// header is populated (the provider sends x-api-key regardless).
	if apiKey == "" {
		apiKey = provider.LocalLiteLLMStub
	}

	return provider.NewAnthropicProvider(apiKey, baseURL), nil
}
