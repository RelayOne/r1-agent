# Prompt Cache Savings

**Scope:** how R1 structures Claude API calls for Anthropic's prompt
cache, how cache hits are tracked in the telemetry pipeline, and what
input-cost reduction the deterministic pricing model projects for
representative agentic workloads.

**Status:** the numbers on this page are **pricing-model projections**
produced by `internal/agentloop.CacheSavingsEstimate`, not live API
measurements. The "Reproducing with live telemetry" section at the
bottom of this page explains how to aggregate measured figures from
`bench/harnesses.RunResult` once you have a corpus run.

## Why prompt caching matters to R1

R1 drives one strong implementer per task through a multi-turn
PLAN → EXECUTE → VERIFY loop. Every turn re-sends the same system
prompt, tool schema, repo map, and prior turns. Without caching, a
20-turn session on Sonnet would pay full input-token price on every
single turn — and the prior-turn history grows quadratically.

Anthropic prices cached tokens at 10% of the input rate (cache reads)
with a 25% premium on the turn that first writes the cache. When the
static prefix is structured correctly, the break-even point is turn
two, and every subsequent turn pays the 10% rate for almost all of
its input.

The `internal/agentloop` package is the native Messages-API loop that
replaces `claude` / `codex` subprocess spawning. It exists precisely
because spawning a new subprocess destroys the cache: the cache is
per-process on the Anthropic side, so every spawn re-writes the whole
prefix.

## How "cache hit" is tracked in R1

R1 tracks cache behavior at two layers. Both are plumbed into the
per-run result record emitted by the benchmark harness.

### 1. `internal/agentloop/cache.go` — cache structuring

`BuildCachedSystemPrompt(static, dynamic)` splits the system prompt
into a cached static block (annotated with `cache_control: ephemeral`)
and an uncached dynamic tail. `SortToolsDeterministic` sorts tool
definitions alphabetically by name before every request — a
non-deterministic tool order busts the cache on every turn and is
the single most common cache-busting anti-pattern.

`CacheSavingsEstimate(systemTokens, toolTokens, avgTurnTokens, turns,
model)` returns the projected cost for a session both with and
without caching, using Anthropic's published pricing:

- Sonnet: $3.00 / MTok input, $3.75 / MTok cache write, $0.30 / MTok cache read
- Opus: $5.00 / MTok input, $6.25 / MTok cache write, $0.50 / MTok cache read
- Haiku: $1.00 / MTok input, $1.25 / MTok cache write, $0.10 / MTok cache read

This is the function the projection table below calls.

### 2. `internal/stream/cache.go` — runtime telemetry

`PromptCacheStats.Record(usage TokenUsage)` reads `cache_read_input_tokens`
and `cache_creation_input_tokens` off every `message_delta` event
streamed back from Anthropic. A request is counted as a "hit" when
`cache_read_input_tokens > 0`, as a "miss" when both cache fields
are zero, and as a "creation" when `cache_creation_input_tokens > 0`.

The live counters are:

- `total_requests`, `cache_hits`, `cache_misses`, `cache_creations`
- `hit_rate = cache_hits / total_requests`
- `tokens_saved` (sum of cache-read tokens)
- `estimated_saving_usd` (measured: cache-read × $2.70/MTok minus
  cache-creation × $0.75/MTok)

These same fields are populated into `bench/harnesses.RunResult` as
`CacheReadTokens` and `CacheWriteTokens`, so any corpus run produces
an auditable per-task measurement.

## What the model projects

The deterministic pricing model, evaluated against three
representative agentic-workload profiles:

| Profile                 | System tok | Tool tok | Avg turn tok | Turns | No-cache USD | Cached USD | Input cost reduction |
|-------------------------|-----------:|---------:|-------------:|------:|-------------:|-----------:|---------------------:|
| short_loop_5_turns      |      2,000 |    1,500 |          400 |     5 |      0.0645 |     0.0293 |               54.5%  |
| standard_loop_20_turns  |      4,000 |    2,000 |          600 |    20 |      0.7020 |     0.1355 |               80.7%  |
| long_loop_50_turns      |      8,000 |    2,500 |          800 |    50 |      4.5150 |     0.4607 |               89.8%  |

