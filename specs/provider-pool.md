<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-20 -->
<!-- DEPENDS_ON: spec-2 (event emission), optional spec-7 (cost dashboard consumes) -->
<!-- BUILD_ORDER: 13 -->

# Provider Pool — Implementation Spec (S-6)

## Overview

Build a unified provider pool (`internal/providerpool/`) that wraps every existing Stoke LLM backend (Anthropic direct, Claude Code subscription, Codex, OpenRouter, Gemini, Ember, LiteLLM, local ollama/llama.cpp) behind one interface with capability negotiation, per-task-type failover, and per-provider cost tracking. The net user promise: "any model works" — the operator points Stoke at whichever providers they have, and the pool routes each task (worker code / reviewer / research lead / research subagent / embedder) to the best available backend, fails over transparently on rate limits, and emits uniform cost events. Gated on `STOKE_PROVIDER_POOL=1` during rollout so the existing single-`BaseURL` path in `internal/engine/native_runner.go:25` stays untouched.

## Stack & Versions

Go 1.22, standard library only plus existing deps. No new third-party packages. Providers keep their current SDK/HTTP bindings.

## Existing Code Inventory (cite everything before proposing anything)

| Concern | File | Line(s) | What exists today |
|---|---|---|---|
| Provider interface | `internal/provider/anthropic.go` | 29-33 | `Provider{ Name(); Chat(); ChatStream() }` with `ChatRequest`/`ChatResponse` types |
| Anthropic direct client | `internal/provider/anthropic.go` | 91-531 | `AnthropicProvider` with retry, cache_control breakpoints, SSE streaming |
| OpenAI-compatible client | `internal/provider/anthropic.go` | 533-984 | `OpenAICompatProvider` for OpenRouter, OpenAI, Gemini-openai-compat, xAI |
| Claude Code CLI shim | `internal/provider/claudecode.go` | 41-273 | `ClaudeCodeProvider` shells `claude --print` / worker mode |
| Codex CLI shim | `internal/provider/codex.go` | 19-200 | `CodexProvider` shells `codex exec` (reviewer + worker modes) |
| Ember managed AI | `internal/provider/ember.go` | 16-161 | `EmberProvider` over `internal/env/ember.AIClient` |
| Gemini native | `internal/provider/gemini.go` | 38-232 | `GeminiProvider` direct generateContent, reviewer floor 32k tokens |
| Model type + fallback chain | `internal/model/router.go` | 24-93 | `Provider` enum, `Routes` table with `Primary` + `FallbackChain` per `TaskType` (plan/refactor/typesafety/docs/security/architecture/devops/concurrency/review) |
| Resolve fallback walker | `internal/model/router.go` | 97-114 | `Resolve(taskType, isAvailable)` walks chain Claude → Codex → OpenRouter → DirectAPI → LintOnly |
| Cost-aware routing | `internal/model/router.go` | 119-160 | `CostAwareResolve` flips to fallback when tracker >80% consumed |
| Cross-model reviewer | `internal/model/router.go` | 164-173 | `CrossModelReviewer(executeProvider)` |
| Cache-affinity routing | `internal/model/cacherouter.go` | 26-88 | Prompt-fingerprint-based provider affinity, 30-min TTL default |
| Architect/editor pairing | `internal/model/architect.go` | 22-60 | `PipelineConfig{Architect, Editor, Reviewer}` (Aider pattern) |
| Subscription pool CRUD | `internal/pools/pools.go` | 36-655 | Persisted (`~/.stoke/pools/manifest.json`) account pools for `claude`/`codex` CLIs (host + container runtime) |
| Subscription scheduler | `internal/subscriptions/manager.go` | 51-312 | `Manager.Acquire/Release/AcquireExcluding/WaitForPool`, circuit breaker (3 fails → 5-min open), utilization-aware selection |
| Subscription drain / usage | `internal/subscriptions/drain.go`, `usage.go` | — | Rolling utilization polling, drain policy |
| Multi-provider SSE client | `internal/apiclient/client.go` | 26-69 | `DefaultConfigs[Provider]{Anthropic,OpenAI,OpenRouter}`, streaming SSE |
| Native runner `BaseURL` | `internal/engine/native_runner.go` | 23-85 | Single `BaseURL`; URL sniffing chooses `NewAnthropicProvider` vs `NewOpenAICompatProvider`; no pool |
| LiteLLM discovery | `internal/litellm/discover.go` | — | `~/.litellm/proxy.port` auto-discovery |
| Modelsource resolver | `internal/modelsource/modelsource.go` | 49-80 | Role → Source → (provider, model-id) resolution for Builder + Reviewer roles |
| Cost tracker | `internal/costtrack/tracker.go` | 14-50 | `ModelPricing` per-million table + `Tracker{OverBudget, Total, BudgetRemaining}` |
| Model-name router | `internal/provider/anthropic.go` | 987-1001 | `ResolveProvider(modelName)` picks backend by model prefix (claude/gpt/grok/slash) |

