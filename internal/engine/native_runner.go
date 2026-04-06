package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ericmacdougall/stoke/internal/agentloop"
	"github.com/ericmacdougall/stoke/internal/provider"
	"github.com/ericmacdougall/stoke/internal/stream"
	"github.com/ericmacdougall/stoke/internal/tools"
)

// NativeRunner implements CommandRunner using Stoke's own agentloop and
// the Anthropic Messages API directly. No Claude Code CLI needed.
type NativeRunner struct {
	apiKey  string
	baseURL string // empty = default Anthropic URL
	model   string // e.g. "claude-sonnet-4-5"
}

// NewNativeRunner creates a native runner using the Anthropic API directly.
func NewNativeRunner(apiKey, model string) *NativeRunner {
	return &NativeRunner{
		apiKey: apiKey,
		model:  model,
	}
}

// Prepare returns a PreparedCommand for informational/logging purposes.
// The native runner doesn't spawn a subprocess, so this is minimal.
func (n *NativeRunner) Prepare(spec RunSpec) (PreparedCommand, error) {
	if err := spec.Validate(); err != nil {
		return PreparedCommand{}, err
	}
	return PreparedCommand{
		Binary: "native",
		Args:   []string{"--model", n.model},
		Dir:    spec.WorktreeDir,
		Notes:  []string{"Using Stoke native agentloop (no CLI subprocess)"},
	}, nil
}

// Run executes a coding task using the native agentloop.
func (n *NativeRunner) Run(ctx context.Context, spec RunSpec, onEvent OnEventFunc) (RunResult, error) {
	if err := spec.Validate(); err != nil {
		return RunResult{IsError: true}, err
	}

	start := time.Now()

	// Create the Anthropic provider
	p := provider.NewAnthropicProvider(n.apiKey, n.baseURL)

	// Create the tool registry
	toolRegistry := tools.NewRegistry(spec.WorktreeDir)
	toolDefs := toolRegistry.Definitions()

	// Create the tool handler that bridges tools.Registry → agentloop.ToolHandler
	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return toolRegistry.Handle(ctx, name, input)
	}

	// Configure the agentloop
	cfg := agentloop.Config{
		Model:              n.model,
		MaxTurns:           spec.Phase.MaxTurns,
		MaxConsecutiveErrs: 3,
		MaxTokens:          16000,
	}

	// Create and configure the loop
	loop := agentloop.New(p, cfg, toolDefs, handler)

	// Wire streaming events if callback provided
	if onEvent != nil {
		loop.SetOnText(func(text string) {
			onEvent(stream.Event{DeltaText: text})
		})
	}

	// Build the system prompt incorporating the phase prompt
	prompt := spec.Prompt
	if spec.Phase.Prompt != "" {
		prompt = spec.Phase.Prompt
	}

	// Run the loop
	result, err := loop.Run(ctx, prompt)

	duration := time.Since(start)
	runResult := RunResult{
		DurationMs: duration.Milliseconds(),
		Subtype:    "success",
	}

	if result != nil {
		runResult.NumTurns = result.Turns
		runResult.ResultText = result.FinalText
		runResult.Tokens = stream.TokenUsage{
			Input:  result.TotalCost.InputTokens,
			Output: result.TotalCost.OutputTokens,
		}
		runResult.CostUSD = result.TotalCost.TotalCostUSD(n.model)

		switch result.StopReason {
		case "max_turns":
			runResult.Subtype = "error_max_turns"
			runResult.IsError = true
		case "max_errors":
			runResult.Subtype = "error_during_execution"
			runResult.IsError = true
		case "cancelled":
			runResult.Subtype = "cancelled"
			runResult.IsError = true
		}
	}

	if err != nil {
		runResult.IsError = true
		if runResult.Subtype == "success" {
			runResult.Subtype = "error_during_execution"
		}
		return runResult, fmt.Errorf("native runner: %w", err)
	}

	// Emit final result event
	if onEvent != nil {
		onEvent(stream.Event{
			Type:       "result",
			CostUSD:    runResult.CostUSD,
			Tokens:     runResult.Tokens,
			DurationMs: runResult.DurationMs,
			NumTurns:   runResult.NumTurns,
			StopReason: result.StopReason,
			ResultText: result.FinalText,
		})
	}

	return runResult, nil
}
