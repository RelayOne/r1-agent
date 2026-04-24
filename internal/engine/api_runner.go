package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/apiclient"
	"github.com/RelayOne/r1/internal/stream"
)

// Stream-event types and result subtypes used when translating
// apiclient events into stream.Event payloads.
const (
	apiEventText            = "text"
	apiSubtypeSuccess       = "success"
	apiSubtypeErrorRunning  = "error_during_execution"
	apiSubtypeRateLimited   = "rate_limited"
)

// APIRunner implements CommandRunner using the native apiclient package
// for direct API calls. Supports Anthropic, OpenAI-compatible, and
// LiteLLM proxy endpoints without spawning subprocesses.
type APIRunner struct {
	// DefaultModel is the model name when not overridden by the pool.
	DefaultModel string
	// DefaultMaxTokens is the max output tokens.
	DefaultMaxTokens int
	// Timeout for the API call.
	Timeout time.Duration
}

// NewAPIRunner creates an APIRunner with sensible defaults.
func NewAPIRunner() *APIRunner {
	return &APIRunner{
		DefaultModel:     "claude-sonnet-4-20250514",
		DefaultMaxTokens: 16000,
		Timeout:          5 * time.Minute,
	}
}

// Prepare builds a PreparedCommand describing the API call.
// Since there is no CLI binary, Binary is empty and the call
// parameters are recorded in Notes for logging.
func (r *APIRunner) Prepare(spec RunSpec) (PreparedCommand, error) {
	if spec.PoolAPIKey == "" {
		return PreparedCommand{}, fmt.Errorf("native API runner requires an API key (PoolAPIKey)")
	}

	baseURL := spec.PoolBaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	model := r.DefaultModel
	maxTokens := r.DefaultMaxTokens
	if maxTokens == 0 {
		maxTokens = 16000
	}

	endpoint := baseURL + "/v1/messages"

	notes := []string{
		"Native API runner (direct HTTP, no subprocess)",
		fmt.Sprintf("Endpoint: %s", endpoint),
		fmt.Sprintf("Model: %s", model),
		fmt.Sprintf("MaxTokens: %d", maxTokens),
		fmt.Sprintf("Phase: %s", spec.Phase.Name),
	}

	return PreparedCommand{
		Binary: "", // no subprocess
		Dir:    spec.WorktreeDir,
		Notes:  notes,
	}, nil
}

// Run makes a direct API call using the apiclient package with SSE streaming.
// It returns the same RunResult as ClaudeRunner so the workflow is agnostic
// to which runner was used.
func (r *APIRunner) Run(ctx context.Context, spec RunSpec, onEvent OnEventFunc) (RunResult, error) {
	prepared, err := r.Prepare(spec)
	if err != nil {
		return RunResult{}, err
	}

	// Determine provider from base URL
	provider := detectProvider(spec.PoolBaseURL)

	cfg := apiclient.Config{
		Provider:  provider,
		APIKey:    spec.PoolAPIKey,
		BaseURL:   spec.PoolBaseURL,
		Model:     r.DefaultModel,
		MaxTokens: r.DefaultMaxTokens,
		Timeout:   r.Timeout,
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.anthropic.com"
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 16000
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}

	client := apiclient.NewClient(cfg)

	// Build the request. The phase prompt is used as the system prompt
	// and spec.Prompt is the user message.
	req := apiclient.Request{
		Messages: []apiclient.Message{
			{Role: "user", Content: spec.Prompt},
		},
		System:    spec.Phase.Prompt,
		MaxTokens: cfg.MaxTokens,
		Stream:    true,
	}

	// If the phase prompt is empty, send only the user message.
	if spec.Phase.Prompt == "" {
		req.System = ""
	}

	start := time.Now()
	var resultText strings.Builder
	var lastUsage apiclient.Usage

	// Stream the response, translating apiclient events to stream.Event
	usage, apiErr := client.Stream(ctx, req, func(ev apiclient.StreamEvent) {
		switch ev.Type {
		case apiEventText:
			resultText.WriteString(ev.Text)
			if onEvent != nil {
				onEvent(stream.Event{
					Type:      "assistant",
					DeltaText: ev.Text,
				})
			}
		case "error":
			if onEvent != nil {
				onEvent(stream.Event{
					Type:    "result",
					IsError: true,
					Subtype: apiSubtypeErrorRunning,
				})
			}
		}
		if ev.Usage != nil {
			lastUsage = *ev.Usage
		}
	})

	durationMs := time.Since(start).Milliseconds()

	result := RunResult{
		Prepared:   prepared,
		DurationMs: durationMs,
		NumTurns:   1,
	}

	if apiErr != nil {
		result.IsError = true
		result.ResultText = apiErr.Error()
		result.Subtype = apiSubtypeErrorRunning

		// Detect rate limiting and auth errors
		var apiError *apiclient.APIError
		if errors.As(apiErr, &apiError) {
			if apiError.IsRateLimit() {
				result.Subtype = apiSubtypeRateLimited
			}
			if apiError.IsAuth() {
				result.Subtype = "auth_error"
			}
		}

		// Emit a result event for the error
		if onEvent != nil {
			onEvent(stream.Event{
				Type:       "result",
				IsError:    true,
				Subtype:    result.Subtype,
				ResultText: result.ResultText,
				DurationMs: durationMs,
			})
		}

		return result, nil
	}

	// Merge usage from stream handler and final usage
	if usage != nil {
		if usage.InputTokens > 0 {
			lastUsage.InputTokens = usage.InputTokens
		}
		if usage.OutputTokens > 0 {
			lastUsage.OutputTokens = usage.OutputTokens
		}
	}

	result.ResultText = resultText.String()
	result.Subtype = apiSubtypeSuccess
	result.Tokens = stream.TokenUsage{
		Input:  lastUsage.InputTokens,
		Output: lastUsage.OutputTokens,
	}

	// Estimate cost (Anthropic Claude Sonnet pricing as baseline)
	result.CostUSD = estimateCost(lastUsage)

	// Emit final result event
	if onEvent != nil {
		onEvent(stream.Event{
			Type:       "result",
			Subtype:    apiSubtypeSuccess,
			ResultText: result.ResultText,
			DurationMs: durationMs,
			CostUSD:    result.CostUSD,
			NumTurns:   1,
			Tokens: stream.TokenUsage{
				Input:  lastUsage.InputTokens,
				Output: lastUsage.OutputTokens,
			},
		})
	}

	return result, nil
}

// detectProvider infers the apiclient.Provider from the base URL.
func detectProvider(baseURL string) apiclient.Provider {
	if baseURL == "" {
		return apiclient.ProviderAnthropic
	}
	lower := strings.ToLower(baseURL)
	switch {
	case strings.Contains(lower, "openrouter.ai"):
		return apiclient.ProviderOpenRouter
	case strings.Contains(lower, "openai.com"):
		return apiclient.ProviderOpenAI
	case strings.Contains(lower, "api.anthropic.com"):
		return apiclient.ProviderAnthropic
	default:
		// LiteLLM proxy and other OpenAI-compatible endpoints
		// use the OpenAI protocol by default
		return apiclient.ProviderOpenAI
	}
}

// estimateCost gives a rough cost estimate based on token counts.
// Uses Anthropic Claude Sonnet pricing as a baseline:
// $3/M input, $15/M output.
func estimateCost(usage apiclient.Usage) float64 {
	inputCost := float64(usage.InputTokens) * 3.0 / 1_000_000
	outputCost := float64(usage.OutputTokens) * 15.0 / 1_000_000
	return inputCost + outputCost
}
