# internal/promptcache

Section-based prompt optimizer for non-agentloop call sites (plan,
SOW, review prompts). Separates static content (instructions, repo
map) from dynamic content (task, current file), places static first
so Anthropic's prompt cache can hit on the prefix, and tracks
per-optimizer hit / miss / break counters.

This package is **not** the cache structuring used by the native
agentic loop. That lives in
[`internal/agentloop/cache.go`](../agentloop/cache.go) and is what
most of R1's LLM traffic goes through.

## What this package provides

- `Optimizer` — section registry (add static + dynamic sections with
  priorities), `Build(dynamicContent)` to produce an
  `OptimizedPrompt` with static-first ordering.
- `CacheStats` — per-optimizer counters: `Hits`, `Misses`, `Breaks`,
  `TokensSaved`, `LastBreak`, `LastBreakCause`.
- `HitRate()` — `Hits / (Hits + Misses)`.
- `EstimateSavings(inputPricePerMToken)` — modeled dollar savings
  from observed `TokensSaved` assuming a 10%-of-input cache read
  price.
- `Suggestions()` — lint-style advice (large dynamic sections,
  dynamic-before-static, total size > 100k tokens).

## Where the published savings number comes from

The `~82%` / `~80.7%` input-cost reduction language in the R1
documentation is a pricing-model projection, not a measurement from
this package. The source is
[`internal/agentloop.CacheSavingsEstimate`](../agentloop/cache.go)
running against a 20-turn Sonnet workload profile. Methodology and
reproduction path:

- [docs/benchmarks/prompt-cache.md](../../docs/benchmarks/prompt-cache.md)
- [docs/benchmarks/README.md](../../docs/benchmarks/README.md)

For live telemetry from this optimizer, read `Stats()` on the
`Optimizer` after a batch of `Build()` calls.

## Related packages

- `internal/agentloop/cache.go` — cache structuring for the native
  Messages-API loop (tools, system blocks, cache breakpoints).
- `internal/stream/cache.go` — SSE-stream accumulator of
  `cache_read_input_tokens` / `cache_creation_input_tokens` per
  request. Used by `internal/engine/claude.go` to report per-run
  cache behavior.
- `internal/microcompact` — cache-aligned context compaction so
  growing history doesn't bust the cache.
- `internal/prompt/fingerprint.go` — prompt fingerprinting used to
  detect when a fingerprint change would invalidate every warm cache.
