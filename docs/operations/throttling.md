# Per-Tool Throttling (C3)

This document is the operator runbook for the C3 per-tool throttle gate. The
implementation lives in `internal/throttle/` and is wired into the
MCP server tools/call dispatch and the native agentloop's
`executeTools` loop. The policy schema is a top-level `throttling:` block
in `r1.policy.yaml`.

Spec: [`specs/per-tool-throttling.md`](../../specs/per-tool-throttling.md).

## Authoring a throttling policy

The policy is a YAML block at the top level of `r1.policy.yaml`. Every
key is optional; the bundled defaults
(`configs/policies/throttling-defaults.yaml`) stand in for anything you
omit.

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

Rate-string grammar: `<int>/<unit>` where `<unit>` is one of `s`, `sec`,
`m`, `min`, `h`, `hour`. Whitespace around the slash is tolerated. The
rate value is converted to tokens per second internally.

## Tool tiers we ship by default

The bundled defaults assign tools to four tiers based on cost and blast
radius. Operators are expected to tune from telemetry — these are
conservative starting points, not production values for every workload.

| Tier         | Rate window (per session)         | Tool examples                                                   | Rationale                                                                            |
|--------------|-----------------------------------|-----------------------------------------------------------------|--------------------------------------------------------------------------------------|
| Write-heavy  | 1/s burst 3                       | r1.mission.create, r1.worktree.merge, r1.worktree.clean, r1.verify.* | Expensive downstream work, audit weight, or non-trivial rollback cost.                |
| Action       | 2/s burst 5                       | r1.session.start, r1.session.cancel, r1.session.resume, r1.mission.cancel, r1.cli.invoke, r1.cortex.lobe_pause, r1.lanes.kill, r1.web.navigate | Side-effectful but reversible; per-session cap protects single misbehaving agents.     |
| Compute      | 5–10/s burst 10–20                | r1.cortex.publish, r1.bus.tail, r1.bus.replay, r1.tui.snapshot, r1.web.click/fill/snapshot, r1.lanes.pin, r1.worktree.diff, r1.lanes.subscribe | Workflow-flow tools that drive iteration but should not flood a tenant.                |
| Read-mostly  | 20–30/s burst 40–60               | r1.session.send, r1.session.list/get, r1.lanes.list/get, r1.mission.list/get, r1.worktree.list, r1.bus.tail, r1.tui.* | Read/observability heavy paths; tenant cap keeps cross-session storms in check.        |

All 38 r1.* tools have an explicit entry in `throttling-defaults.yaml`
so operators see the canonical names in their policy diff. A new tool
added to the catalog without a corresponding entry falls back to
`defaults` (10/s session, 100/s tenant).

## How to read a throttled denial

A denial appears on three surfaces:

1. **Tool result block** (the LLM sees it). The text is
   `throttled: retry after Xs` and the MCP `_meta.r1_error.code` is
   `"tool/throttled"` with `retry_after_ms`, `scope`, `principal`, and
   `tool` fields.
2. **Bus event** `tool.throttled` (subscribers see it). Payload:
   `{tool, input: {scope, principal, retry_after_ms}}`. Fan out
   subscribers via `r1.bus.tail` or your dashboard.
3. **Metrics counters** on the `/metrics` endpoint
   (`daemon.metrics` RPC). Three counters per tool:
   - `throttle.allowed.<tool>` — successful admissions.
   - `throttle.denied.<tool>.session` — per-session bucket denials.
   - `throttle.denied.<tool>.tenant` — per-tenant bucket denials.

The `r1.throttle.status` diagnostic tool returns the live state of every
bucket: tool, scope, principal, available_tokens, burst, and rate. It's
read-only and safe to poll.

## Common troubleshooting

### Why is `X` being throttled for tenant `Y`?

Resolve in this order:

1. Check `throttling.overrides[]` — first matching glob wins.
2. Check `throttling.tools.<X>` for an explicit per-tool entry.
3. Fall back to `throttling.defaults.per_tenant`.

The same precedence applies to the per-session tier with `Y` replaced
by the session id.

### Why did the rate not change after `daemon.reload_config`?

- Confirm the policy path you supplied actually contains the new
  `throttling:` block (`r1 ctl daemon.reload_config --path …` echoes
  the absolute path applied).
- Validation failures preserve the OLD policy and surface as an RPC
  error. Check the daemon stderr for `throttle: reload rejected: …`.
- Buckets are updated in place — `SetLimit`/`SetBurst` swap the rate
  on the existing limiter but PRESERVE its current token count. A
  bucket that was exhausted before reload stays exhausted; the new
  rate kicks in on the refill. This is intentional: dropping tokens
  on reload would let an attacker who triggered an alert manipulate
  rates by reloading the config.

### Interaction with `--one-shot` and RelayGate

The `--one-shot` path bypasses the throttler entirely (the agentloop
Config gets a nil `Throttler` field). RelayGate is the upstream
admission-control point for that path; doubling up causes
head-of-line blocking under load and double-counting on the metrics
surface.

The `r1 mcp serve --no-throttle` flag installs `throttle.NoOpLimiter`
on the stdio MCP server for local debugging only. Production
`r1 serve` always wires a real limiter.

### Kill switch

Setting `R1_DISABLE_THROTTLE=1` at daemon start installs a no-op
limiter that admits every call. The daemon logs a warning at startup
so the configuration is discoverable in the operator's log stream.

## Performance characteristics

The hot path (`AllowMCP` / `AllowAgentloop`) is allocation-free under
steady state and uses `sync.Map` for bucket lookup. Two `Reserve`
calls + a `Cancel`-on-deny is the only synchronization overhead per
admitted call.

The acceptance criterion is **p99 < 100 µs per Allow call** under
1000 concurrent goroutines × 100 calls. Reproduce locally:

```bash
go test -tags throttle_bench -run TestThrottleHotPath \
    ./bench -timeout=60s -race -v
```

The bench reports `avg`, `p50`, `p95`, `p99`, and `max` for the full
sample set and fails if p99 is at or above 100µs. Measured p99 on a
Linux developer laptop (with `-race`): ≈30µs.

## Coexistence with the A1-T2 promptguard input gate

C3's throttle and A1-T2's tool-input promptguard validation are
sibling pre-dispatch checks. Both live on the
`mcp.PreDispatch` struct (which the MCP server's tools/call switch
invokes via `PreDispatch.Check`). The two gates evaluate in this
order:

1. **Throttle**: cheap, deterministic, does not look at the
   tool-call arguments. Denies early so the validator does not pay
   the scan cost when the call would have been rejected anyway.
2. **Input validation**: scans the args for known prompt-injection
   shapes. Runs only if the throttle gate admitted.

The two are wired independently — C3 sets `PreDispatch.Throttler`,
A1-T2 sets `PreDispatch.ValidateInput`. Neither field touches the
other.

## Files

- `internal/throttle/throttle.go` — Limiter implementation.
- `internal/throttle/bucket.go` — sync.Map-keyed bucket index.
- `internal/throttle/policy/policy.go` — Config schema + validation.
- `internal/throttle/defaults.go` — bundled YAML loader.
- `internal/throttle/gc.go` — stale-bucket sweep.
- `configs/policies/throttling-defaults.yaml` — checked-in baseline.
- `internal/mcp/predispatch.go` — shared throttle + promptguard gate.
- `internal/agentloop/loop.go` — agentloop `executeTools` hook.
- `cmd/r1/throttle_wiring.go` — daemon startup + reload glue.
- `bench/throttling_bench_test.go` — p99 < 100µs guard.
