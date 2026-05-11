<!-- STATUS: ready -->
<!-- CREATED: 2026-05-11 -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 42 -->

# Per-Tool Throttling Policy — Implementation Spec

## Overview
Declarative per-tool rate limits + burst budgets in `r1.policy.yaml`, enforced at the MCP and agentloop boundaries with two-tier (per-session + per-tenant) token buckets. Implements SOW C3. The model-initiated tool surface (38 r1.* tools across 10 categories, plus all MCP servers wired via `mcp_servers:`) is the throttling target; internal R1 calls (cortex Lobes, supervisor rules) and the `--one-shot` path are explicitly out of scope. Per-process state only — no Redis, no cross-daemon coordination, because sessions are pinned to a single daemon by D-D2 in `specs/r1d-server.md`.

## Stack & Versions
- Go 1.25.5 (existing `go.mod`)
- `golang.org/x/time/rate` — token-bucket primitive. **Decision**: chose `x/time/rate` over `go.uber.org/ratelimit` because (a) we need non-blocking `Allow()` semantics at the MCP boundary (uber-go's `Take()` blocks the caller, which would queue tool calls and defeat throttling as backpressure), (b) the existing `internal/skill/builtin/rate-limiting-patterns.md` codifies `x/time/rate` as the house default, (c) `Reserve` lets us compute a precise `retry-after` value, and (d) it is already implicitly available via the `golang.org/x` umbrella the repo uses (`x/crypto`, `x/sync`, `x/tools`, `x/term`).
- `gopkg.in/yaml.v3` — already used in `internal/config/`; same parser path.
- No Redis, no `go-redis-rate`, no Lua. Per-process buckets are sufficient per the boundaries below.

## Existing Patterns to Follow
- Policy parser: `internal/config/policy.go` — the YAML-with-fallback-line-scanner pattern. The `throttling:` block must use the same `parseXBlock` (yaml.v3) escape hatch already used for `mcp_servers` and `cortex` so it does not break the line-scanner.
- Policy validation: `internal/config/validate.go` — register a new `ValidateThrottling` called from `LoadPolicy`.
- MCP server tool-call dispatch (the boundary): `internal/mcp/codebase_server.go:1006` (`case mcpMethodToolsCall`) and `internal/mcp/lanes_server.go:1096` (same case). Both run a `switch` on `req.Method` and dispatch `HandleToolCall`. Throttle hook lives **before** `HandleToolCall`.
- Native agentloop tool execution: `internal/agentloop/loop.go:709` (`execOne` inside `executeTools`). Throttle hook lives **before** `l.handler(ctx, tc.Name, tc.Input)`.
- Per-principal scope precedent: `internal/rbac/rbac.go` — identity-to-role mapping, glob-style assignment, deny-by-default with structured `AccessDeniedError`. Throttle follows the same shape: `ThrottledError` with `Tool`, `Scope`, `Principal`, `RetryAfter` fields, plus the same "first match wins" override semantics.
- Pool + circuit-breaker rate-limit precedent: `internal/subscriptions/manager.go` — see `StatusThrottled`/`StatusExhausted`/`CircuitBreakerUntil`. Same `sync.Mutex` discipline; throttle uses `sync.Map` keyed by composite (scope, principal, tool) per Go's standard advice for read-heavy maps.
- Bus event emission: `internal/agentloop/loop.go:791` (`l.eventBus.EmitAsync(postEv)`) for the `tool.throttled` event; event-type constants in `internal/hub/events.go:37-43` (alongside `EventToolBlocked`).
- Metrics surface: `internal/metrics/metrics.go` `Counter` + `Registry.Counter()` pattern; reachable via the existing `/metrics` endpoint pattern.
- Daemon reload callback: `internal/server/jsonrpc/daemonapi_impl.go:108` (`reloadConfigFn`) — wire throttle reload into the same callback that already exists for `daemon.reload_config` (shipped in commit `ea55f6d1`).
- Coderadar: `internal/coderadar/coderadar.go` `CaptureError` / `CaptureEvent` for the observability fan-out (B3).

## Library Preferences
- Token bucket: `golang.org/x/time/rate` — add to `go.mod` `require` block alongside `golang.org/x/sync v0.20.0`. Pin to the latest stable matching Go 1.25.
- Glob matching for principal overrides: stdlib `path/filepath.Match` (already used in `internal/config/policy.go` files-protection). Do NOT pull in `gobwas/glob`.
- Concurrent map: stdlib `sync.Map`. Do NOT use `concurrent-map` or `cmap`.

## Data Models

### `throttle.Bucket`
| Field          | Type              | Constraints                                   | Default |
|----------------|-------------------|-----------------------------------------------|---------|
| `limiter`      | `*rate.Limiter`   | NOT NIL                                       | required |
| `lastAccess`   | atomic.Int64      | unix nanos; for stale-bucket GC               | `time.Now().UnixNano()` |
| `scope`        | `Scope` (enum)    | `ScopeSession` \| `ScopeTenant`               | required |
| `principal`    | string            | session_id or tenant_id                       | required |
| `tool`         | string            | canonical tool name (e.g. `r1.session.start`) | required |

### `throttle.Policy` (parsed from `r1.policy.yaml`)
| Field       | Type                              | Constraints                          | Default |
|-------------|-----------------------------------|--------------------------------------|---------|
| `Defaults`  | `ScopedLimits`                    | both per-session and per-tenant set  | hardcoded fallback (see T12) |
| `Tools`     | `map[string]ScopedLimits`         | key = canonical tool name            | empty |
| `Overrides` | `[]Override`                      | ordered; first-match-wins            | empty |

### `throttle.ScopedLimits`
| Field        | Type    | Constraints                                  | Default |
|--------------|---------|----------------------------------------------|---------|
| `PerSession` | `Limit` | required when set                            | inherit defaults |
| `PerTenant`  | `Limit` | required when set                            | inherit defaults |

### `throttle.Limit`
| Field   | Type           | Constraints                                              | Default |
|---------|----------------|----------------------------------------------------------|---------|
| `Rate`  | `rate.Limit`   | tokens/sec; parsed from `"N/{s,m,h}"` strings            | required |
| `Burst` | int            | >= 1 (rate.Limiter rejects 0)                            | required |

### `throttle.Override`
| Field        | Type     | Constraints                                          | Default |
|--------------|----------|------------------------------------------------------|---------|
| `Principal`  | string   | glob like `tenant:enterprise-*`; first match wins    | required |
| `Multiplier` | float64  | > 0; scales BOTH rate and burst                      | `1.0` |

## Policy Schema — YAML

The `r1.policy.yaml` block. NOTE the SOW item says `config.toml`; the existing parser is **YAML** (`internal/config/policy.go` + `r1.policy.yaml`, `stoke.policy.yaml`). Stay with YAML — TOML would require a new parser stack and conflict with the existing 800-line `parsePolicyYAML` line-scanner.

```yaml
throttling:
  defaults:
    per_session: { rate: "10/s", burst: 20 }
    per_tenant:  { rate: "100/s", burst: 200 }
  tools:
    browse.fetch:
      per_session: { rate: "1/s",   burst: 5 }
      per_tenant:  { rate: "30/s",  burst: 60 }
    deploy.execute:
      per_session: { rate: "1/min", burst: 3 }
      per_tenant:  { rate: "10/min", burst: 20 }
    r1.session.send:
      per_session: { rate: "20/s",  burst: 40 }
      per_tenant:  { rate: "200/s", burst: 400 }
  overrides:
    - principal: "tenant:enterprise-*"
      multiplier: 10
    - principal: "session:bench-*"
      multiplier: 100
```

Rate string grammar (parsed by `throttle.ParseRate`): `<int>/{s,m,h}` -> `rate.Limit` as `<int> / <window-seconds>`. e.g. `"1/min"` -> `rate.Limit(1.0/60.0)`.

## Boundaries — What NOT To Do
- Do NOT throttle in `--one-shot` mode (latency-critical; RelayGate owns admission control upstream per `specs/provider-pool.md`).
- Do NOT throttle internal R1 calls: cortex Lobes invoked via `internal/cortex/`, supervisor rules at `internal/supervisor/`, planner-internal calls. The hook is `tool.invocation.source == "model"` only — internal callers use a separate code path that bypasses the throttle gate.
- Do NOT add per-API-key throttling. That layer belongs to RelayGate (the upstream proxy); duplicating it here causes double-counting.
- Do NOT use Redis, etcd, or any external rate-limit store. Sessions are pinned to one daemon by D-D2; per-process buckets are correct.
- Do NOT introduce a new concurrency primitive (no goroutine-per-bucket, no channel-based limiter). `sync.Map<key, *Bucket>` is sufficient and matches Go's read-heavy-map idiom.
- Do NOT block in `Allow()` — return immediately with a `RetryAfter` value computed via `limiter.Reserve()` and then `r.Cancel()` (so we do not actually consume the reservation we are rejecting).
- Do NOT add new bus event types beyond `EventToolThrottled`; reuse the existing `hub.ToolEvent` payload shape.
- Do NOT modify the consent workflow at `internal/consent/workflow.go` — throttling is orthogonal to human-in-loop approval.

## Acceptance Criteria
- WHEN 1000 concurrent goroutines each issue 100 calls to `throttle.AllowMCP` with distinct `session_id`s THE p99 overhead per `Allow` SHALL be < 100us (bench `bench/throttling_bench.go`).
- WHEN a tool is configured with `rate: "5/s", burst: 10` THE observed admission rate over a 60-second window SHALL be within +/-5% of 5/s after the initial burst is drained.
- WHEN throttle denies a call THE `tool.throttled` event SHALL appear on the hub.Bus within 100ms (assert via timed bus subscriber in integration test).
- WHEN the principal glob `tenant:enterprise-*` is configured with `multiplier: 10` THE per-tenant rate for `tenant:enterprise-acme` SHALL be exactly 10x the default per-tenant rate (asserted in unit test against >= 10 glob cases including negative cases like `tenant:other-acme`).
- WHEN `daemon.reload_config` is invoked with a policy that changes the rate for `browse.fetch` from `1/s` to `5/s` THE next call to `AllowMCP` for `browse.fetch` SHALL use the new rate within 50ms WITHOUT resetting tokens for unrelated buckets.
- WHEN `--one-shot` is the active mode THE throttle hook SHALL be a compile-time-elided no-op (verified by a test that runs the one-shot path with a `rate: "0/s", burst: 1` policy and asserts the call still succeeds).

## Implementation Checklist

1. [ ] **T1 — `throttling:` schema in policy parser.** Add a top-level `throttling` block to `internal/config/policy.go` (`type Policy struct`). Parse via the existing yaml.v3 escape hatch — add `parseThrottlingBlock(raw []byte) (ThrottlingConfig, error)` next to `parseMCPServersBlock`/`parseCortexBlock`. Update the line-scanner in `parsePolicyYAML` to treat `section == "throttling"` as a continue-this-section sentinel (same one-liner as `mcp_servers` / `cortex` at lines 321-331). Add validation `ValidateThrottling(cfg ThrottlingConfig) error` in `internal/config/validate.go` enforcing: rate strings parse, burst >= 1, multipliers > 0, override globs compile via `filepath.Match("", principal)` smoke test. Files: `internal/config/policy.go`, `internal/config/validate.go`, `internal/config/schema.go` (add Go struct types). Tests: round-trip YAML, all rejection cases.

2. [ ] **T2 — `internal/throttle/` package skeleton.** Create `internal/throttle/throttle.go` with the public API: `type Limiter interface { AllowMCP(ctx, sessionID, tenantID, tool string) Decision; AllowAgentloop(...) Decision; Reload(cfg config.ThrottlingConfig) error }`. `type Decision struct { Allowed bool; RetryAfter time.Duration; Scope Scope; Principal string; Tool string }`. Implementation: `type limiter struct { cfg atomic.Pointer[config.ThrottlingConfig]; buckets sync.Map }`. Use atomic pointer for the policy so reload is lock-free for the read path. Tests: nil-safe smoke + interface compliance.

3. [ ] **T3 — Token-bucket primitive wiring.** Add `golang.org/x/time/rate` to `go.mod`. In `internal/throttle/bucket.go`: `func (l *limiter) bucketFor(scope Scope, principal, tool string) *rate.Limiter` — composite key `scope|principal|tool` stored in `sync.Map`. On miss, resolve the effective `Limit` (tool-specific -> defaults -> multiplier-from-overrides), construct `rate.NewLimiter(limit.Rate, limit.Burst)`, `LoadOrStore` (handle race), and stamp `lastAccess`. Concurrency note: `rate.Limiter` is itself goroutine-safe per its docs. Tests: race detector clean under 1000 concurrent `bucketFor` for the same key (only one bucket constructed).

4. [ ] **T4 — Two-tier check (`AllowMCP` body).** Compose two `Reserve()` calls (per-session AND per-tenant) atomically: reserve session first, reserve tenant second; if either has `Delay() > 0`, cancel BOTH reservations (`r.CancelAt(time.Now())`) and return `Decision{Allowed: false, RetryAfter: max(sessionDelay, tenantDelay)}`. Use `Reserve` rather than `Allow` precisely because we need to compute retry-after AND because cancellation is the only way to atomically reject without consuming tokens. Inline-document this choice; it is the load-bearing correctness invariant. Tests: per-session OK + per-tenant denied -> session tokens not consumed (the `r.Cancel()` correctness check).

5. [ ] **T5 — Rate-string parser.** `func ParseRate(s string) (rate.Limit, error)` in `internal/throttle/parse.go`. Grammar: `<int>/<unit>` where unit in `{s, sec, m, min, h, hour}`. Return `rate.Limit(n / windowSeconds)`. Reject zero (caller can express deny-all via `burst: 0` in a future iteration, not via rate). Unit-test with `1/s`, `100/min`, `5/hour`, `10 / s` (whitespace), and rejection cases (`""`, `1/`, `/s`, `-1/s`, `abc/s`, `1/day`).

6. [ ] **T6 — Override-glob matcher.** `func resolveMultiplier(principal string, overrides []Override) float64` — iterate in declaration order, first `filepath.Match(o.Principal, principal)` hit wins, return its multiplier; fallback `1.0`. Unit-test >= 10 cases: `tenant:enterprise-*` vs `tenant:enterprise-acme` (match), `tenant:enterprise-*` vs `tenant:other-acme` (no match), nested wildcards `session:bench-*-shadow` vs `session:bench-42-shadow`, escape `[` literals, empty principal, multiple overrides with first-match precedence, trailing-only wildcard, leading-only wildcard, exact match, and a deliberately malformed pattern surfaced as a startup error not a runtime fallback.

7. [ ] **T7 — MCP boundary enforcement.** In `internal/mcp/codebase_server.go` `ServeStdio` `case mcpMethodToolsCall` (line 1006) and `internal/mcp/lanes_server.go` `serveJSONRPC` `case "tools/call"` (line 1096), insert a hook BEFORE `HandleToolCall`. Read `session_id` and `tenant_id` from `params.meta.r1_session_id` / `params.meta.r1_tenant_id` (extend the existing `params.meta.r1_mcp_key` auth meta-channel — same wire location, already documented at `internal/mcp/r1_server.go:88`). On `Decision{Allowed:false}`, return MCP error result `{"isError": true, "content":[{"type":"text","text":"throttled: retry after Xs"}]}` AND set a structured `_meta.r1_error.code = "tool/throttled"` for non-LLM consumers. Wire the throttle via a `WithThrottler(l Limiter)` builder option on each server (parallels `WithAuthKey`, `WithCortex`, `WithLanesServer`). Nil throttler = open mode (matches the open-local-dev posture of `authKey == ""`). Tests: integration test in T19.

8. [ ] **T8 — agentloop boundary enforcement.** In `internal/agentloop/loop.go` `executeTools`/`execOne` (line 709-740), call `l.throttler.AllowAgentloop(ctx, sessionID, tenantID, tc.Name)` BEFORE the `start := time.Now(); content, err := l.handler(...)` block. On denial, synthesize the `tool_result` block directly: `Content: fmt.Sprintf("Throttled: retry after %.1fs. Use a different tool or wait.", decision.RetryAfter.Seconds()), IsError: true` and `continue` — the model sees a normal tool_result and can recover or wait gracefully. Add `throttler Limiter` to `Config` struct (line ~100) with nil-safe access. Plumb session/tenant IDs through `Config` — they are already available in the daemon's `agent_session.go` plumbing. Tests: integration test in T20.

9. [ ] **T9 — `EventToolThrottled` bus event.** Add `EventToolThrottled EventType = "tool.throttled"` to `internal/hub/events.go` next to `EventToolBlocked`. On every denial, the throttle hook (in both MCP and agentloop paths) emits via `bus.EmitAsync(&hub.Event{Type: EventToolThrottled, Tool: &hub.ToolEvent{Name: tool, Input: map[string]any{"scope": scope, "principal": principal, "retry_after_ms": decision.RetryAfter.Milliseconds()}}})`. Use `EmitAsync` not `Emit` — throttling must not block on a slow subscriber. Tests: subscribe to bus, deny a call, assert event arrives in < 100ms (acceptance criterion 3).

10. [ ] **T10 — Metrics counters.** Register three counters via the existing `metrics.DefaultRegistry.Counter`: `throttle.allowed.<tool>`, `throttle.denied.<tool>.session`, `throttle.denied.<tool>.tenant`. Increment in `AllowMCP`/`AllowAgentloop`. Verify they show up at the existing `/metrics` endpoint (jsonrpc `daemon.metrics` per `specs/r1d-server.md` — confirm endpoint name in `internal/server/jsonrpc/daemonapi.go` and add a test that asserts the counter appears in the response after a denied call).

11. [ ] **T11 — Coderadar observability.** In each denial path, also call `coderadar.FromEnv("r1d").CaptureEvent(ctx, "tool.throttled", map[string]any{"tool": tool, "scope": scope, "principal": principal, "retry_after_ms": ms})`. No-op when CodeRadar DSN absent (existing behavior). Honors the B3 instrumentation contract. Tests: assert call with a fake CodeRadar transport that records events.

12. [ ] **T12 — Bundled default policy.** Add `configs/policies/throttling-defaults.yaml` with a conservative baseline for all 38 r1.* tools + the known MCP tool categories: write tools (`r1.session.send`, `deploy.execute`, file-write-style) get tight buckets; read tools (`r1.session.get`, `r1.session.list`, browse.fetch-style) get loose buckets. Embed via `//go:embed configs/policies/throttling-defaults.yaml` in `internal/throttle/defaults.go` exposed as `func DefaultPolicy() config.ThrottlingConfig`. When the operator's `r1.policy.yaml` omits `throttling:` entirely, use this. When the operator provides a partial block, **deep-merge**: missing tool entries fall back to defaults; explicit tool entries override completely (no sub-field merging — too confusing). Tests: golden-file diff for the bundled YAML; merge semantics covered.

13. [ ] **T13 — Per-session injection-budget integration hook.** Public function `func (l *limiter) DropSessionTokens(sessionID string)` that walks `buckets` and for every (`ScopeSession`, `sessionID`, *) bucket calls `bucket.SetBurst(0); bucket.SetLimit(0)`. Future-coupled to A1 T5 (promptguard per-session injection-budget firing `daemon.session.kill`): when that lands, its `kill` callback also invokes `throttle.DropSessionTokens` to prevent pre-queued tool calls from racing the kill. Until A1 lands, this function is exercised by a test that simulates the callback directly. Document the coupling inline; reference `specs/promptguard.md` once it exists. Tests: drop session's tokens, assert next `AllowMCP` for that session denies with retry-after = infinity; sibling sessions unaffected.

14. [ ] **T14 — Dynamic reload via `daemon.reload_config`.** In `internal/server/jsonrpc/daemonapi_impl.go` where `reloadConfigFn` is set, the daemon's wiring (likely `cmd/r1/daemon_cmd.go` and `cmd/r1/serve_cmd.go`) must, on reload success, call `throttler.Reload(newPolicy.Throttling)`. `Reload` swaps the `atomic.Pointer[ThrottlingConfig]` and prunes stale buckets (those whose tool no longer appears in config falls back to defaults — keep the bucket, swap the limiter behind it via `SetLimit`/`SetBurst` so in-flight tokens are not lost). Add an integration test that reloads mid-flight and asserts the new rate takes effect within 50ms while a bucket that was not touched preserves its current token count.

15. [ ] **T15 — `--one-shot` bypass.** In `internal/oneshot/` (the package containing `decompose.go` and related verbs) and the `cmd/r1/oneshot_cmd.go` driver: when building the agentloop `Config`, set `Throttler: nil`. The agentloop hook is `if l.throttler != nil { ... }`. Test: run a oneshot pipeline with a deliberately strict policy and assert the call still succeeds. Document inline why: "RelayGate is upstream admission control for one-shot; doubling-up causes head-of-line blocking under load."

16. [ ] **T16 — Stale-bucket GC.** A background goroutine in the daemon (`internal/throttle/gc.go`, started by `daemon.go` alongside other long-running goroutines) every 5 minutes walks `buckets`, deletes entries where `time.Since(lastAccess) > 1 hour` AND `bucket.limiter.Tokens() >= bucket.limiter.Burst()` (i.e. bucket is full so nothing is owed to the principal). Prevents unbounded `sync.Map` growth across long-running daemons. Test: insert 100k synthetic buckets with old `lastAccess`, run GC, assert <= a few buckets remain.

17. [ ] **T17 — Unit tests: bucket math.** `internal/throttle/bucket_test.go`: (a) burst-then-deny: 5 immediate Allows succeed with `burst: 5`; 6th denies with `retry-after ~= 1/rate`. (b) refill: wait 1s after exhaustion at `rate: 1/s, burst: 1`; next Allow succeeds. (c) concurrent safety: 1000 goroutines hammer the same bucket; total allows <= `burst + rate*duration`. (d) two-tier interaction: per-session OK + per-tenant exhausted -> denied with tenant retry-after; assert no session tokens consumed (this is the `r.Cancel()` correctness check).

18. [ ] **T18 — Unit tests: policy parsing.** `internal/config/policy_throttling_test.go`: round-trip the YAML example above, assert structure. Invalid cases: malformed rate string, burst=0, multiplier <= 0, glob with bad bracket. Override ordering: first-match-wins verified in dedicated test.

19. [ ] **T19 — Integration test: MCP path.** `internal/mcp/codebase_server_throttle_test.go`: start a `CodebaseServer` in-process with `WithThrottler(...)`, drive it via stdin/stdout pipes with two `tools/call` requests in rapid succession against a 1/s-burst-1 bucket, assert the second response carries `_meta.r1_error.code == "tool/throttled"` and a retry-after value within 50ms of expected.

20. [ ] **T20 — Integration test: agentloop path.** `internal/agentloop/loop_throttle_test.go`: configure a fake tool that records calls; configure throttle at `rate: 1/s, burst: 2`; feed 5 tool-use blocks in a single turn; assert exactly 2 real calls + 3 synthetic-throttled `tool_result` blocks visible in the next-turn messages.

21. [ ] **T21 — Bench: `bench/throttling_bench.go`.** `//go:build throttle_bench`. 1000 concurrent goroutines each issuing 100 `AllowMCP` calls against a 100-session x 10-tenant x 38-tool key space. Assert p99 per `Allow` < 100us and zero data races (run with `-race`). Build tag mirrors `r1d_serve_bench_test.go` pattern at `bench/r1d_serve_bench_test.go`. Invoke `go test -tags throttle_bench -run TestThrottleHotPath ./bench -timeout=60s -race -v`.

22. [ ] **T22 — Operator runbook.** Add `docs/operations/throttling.md` (parent dir does not exist; the build step must create it — note this in the spec). Sections: "Authoring a throttling policy" (with the YAML example), "Tool tiers we ship by default" (table of all 38 r1.* tools + categories with rationale), "How to read a throttled denial" (logs / events / metrics), "Common troubleshooting" (`Why is X being throttled for tenant Y?` -> check overrides, then per-tool, then defaults; `Why did the rate not change after reload?` -> check the reload callback wired, see T14), "Interaction with --one-shot and RelayGate" (no double-throttling), "Performance characteristics" (T21 bench numbers, p99 < 100us claim with reproduction).

23. [ ] **T23 — `r1 mcp serve` flag plumbing.** Add `--no-throttle` to `cmd/r1/mcp.go` `runMCPServe` for local dev / debugging. Default off (throttle on). Document in `mcpUsage` block at line 193. Mirrors the existing `--no-cortex` flag. Tests: assert flag wiring in `cmd/r1/mcp_serve_print_tools_test.go` style.

24. [ ] **T24 — Surface throttling state to TUI.** Add an entry to `internal/mcp/r1_server_catalog.go` under `r1ClITools()` (or whichever category fits): `r1.throttle.status` tool that returns `{tool, scope, principal, available_tokens, burst, rate}` for diagnostic introspection. Surface-only; reads from the same `buckets` map. Read-only — never mutates. Lets the operator answer "why was I throttled" without grepping logs. Tests: catalog test asserts the new tool definition is present and the schema validates.

25. [ ] **T25 — Migration / rollout note in CHANGELOG.** Append to `CHANGELOG.md` under the next unreleased section: bumps `r1.policy.yaml` schema with the new `throttling:` block; defaults are conservative; operators with existing policies need no change (defaults bundled); set `R1_DISABLE_THROTTLE=1` for instant kill-switch (read in `internal/throttle/throttle.go` at startup, log a warning, install a no-op throttler). Tests: assert env-var path installs the no-op throttler.

26. [ ] **T26 — End-to-end smoke.** Add a `r1d` integration test (under `internal/daemon/` or `bench/`) that boots the daemon, sends 10 rapid `r1.session.send` calls from a single session against a deliberately strict policy, asserts: (a) some calls succeed (b) some return `tool/throttled` (c) bus shows `tool.throttled` events (d) `/metrics` counters increment (e) `r1.throttle.status` shows non-zero `denied` totals. Tag `throttle_e2e`.

## Testing — Deliverable Summary
- [ ] Happy: 1 call against `rate: 10/s, burst: 20` -> success in <100us (T21).
- [ ] Happy: 20 immediate calls against `rate: 10/s, burst: 20` -> all 20 succeed; 21st denies with retry-after ~100ms (T17a).
- [ ] Error: malformed rate string `1/day` in policy YAML -> `LoadPolicy` returns an error mentioning the offending line (T18).
- [ ] Error: tenant glob `tenant:enterprise-[` (bad bracket) -> `ValidateThrottling` returns a structured error at config-load time, not at first request (T6, T18).
- [ ] Edge: per-session OK, per-tenant denied -> call denied; retry-after = tenant value; session tokens NOT consumed (T17d — the load-bearing `r.Cancel()` correctness check).
- [ ] Edge: reload mid-flight changes `browse.fetch` rate -> new rate observed within 50ms; unrelated bucket's `Tokens()` count unchanged (T14).
- [ ] Edge: `--one-shot` with strict policy -> call succeeds; throttler is nil; zero events emitted (T15).
- [ ] Edge: 1000 concurrent goroutines on the same bucket -> race-free per `-race`; admission count <= `burst + rate*duration` (T17c).

## Error Handling
| Failure                                  | Strategy                                                                          | User Sees |
|------------------------------------------|-----------------------------------------------------------------------------------|-----------|
| Per-session bucket denies                 | Cancel tenant reservation; return `Decision{Allowed:false, Scope:Session}`        | `throttled: retry after Xs` |
| Per-tenant bucket denies                  | Cancel session reservation; return `Decision{Allowed:false, Scope:Tenant}`        | `throttled: retry after Xs` |
| Policy reload fails validation            | Keep old policy in place; log error; `daemon.reload_config` returns the error     | RPC error surfaced to operator |
| Throttle config missing entirely          | Use bundled `throttling-defaults.yaml`; log info on startup                       | Conservative defaults applied |
| `R1_DISABLE_THROTTLE=1`                   | Install no-op throttler; log warning on startup                                   | All calls succeed; nothing emitted |
| Bus emission slow (subscriber backpressure) | `EmitAsync` drops on overflow per existing bus semantics; bump `bus.subscriber.overflow` counter | No throttle-path stall |

## Library / Module Additions
- `golang.org/x/time/rate` — add to `go.mod` `require` block.
- No other new dependencies.

## Notes on the SOW vs Reality
- SOW says `config.toml`; the repo uses YAML (`r1.policy.yaml`). T1 picks YAML to avoid forking the parser stack. If TOML is mandatory, T1 must be expanded to add a TOML parser (extra ~2 days, not currently estimated).
- SOW lists "38 tools across 10 categories" — confirmed by `internal/mcp/r1_server_catalog.go:27-43`. The bundled default policy in T12 must list all 38 explicitly (do not trust glob defaults for the canonical tools — operators should see them in the YAML).
- A1 T5 (per-session injection-budget kill) is referenced but not yet implemented. T13 ships the hook; A1 wires the caller when it lands.
- B3 (CodeRadar observability fan-out) is referenced via `internal/coderadar/` which is already a thin wrapper; T11 uses its existing surface, no new B3 work required here.
