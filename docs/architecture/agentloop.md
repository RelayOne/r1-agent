# Agent Loop Architecture

## Overview

The agent loop (`internal/agentloop/`) implements a native agentic tool-use loop using the Anthropic Messages API directly, bypassing the Claude Code CLI. This gives Stoke full control over tool execution, prompt caching, cost tracking, and context management.

## Design

### Stateless Messages API

The entire conversation history is sent on every API call. This is the Anthropic-recommended pattern for tool-use agents — no server-side state to manage.

### Loop Structure

```
Loop.Run(ctx, userMessage)
  for turn := 0; turn < MaxTurns; turn++ {
    1. Build request (sorted tools, cached system prompt)
    2. Call API with streaming (ChatStream)
    3. Accumulate cost (CostTracker.Add)
    4. Convert response → ContentBlocks
    5. If stop_reason != "tool_use" → return result
    6. Execute tools (parallel if multiple)
    7. Append tool results as user message
    8. Check consecutive error limit
  }
```

### Configuration

```go
Config{
    Model:              "claude-sonnet-4-5-20250929",
    MaxTurns:           25,        // hard limit on API calls
    MaxConsecutiveErrs: 3,         // abort after 3 consecutive tool errors
    MaxTokens:          16000,     // per-turn output limit
    SystemPrompt:       "...",     // static, cached across turns
    ThinkingBudget:     0,         // extended thinking (0 = disabled)
    Timeout:            5*time.Minute,
}
```

### Prompt Cache Alignment

Three techniques for ~90% input token cost reduction:

1. **Tool sorting**: `SortToolsDeterministic()` sorts tools alphabetically. Non-deterministic ordering is the #1 cache-busting anti-pattern.
2. **System prompt caching**: `BuildCachedSystemPrompt()` wraps the system prompt in `cache_control: {type: "ephemeral"}` breakpoints.
3. **Tool definition caching**: `toolsWithCacheControl()` adds `cache_control` to the last tool definition (Anthropic-recommended pattern).

### Tool Execution

- Single tool: executed inline
- Multiple tools: executed in parallel via goroutines + `sync.WaitGroup`
- Hub integration: `EvtToolPreUse` (sync gate — can block) and `EvtToolPostUse` (async observe)

### Cost Tracking

`CostTracker` accumulates input/output/cache-write/cache-read tokens across turns. `TotalCostUSD(model)` computes dollar cost using model-specific pricing.

## Provider Layer (`internal/provider/`)

### AnthropicProvider

Direct HTTP client for Anthropic Messages API:
- Non-streaming (`Chat`) and streaming (`ChatStream`) with SSE parsing
- `SystemRaw` field for pre-formatted system blocks with cache_control
- `CacheEnabled` flag triggers cache_control on last tool definition

### OpenAICompatProvider

Generic client for OpenAI, XAI, OpenRouter endpoints:
- Translates Anthropic-style messages to OpenAI format
- Streaming via SSE with `data: [DONE]` sentinel

### ResolveProvider

Routes model names to providers: `claude*` → Anthropic, `gpt*/o1/o3` → OpenAI, `grok*` → XAI, `*/` → OpenRouter.

## Integration

```
engine.NativeRunner.Run()
  → provider.NewAnthropicProvider()
  → agentloop.New(provider, config, tools, handler)
  → loop.SetEventBus(bus)
  → loop.Run(ctx, task)
  → return RunResult{cost, tokens, turns, duration}
```