Model: Sonnet pricing. Opus and Haiku produce identical *percentages*
(the pricing structure is proportional) but different absolute dollars.

**Methodology footprint.** Runner:
`bench/prompt_cache/run.go` at commit
`a59c9b56bad0ce67aaeca32a54aa4f442eeb8220`. Go: 1.26.2 linux/amd64.
Date: 2026-04-23. Pricing table: as of 2026-Q1 per
`internal/agentloop/cache.go`.

### Interpretation

The standard 20-turn loop is where R1's internal targets live. The
model projects 80.7% input-cost reduction for that profile, which is
the source of the "~82% input cost reduction" language used
elsewhere in the codebase and historical design docs — see
`internal/agentloop/cache.go:25` and
`docs/history/impl-guide/01-architecture-decisions.md:283`.

**Two important caveats:**

1. The `~82%` figure is a **pricing-model projection** for a
   specific workload shape. Real sessions vary. Sessions with
   frequent cache breaks (tool-set changes mid-session, system-prompt
   reshuffling, non-deterministic prior-turn ordering) can drop to
   single-digit savings.
2. The projection assumes the cache is structured correctly. The
   whole point of `internal/agentloop/cache.go` is to make that
   assumption true for every call originating inside R1.

Measured savings vary by workload. Use the reproduction path below
to get a number for your specific mix.

## Reproducing the projection

```bash
# Text output (default):
go run ./bench/prompt_cache

# JSON for machine pipelines:
go run ./bench/prompt_cache -json

# Other model classes:
go run ./bench/prompt_cache -model opus
go run ./bench/prompt_cache -model haiku
```

Or via the top-level Makefile target once wired:

```bash
make bench-cache   # equivalent to: go run ./bench/prompt_cache
```

The runner does not make API calls. It loads the three profiles
above and invokes `agentloop.CacheSavingsEstimate` three times. Output
is deterministic — you should get the same numbers on any machine.

## Reproducing with live telemetry

To replace the model projection with an actual measured figure:

1. Run any corpus through the benchmark harness:

   ```bash
   go run ./bench/cmd/bench run --corpus ./corpus --harnesses stoke --reps 3
   ```

2. Aggregate `cache_read_tokens` and `cache_write_tokens` across the
   per-task `RunResult` records written into the report directory
   (`bench/reports/*.json`). Cross-reference against `input_tokens`
   for the denominator.

3. The measured reduction is:

   ```
   measured_reduction = 1 - (
     cache_write_tokens * 1.25 +
     cache_read_tokens  * 0.10 +
     uncached_input_tokens * 1.00
   ) / (total_input_tokens_no_cache * 1.00)
   ```

   using Sonnet-relative pricing weights. The reports/markdown
   formatter can be extended to emit this row directly.

4. Publish the measurement into this document with the corpus name,
   commit hash, model, date, and per-profile figures. Do **not**
   overwrite the projection table — add a separate "Measured" table
   below it so the distinction stays visible.

## Related code

- `internal/agentloop/cache.go` — cache structuring, savings model.
- `internal/promptcache/optimizer.go` — the section-based optimizer
  used for non-agentloop prompts (plan / SOW / review). Tracks
  its own hit/miss/break counters.
- `internal/stream/cache.go` — live cache-stats accumulator fed by
  the SSE stream parser.
- `internal/engine/claude.go` — subprocess-mode cache stat plumbing
  (see `// Track prompt cache stats for cost optimization reporting`).
- `bench/harnesses/iface.go` — `RunResult.CacheReadTokens` /
  `RunResult.CacheWriteTokens`.
- `bench/prompt_cache/run.go` — the runner used to produce the
  projection table above.
