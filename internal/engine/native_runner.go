package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ericmacdougall/stoke/internal/agentloop"
	"github.com/ericmacdougall/stoke/internal/hub"
	"github.com/ericmacdougall/stoke/internal/provider"
	"github.com/ericmacdougall/stoke/internal/stream"
	"github.com/ericmacdougall/stoke/internal/tools"
)

// NativeRunner implements CommandRunner using Stoke's own agentloop and
// the Anthropic Messages API directly. No Claude Code CLI needed.
type NativeRunner struct {
	apiKey   string
	BaseURL  string   // empty = default Anthropic URL; set for LiteLLM or custom proxy
	model    string   // e.g. "claude-sonnet-4-5"
	EventBus *hub.Bus // optional: publishes tool use events
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
	p := provider.NewAnthropicProvider(n.apiKey, n.BaseURL)

	// Create the tool registry
	toolRegistry := tools.NewRegistry(spec.WorktreeDir)
	allDefs := toolRegistry.Definitions()

	// Filter tools based on phase restrictions.
	writableTools := map[string]bool{
		"edit_file":  true,
		"write_file": true,
		"bash":       true, // bash can write; restricted in read-only mode
	}
	var toolDefs []provider.ToolDef
	for _, td := range allDefs {
		if spec.Phase.ReadOnly && writableTools[td.Name] {
			continue // exclude write-capable tools in read-only mode
		}
		toolDefs = append(toolDefs, td)
	}

	// Build allowed tool set for handler enforcement.
	allowedTools := make(map[string]bool, len(toolDefs))
	for _, td := range toolDefs {
		allowedTools[td.Name] = true
	}

	// Create the tool handler that bridges tools.Registry → agentloop.ToolHandler
	handler := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		if !allowedTools[name] {
			return "", fmt.Errorf("tool %q not allowed in phase %q (read_only=%v)", name, spec.Phase.Name, spec.Phase.ReadOnly)
		}
		return toolRegistry.Handle(ctx, name, input)
	}

	// Configure the agentloop. SystemPrompt is the cacheable static
	// context (passed via RunSpec.SystemPrompt or Phase.Prompt); it's
	// wrapped in a cache_control breakpoint by agentloop.
	systemPrompt := spec.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = spec.Phase.Prompt
	}
	cfg := agentloop.Config{
		Model:              n.model,
		MaxTurns:           spec.Phase.MaxTurns,
		MaxConsecutiveErrs: 3,
		MaxTokens:          16000,
		SystemPrompt:       systemPrompt,
	}

	// Progressive context compaction. When RunSpec.CompactThreshold is
	// set, hook a cache-preserving compactor into the agentloop so long
	// tasks don't blow past the context window. The compactor keeps the
	// first user message (task brief) + the last 6 messages verbatim
	// and summarizes older tool_results.
	if compactionEnabled(spec) {
		cfg.CompactThreshold = spec.CompactThreshold
		cfg.CompactFn = buildNativeCompactor(6, 200)
	}

	// Create and configure the loop
	loop := agentloop.New(p, cfg, toolDefs, handler)

	// Wire hub event bus for tool use events
	if n.EventBus != nil {
		loop.SetEventBus(n.EventBus)
	}

	// Wire streaming events if callback provided
	if onEvent != nil {
		loop.SetOnText(func(text string) {
			onEvent(stream.Event{DeltaText: text})
		})
	}

	// User message is spec.Prompt. The cacheable static context was
	// already passed as cfg.SystemPrompt above. When the caller only
	// set Phase.Prompt (legacy behavior before spec.SystemPrompt
	// existed) we still respect it: it's treated as the system prompt
	// and spec.Prompt becomes the user message.
	userMessage := spec.Prompt

	// Run the loop
	result, err := loop.Run(ctx, userMessage)

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