**Design-decision anchors from `CLAUDE.md`**: #11 cross-model review, #15 `Primary → FallbackChain` walker, #27 Codex/Claude parity on `RunResult` fields, #20 pre-execute budget check.

**Gap**: no object mediates between a task's requirements (tools, thinking, vision, context size, max cost) and the available providers. `model.Resolve` picks a symbolic `Provider` but the caller still hand-constructs the concrete `provider.Provider` elsewhere (native_runner BaseURL sniff at 73-85, model-name sniff at `ResolveProvider` 987-1001, modelsource's Builder/Reviewer resolver). Nothing emits a uniform `provider.call.complete` event. Nothing cost-aggregates per provider. `Pool.Acquire` exists for subscription CLIs but not for API providers, and capability negotiation is absent everywhere.

## Existing Patterns to Follow

- Provider interface shape: `internal/provider/anthropic.go:29-33`
- Registry-by-name init pattern (already used for plan Ecosystems): `internal/plan/integrity_*.go` each `init()` calls `plan.RegisterEcosystem()`; see `native_runner.go:363` `runEcosystemGate` for the dispatch loop
- Fallback walker: `internal/model/router.go:97-114`
- Manager acquire/release + circuit breaker: `internal/subscriptions/manager.go:66-203`
- Config YAML parsing: `internal/config/config.go` (`verificationExplicit` bool pattern from `CLAUDE.md` design #22)
- Event emission to bus: `internal/hub/bus.go` + `internal/bus/bus.go` (`spec-2`)

## Boundaries — What NOT To Do

- Do NOT modify `internal/provider/*.go` shapes — the new `providerpool.Provider` WRAPS existing clients via adapters in the new package
- Do NOT touch `internal/subscriptions/manager.go` — subscription-CLI pooling stays as-is; the new pool calls into it through an adapter for Claude/Codex subscription capacity
- Do NOT introduce new vendor SDKs — reuse existing HTTP clients in `internal/provider/`
- Do NOT change `BaseURL` on `engine.NativeRunner` — add a parallel `PoolOverride` field; keep legacy path default-on when `STOKE_PROVIDER_POOL` unset
- Do NOT hand-roll an auth manager — delegate to `internal/pools/` for subscription flows and env vars for API keys
- Do NOT build multi-tenant billing (belongs in CloudSwarm)
- Do NOT re-implement prompt caching — adapters pass `CacheEnabled` through unchanged

## Data Models

### `providerpool.Request` (new)

Wraps `provider.ChatRequest` plus routing hints.

| Field | Type | Constraints | Default |
|---|---|---|---|
| Chat | `provider.ChatRequest` | required | — |
| TaskType | `model.TaskType` | required | — |
| Role | `string` | one of: `worker`, `reviewer`, `research-lead`, `research-subagent`, `embedder`, `repair`, `judge` | `worker` |
| Requirements | `Requirements` | may be zero-value | see below |
| Fingerprint | `string` | optional; pool computes if empty | auto |
| Budget | `*costtrack.Tracker` | optional budget gate | nil |

### `providerpool.Requirements` (new)

| Field | Type | Meaning |
|---|---|---|
| NeedsTools | `bool` | Provider must support tool_use blocks |
| NeedsThinking | `bool` | Provider must support extended-thinking (Opus 4.7, o-series) |
| NeedsVision | `bool` | Provider must support image content blocks |
| NeedsStreaming | `bool` | Must implement ChatStream (not just Chat) |
| Min1MContext | `bool` | Must advertise 1M-token context window |
| MinContextTokens | `int` | Min context window (0 = any) |
| MaxCostPer1kIn | `float64` | Input-token cost ceiling (USD/1k); 0 = any |
| MaxCostPer1kOut | `float64` | Output-token cost ceiling (USD/1k); 0 = any |
| PreferredOrder | `[]string` | Explicit provider-name priority; empty = use policy table |
| Exclude | `map[string]bool` | Provider names to skip (used during failover) |

### `providerpool.CapabilitySet` (new)

```go
type CapabilitySet struct {
    Streaming       bool
    Tools           bool
    Thinking        bool
    Vision          bool
    OneMContext     bool
    MaxContext      int            // tokens
    Models          []string       // IDs this provider can serve
    Costs           map[string]CostRate // per model
}

type CostRate struct {
    InputPer1k        float64
    OutputPer1k       float64
    CacheReadPer1k    float64
    CacheWritePer1k   float64
}
```

### `providerpool.Response` (new)

Superset of `provider.ChatResponse`:

```go
type Response struct {
    Chat         *provider.ChatResponse
    ProviderName string
    Model        string
    LatencyMs    int64
    CostUSD      float64
    FailoverFrom []string // providers that failed before this one succeeded
}
```

## Provider Interface (Go)

```go
// internal/providerpool/interface.go
package providerpool

import (
    "context"
    "github.com/ericmacdougall/stoke/internal/provider"
    "github.com/ericmacdougall/stoke/internal/stream"
)

type DeltaFn func(stream.Event)

type Provider interface {
    Name() string                                         // stable identifier, e.g. "anthropic", "claude-code-subscription"
    Capabilities() CapabilitySet                          // self-declared; must be deterministic per instance
    Call(ctx context.Context, req Request) (*Response, error)
    StreamCall(ctx context.Context, req Request, onDelta DeltaFn) (*Response, error)
    HealthCheck(ctx context.Context) error                // cheap liveness probe; nil = healthy
}

type Pool interface {
    Acquire(ctx context.Context, req Request) (Provider, error)
    Release(p Provider, outcome Outcome)
    Failover(ctx context.Context, prev Provider, err error, req Request) (Provider, error)
    List() []Provider
    Stats() Stats
}

type Outcome struct {
    Success     bool
    RateLimited bool
    LatencyMs   int64
    CostUSD     float64
    Tokens      stream.TokenUsage
    Err         error
}

type Stats struct {
    PerProvider map[string]ProviderStats
}

type ProviderStats struct {
    Calls        int
    Failures     int
    TokensIn     int64
    TokensOut    int64
    CostUSD      float64
    LatencyMsP50 int64
    LatencyMsP95 int64
    CircuitOpen  bool
}
```

## Pool Implementation

### Registration (init-function pattern)

```go
// internal/providerpool/registry.go
var registry = map[string]Factory{}

type Factory func(cfg ProviderConfig) (Provider, error)

func Register(name string, f Factory) { registry[name] = f }
func MustBuild(name string, cfg ProviderConfig) Provider { ... }
```

Each backend registers itself in its own file's `init()`:

```go
// internal/providerpool/anthropic.go
func init() { Register("anthropic", newAnthropicProvider) }

// internal/providerpool/claude_code.go
func init() { Register("claude-code-subscription", newClaudeCodeProvider) }

// internal/providerpool/codex.go
func init() { Register("codex", newCodexProvider) }

// internal/providerpool/openrouter.go
func init() { Register("openrouter", newOpenRouterProvider) }

// internal/providerpool/ember.go
func init() { Register("ember", newEmberProvider) }

// internal/providerpool/gemini.go
func init() { Register("gemini", newGeminiProvider) }

// internal/providerpool/litellm.go
func init() { Register("litellm", newLiteLLMProvider) }

// internal/providerpool/local.go
func init() { Register("local", newLocalProvider) }
```

Adapters compose existing `internal/provider.*Provider` types — no duplication.

## Capability Negotiation Matrix

Matching is a pure function: `match(Requirements, CapabilitySet) bool`. All requirement fields are AND-ed; empty requirement (`false`/`0`/nil) means "don't care".

| Role | NeedsTools | NeedsThinking | NeedsVision | MinContextTokens | Typical target |
|---|---|---|---|---|---|
| `worker` (code) | ✓ | — | — | 200k | anthropic-cc-subscription → anthropic → openrouter |
| `research-lead` | ✓ | ✓ | — | 200k | anthropic:opus-4.7 → anthropic:opus-4.5 |
| `research-subagent` | ✓ | — | — | 200k | anthropic:sonnet-4.6 → openrouter:sonnet → gemini:2.5-pro |
| `reviewer` / `judge` | — | — | — | 100k | anthropic:sonnet-4.6 → codex → gemini |
| `repair` (descent T4) | ✓ | — | — | 200k | anthropic-cc-subscription → anthropic → codex |
| `embedder` | — | — | — | 8k | local:nomic-embed → anthropic:voyage-3 |
| `vision-check` | — | — | ✓ | 100k | anthropic:sonnet-4.6 → gemini:3-pro |

Pool `Acquire` algorithm:

1. Start with `req.Requirements.PreferredOrder` if non-empty, else consult policy table for `req.Role`/`req.TaskType`.
2. Skip any name in `req.Requirements.Exclude`.
3. For each candidate: check `Capabilities()` against `Requirements`; skip on mismatch.
4. If provider is circuit-open (`Stats.CircuitOpen`), skip.
5. If `req.Budget != nil` and `Budget.OverBudget()`, prefer cheaper providers (mirror `model.CostAwareResolve` at `internal/model/router.go:119`).
6. Apply cache affinity: if `req.Fingerprint` has prior successful provider in `CacheRouter` (see `internal/model/cacherouter.go:58`), bump that provider to front.
7. Return first survivor. If none, return wrapped `ErrNoProvider`.

## Failover Policy

Per-role fallback chains live in config (below) and default to the table in `internal/model/router.go:47-93` mapped onto provider-pool names. `Failover(prev, err, req)`:

| Failure signal | Action |
|---|---|
| 429 / rate_limit_exceeded | Circuit-break `prev` for 5 min (reuse subscription.Manager 5-min policy); pick next candidate with `req.Requirements.Exclude[prev.Name()]=true` |
| 5xx / 502 / 503 / 504 | Retry `prev` once with exponential backoff (already built into `AnthropicProvider.Chat` at `anthropic.go:148-171`); then failover |
| context deadline / i/o timeout | Treat as retriable (`isRetriableProviderError` at `anthropic.go:205-236`); failover after one retry |
| 401 / 403 | Hard fail; mark provider `unhealthy`; do NOT failover (credential problem won't self-heal) |
| codex: "no last agent message" | Already retried 3× inside `codex.go:65-85`; if still failing, failover |
| Empty response | Treat as retriable once; if second attempt still empty, failover |
| No candidates remaining | Return `ErrExhausted`; emit `provider.pool.exhausted` event |

Conversation-state preservation: `Request.Chat.Messages` is already the full conversation; failover just re-sends to the next provider. For `ChatStream` mid-stream failures, partial text accumulated via `DeltaFn` is discarded and the retry begins clean (tradeoff accepted — avoids model contamination).

## Cost Event Schema

Every `Call` / `StreamCall` success or terminal failure emits one event to the hub (`internal/hub/bus.go`) and to the durable bus (`internal/bus/bus.go`, `spec-2`):

```json
{
  "type": "provider.call.complete",
  "ts": "2026-04-20T15:30:00Z",
  "session_id": "…",
  "provider": "anthropic",
  "model": "claude-opus-4-7",
  "role": "research-lead",
  "task_type": "architecture",
  "tokens_in": 45123,
  "tokens_out": 2104,
  "cache_read_tokens": 40000,
  "cache_write_tokens": 0,
  "cost_usd": 0.2487,
  "latency_ms": 8423,
  "stop_reason": "end_turn",
  "failover_from": ["openrouter"],
  "outcome": "success"
}
```

Additional event types:

- `provider.pool.failover` — `{from, to, reason, attempt_n}`
- `provider.pool.exhausted` — `{task_type, tried:[provider…], last_err}`
- `provider.circuit.opened` / `provider.circuit.closed` — `{provider, reason, reopens_at}`
- `provider.health.degraded` — from `HealthCheck` background probe

Consumer: the streamjson emitter (`internal/streamjson/emitter.go`) gets a new mapper that translates `provider.call.complete` into a Claude-Code-compatible `system.api_cost` event. The cost dashboard (spec-7) tails the durable bus and aggregates.

## Config YAML Grammar

Operator-facing. Lives under `providers:` and `tasks:` keys in the existing `.stoke/config.yaml` (parsed by `internal/config/config.go`):

```yaml
providers:
  - name: anthropic
    enabled: true
    api_key_env: ANTHROPIC_API_KEY
    base_url: https://api.anthropic.com   # optional; overrides default
    models: [claude-opus-4-7, claude-sonnet-4-6, claude-haiku-4-5]
    max_rpm: 50                           # optional soft cap
    prefer_for: []                        # roles this provider wins by default
    fallback_for: []                      # roles this provider only serves as fallback

  - name: anthropic-cc-subscription
    enabled: true
    pool_source: stoke-pools               # delegates to internal/pools/ manifest
    models: [claude-opus-4-7, claude-sonnet-4-6]
    prefer_for: [worker, repair]

  - name: codex
    enabled: true
    binary: codex
    models: [gpt-5-codex, o3-codex]

  - name: openrouter
    enabled: true
    api_key_env: OPENROUTER_API_KEY
    base_url: https://openrouter.ai
    models: ["anthropic/claude-sonnet-4", "google/gemini-2.5-pro"]
    fallback_for: [research-subagent]

  - name: ember
    enabled: false
    api_key_env: EMBER_API_KEY
    base_url_env: EMBER_API_URL

  - name: gemini
    enabled: true
    api_key_env: GEMINI_API_KEY
    models: [gemini-3-pro-preview, gemini-2.5-pro, gemini-2.5-flash]

  - name: litellm
    enabled: true
    base_url_file: ~/.litellm/proxy.port    # auto-discovered
    api_key_env: LITELLM_MASTER_KEY

  - name: local
    enabled: false
    base_url: http://localhost:11434        # ollama default
    models: [nomic-embed-text, llama3.2:3b]
    prefer_for: [embedder]

tasks:
  worker.code:         [anthropic-cc-subscription, anthropic, openrouter]
  research.lead:       ["anthropic:claude-opus-4-7", "anthropic:claude-opus-4-5"]
  research.subagent:   ["anthropic:claude-sonnet-4-6", "openrouter:anthropic/claude-sonnet-4"]
  reviewer.adjudicate: ["anthropic:claude-sonnet-4-6", codex, gemini]
  repair.dispatch:     [anthropic-cc-subscription, anthropic, codex]
  embedder:            ["local:nomic-embed-text", "anthropic:voyage-3"]

pool:
  default_timeout: 30m
  health_check_interval: 5m
  circuit_open_duration: 5m
  failover_max_attempts: 4
  cache_affinity_ttl: 30m
```

Grammar rules:
- `name` unique across the list; lowercase + hyphens.
- `provider:model` entries in `tasks.*` pin model ID inside provider; bare provider name lets the provider pick from its `models` list.
- Missing `api_key_env` + missing explicit key → provider disabled with warning (not error — some providers only need CLI auth).
- `verificationExplicit` parity (per `CLAUDE.md` design #22): distinguish "empty array" from "omitted" to avoid silent "all disabled" misconfig.
- Unknown provider name → config validation error surfaced by `go vet` equivalent at load time.

## Migration Strategy

1. **Phase 1 — landed behind flag.** New `internal/providerpool/` package + `stoke providers` subcommand ships first. Default-off: `STOKE_PROVIDER_POOL=1` env or `--provider-pool` flag opts in. Legacy `engine.NativeRunner.BaseURL` sniff stays default.

2. **Phase 2 — parallel shim.** `engine.NativeRunner` gains optional `PoolOverride providerpool.Pool` field. When set (callers opt in when `STOKE_PROVIDER_POOL=1`), the runner's provider-construction block at `native_runner.go:70-85` is replaced with `pool.Acquire(ctx, req)`. Mission dispatch (`cmd/stoke/main.go`), SOW dispatch (`cmd/stoke/sow_native.go`), and research executor (RT-07) populate the override.

3. **Phase 3 — default on.** After 2 weeks of green runs with `STOKE_PROVIDER_POOL=1`, flip default and keep env var as escape hatch for rollback. `BaseURL` field on `NativeRunner` is preserved and honored when pool is absent — legacy CLI invocations keep working.

4. **Phase 4 — deprecate `model.Resolve` direct callers.** Replace call sites one-by-one with `pool.Acquire`. `model.Resolve` becomes an internal helper that builds the default `tasks.*` table. No breaking API change.

Zero forced migration for existing users. `.stoke/config.yaml` without a `providers:` block continues to work via a seed default that mirrors current behavior.

## Business Logic

### `Pool.Acquire(ctx, req)`
1. Validate: `req.Role` in known set, `req.Chat.Model` non-empty OR provider will fill default.
2. Resolve candidates: `req.Requirements.PreferredOrder` → `config.tasks[req.Role+"."+req.TaskType]` → `config.tasks[req.Role]` → default table.
3. Filter: capability match, circuit state, cost ceiling.
4. Cache affinity lookup.
5. Pick head; mark busy via internal counter (no blocking — multiple concurrent calls to same provider allowed).
6. Return provider.

### `Pool.Call` wrapper (what callers actually invoke)
1. `Acquire` → provider P.
2. `start := time.Now()`; invoke `P.Call(ctx, req)`.
3. On error: `Failover` loop with `Exclude` accumulating; give up after `failover_max_attempts`.
4. On success: compute `LatencyMs`, lookup pricing via `costtrack.ModelPricing`, compute `CostUSD`, emit `provider.call.complete`, update Stats, `Release(P, Outcome)`.
5. Return `Response`.

### `Pool.Failover(prev, err, req)`
1. Classify err (see Failover Policy table).
2. Tripping circuit: update internal state + emit `provider.circuit.opened`.
3. Add `prev.Name()` to `req.Requirements.Exclude`.
4. Call `Acquire` again.
5. Emit `provider.pool.failover`.

### Cost computation
`costUSD = (tokensIn/1000)*CostRate.InputPer1k + (tokensOut/1000)*CostRate.OutputPer1k + cache deltas`. Model-level costs come from the provider's own `Capabilities().Costs[modelID]`; fall back to `costtrack.ModelPricing` when the provider doesn't self-declare.

## Error Handling

| Failure | Strategy | User sees |
|---|---|---|
| No provider matches requirements | Return `ErrNoProvider{role, requirements}`; do not block | CLI error message + `providers list` hint |
| All providers circuit-open | Wait `pool.circuit_open_duration` via `time.Ticker`; retry once | `provider.pool.exhausted` event; non-zero exit |
| Budget gate fires mid-task | `Failover` picks cheapest remaining; on full exhaustion returns `ErrOverBudget` | Stream result with `error_max_budget_usd` subtype |
| HealthCheck consistently fails | Mark unhealthy, emit `provider.health.degraded`; auto-retry probe every `health_check_interval` | `stoke providers list` shows ✖ |
| Config references unknown provider | Fail `providerpool.BuildFromConfig` at startup | `stoke run` exits non-zero with path:line |
| `anthropic-cc-subscription` has zero pools | Provider disabled on startup with warning | `stoke providers list` shows disabled + reason |

## Testing

### `internal/providerpool/capability_test.go`
- [ ] Happy `TestCapabilityMatch`: `Requirements{NeedsTools:true, NeedsThinking:true}` against capability set with both → match=true
- [ ] Negative `TestCapabilityMatch_MissingThinking`: requirements with `NeedsThinking:true`, capability has only Tools → match=false
- [ ] Zero-value requirements match any capability set
- [ ] Cost-ceiling filter: `MaxCostPer1kIn=0.002` rejects Opus (0.015) accepts Sonnet (0.003)
- [ ] `Min1MContext:true` only matches providers advertising it

### `internal/providerpool/failover_test.go`
- [ ] `TestFailover`: stub provider A errors 429 → pool picks B, emits `failover` event, A is circuit-open
- [ ] `TestFailover_CircuitCloses`: after `circuit_open_duration` elapses (fake clock), A becomes eligible again
- [ ] `TestFailover_HardFail401`: 401 does NOT failover; returns immediately
- [ ] `TestFailover_Exhaustion`: all N providers fail → returns `ErrExhausted` with chain
- [ ] `TestFailover_PreservesMessages`: second provider receives byte-identical `req.Chat.Messages`

### `internal/providerpool/cost_test.go`
- [ ] `TestCostTracking`: 2 calls to provider `a`, 1 call to `b`; `Stats.PerProvider[a].Calls==2`, costs aggregate
- [ ] `TestCostTracking_EmitsEvent`: hub subscriber receives `provider.call.complete` with correct `tokens_in/tokens_out/cost_usd`
- [ ] `TestCostTracking_BudgetGate`: `Tracker.OverBudget()=true` → pool routes to cheaper provider per policy
- [ ] Cache-read pricing applied: usage with `CacheRead=10000` cuts cost vs fresh input

### `internal/providerpool/registry_test.go`
- [ ] All eight expected providers register via `init()`; `List()` returns them
- [ ] Disabled providers in config skipped; `List()` omits them
- [ ] `MustBuild` of unknown name panics in test, errors in prod

### `internal/providerpool/integration_test.go`
- [ ] `TestConfigRoundTrip`: parse sample YAML → build pool → names/models/flags survive
- [ ] `TestPrefFor`: provider `prefer_for:[worker]` is head of worker candidate list
- [ ] `TestFallbackFor`: provider `fallback_for:[research-subagent]` is last in candidate list
- [ ] `TestCacheAffinity`: same fingerprint in 2 successive Acquires pins same provider

### `cmd/stoke/providers_test.go`
- [ ] `stoke providers list` prints JSON with name/enabled/models/last-cost
- [ ] `stoke providers test <name>` runs `HealthCheck` and prints pass/fail

## Acceptance Criteria

- WHEN a task requests `NeedsThinking:true` and the PreferredOrder has a non-thinking provider first, THE SYSTEM SHALL skip that provider and pick the next thinking-capable one without error.
- WHEN the primary provider returns 429, THE SYSTEM SHALL open a 5-minute circuit breaker, emit `provider.circuit.opened`, and transparently re-dispatch the same `Request` to the next eligible provider within `failover_max_attempts` attempts.
- WHEN a provider call completes, THE SYSTEM SHALL emit exactly one `provider.call.complete` event with `tokens_in`, `tokens_out`, `cost_usd`, `latency_ms`, `provider`, `model`, `failover_from` fields populated.
- WHEN `STOKE_PROVIDER_POOL` is unset, THE SYSTEM SHALL bypass the pool entirely and use the legacy `engine.NativeRunner.BaseURL` path with byte-identical behavior to the prior release.
- WHEN operator config lists an unknown provider name, THE SYSTEM SHALL fail at startup with the offending path:line — never silently at dispatch time.
- WHEN `costtrack.Tracker.OverBudget()` returns true, THE SYSTEM SHALL prefer the cheapest provider that still satisfies `Requirements` (mirroring `model.CostAwareResolve`).
- WHEN `stoke providers list` runs, THE SYSTEM SHALL print one row per configured provider with enabled/healthy/last-cost/circuit-state.
- WHEN `stoke providers test <name>` runs, THE SYSTEM SHALL invoke that provider's `HealthCheck` and exit 0 on nil error, 1 otherwise.

### Bash gate (CI-runnable)
```bash
go build ./cmd/stoke
go vet ./internal/providerpool/...
go test ./internal/providerpool/... -run TestCapabilityMatch
go test ./internal/providerpool/... -run TestFailover
go test ./internal/providerpool/... -run TestCostTracking
go test ./internal/providerpool/... -run TestConfigRoundTrip
go test ./internal/providerpool/... -run TestCacheAffinity
STOKE_PROVIDER_POOL=1 ./stoke run --dry-run "hello" | jq -e '.type'
./stoke providers list | jq -e '.[] | select(.name == "anthropic")'
./stoke providers test anthropic
./stoke providers test local || true     # ok to fail when no local model present
STOKE_PROVIDER_POOL=0 ./stoke run --dry-run "hello" | jq -e '.type'   # legacy path still works
```

## Implementation Checklist

1. [ ] Create `internal/providerpool/interface.go` with `Provider`, `Pool`, `Request`, `Requirements`, `Response`, `CapabilitySet`, `CostRate`, `Stats`, `Outcome`, `DeltaFn` exactly as specified above. No adapters yet. Include the `ErrNoProvider`, `ErrExhausted`, `ErrOverBudget` sentinel errors. `go vet ./internal/providerpool/...` clean.
2. [ ] Create `internal/providerpool/registry.go` with `Register(name, Factory)`, `MustBuild`, `Build(name, cfg)`, and the package-level `registry` map. Add `init_order_test.go` asserting registration is reproducible.
3. [ ] Create `internal/providerpool/match.go` implementing `func matches(r Requirements, c CapabilitySet) bool`. Unit test every field in isolation + one composite. Cover zero-value = any.
4. [ ] Create `internal/providerpool/pool.go` with default `Pool` implementation: internal mutex-guarded state (`stats map[string]*ProviderStats`, `circuit map[string]time.Time`, `cacheRouter *model.CacheRouter`). Implement `Acquire`, `Release`, `Failover`, `List`, `Stats`. Wire `model.CacheRouter` from `internal/model/cacherouter.go:38` for fingerprint affinity.
5. [ ] Create `internal/providerpool/call.go` with helper `func Call(ctx, p Pool, req Request) (*Response, error)` that implements the acquire → invoke → failover loop → release → emit-event flow. This is the single entry point callers use; it keeps `Pool.Acquire` composable for tests.
6. [ ] Create `internal/providerpool/anthropic.go`. Adapter wraps `provider.NewAnthropicProvider` (`internal/provider/anthropic.go:101`). `Capabilities()` returns `{Streaming:true, Tools:true, Thinking: (model in opusThinkingSet), Vision:true, MaxContext:200000, Models: cfg.Models, Costs: costsFromModelPricing}`. Call `init(){ Register("anthropic", newAnthropicProvider) }`.
7. [ ] Create `internal/providerpool/claude_code.go`. Adapter wraps `provider.NewClaudeCodeProvider` (`claudecode.go:70`); caps: `{Streaming:false, Tools:true, Thinking:false, Vision:false, MaxContext:200000}`. Pulls subscription account from `internal/pools/pools.go:97` `ClaudeDirs()` + delegates capacity check to `subscriptions.Manager.Acquire`.
8. [ ] Create `internal/providerpool/codex.go`. Adapter wraps `provider.NewCodexProvider` (`codex.go:35`). Caps: `{Streaming:false, Tools:true, Thinking:true, Vision:false, MaxContext:200000}`. Register `"codex"`.
9. [ ] Create `internal/providerpool/openrouter.go`. Adapter wraps `provider.NewOpenAICompatProvider(name="openrouter", ...)` (`anthropic.go:551`). Caps: `{Streaming:true, Tools:true, Thinking:false, Vision:true, MaxContext:128000}`. Dynamically loads model list from `/api/v1/models` on first `HealthCheck`.
10. [ ] Create `internal/providerpool/ember.go`. Wraps `provider.NewEmberProvider` (`ember.go:23`). Caps: `{Streaming:true, Tools:false, Thinking:false, Vision:false}`. Register `"ember"`.
11. [ ] Create `internal/providerpool/gemini.go`. Wraps `provider.NewGeminiProvider` (`gemini.go:46`). Caps: `{Streaming:false, Tools:false, Thinking:true, Vision:true, MaxContext:1000000, OneMContext:true}`. Register `"gemini"`.
12. [ ] Create `internal/providerpool/litellm.go`. Auto-discovers base URL via `internal/litellm/discover.go`. Uses `provider.NewOpenAICompatProvider("litellm", key, url)`. Caps mirror Anthropic since the proxy typically fronts Sonnet/Opus.
13. [ ] Create `internal/providerpool/local.go`. Talks to `OLLAMA_HOST` (default `http://localhost:11434`) and llama.cpp-compatible servers via `provider.NewOpenAICompatProvider` with `chatPath="/v1/chat/completions"`. Caps: `{Streaming:true, Tools:false, Thinking:false, Vision:false}`; cost rate 0.
14. [ ] Add `internal/providerpool/config.go`: struct mirroring the YAML grammar above; `LoadFromFile(path)`, `BuildFromConfig(cfg) (Pool, error)`. Honor the `verificationExplicit` pattern. Unit test round-trip.
15. [ ] Add `internal/providerpool/events.go` with `EmitCallComplete(bus, response)`, `EmitFailover`, `EmitCircuitOpened`, `EmitExhausted`, `EmitHealthDegraded`. Wire to both `internal/hub/bus.go` and `internal/bus/bus.go`.
16. [ ] Extend `internal/engine/native_runner.go`: add optional field `PoolOverride providerpool.Pool`. When non-nil AND `STOKE_PROVIDER_POOL=1`, replace the provider-construction block (lines 70-85) with `p, err := providerpool.Call(...)`. Preserve legacy default. Add table-driven test covering both paths.
17. [ ] Add `cmd/stoke/providers.go` subcommand group: `providers list`, `providers test <name>`, `providers enable <name>`, `providers disable <name>`. `list` prints table + JSON via `--json`. `test` calls `HealthCheck`.
18. [ ] Register streamjson mapper in `internal/streamjson/emitter.go` that converts `provider.call.complete` → Claude-Code-style `system.api_cost` NDJSON line so `STOKE_PROVIDER_POOL=1 stoke run --output-format=stream-json` stays wire-compatible.
19. [ ] Write `internal/providerpool/capability_test.go`, `failover_test.go`, `cost_test.go`, `integration_test.go`, `registry_test.go` per the Testing section.
20. [ ] Write `cmd/stoke/providers_test.go` covering `list` + `test` subcommands against a fake pool.
21. [ ] Add end-to-end test `cmd/stoke/pool_smoke_test.go`: run `STOKE_PROVIDER_POOL=1 ./stoke run --dry-run "hello"`, assert stream-json output includes `provider.call.complete`.
22. [ ] Update `CLAUDE.md` package map with new `providerpool/` entry (single-line add; no other doc edits).
23. [ ] Feature-flag integration: env var `STOKE_PROVIDER_POOL=1` + CLI flag `--provider-pool` mirror each other; CLI wins. Default-off.
24. [ ] Verify CI gate: `go build ./cmd/stoke && go test ./... && go vet ./...` green at every commit. Leave `internal/provider/`, `internal/subscriptions/`, `internal/pools/`, `internal/model/` source files untouched.
