<!-- STATUS: ready -->
<!-- CREATED: 2026-05-11 -->
<!-- DEPENDS_ON: relayone-sso -->
<!-- BUILD_ORDER: 38 -->

# PostHog Product Analytics — Implementation Spec (SOW B1)

## 1. Overview

R1 today emits a rich, structured event stream through `internal/bus/` (typed event hub with WAL, hooks, and async subscribers), `internal/hub/builtin/` (in-process observers like the honesty gate and cost tracker), and `internal/telemetry/` (in-memory metrics summarizer). What it lacks is an off-host product analytics destination: there is no funnel visualisation, no cohort analysis, no per-tenant dashboarding, and no way to answer "do anti-truncation events correlate with mission completion?" without scraping WALs by hand.

This spec adds an `internal/analytics/` package that wraps the official `github.com/posthog/posthog-go` SDK (https://posthog.com/docs/libraries/go), mirrors the **DSN-aware no-op-when-empty shape of `internal/coderadar/coderadar.go`**, and registers a single non-blocking subscriber on the existing bus that maps R1 events onto a stable PostHog event taxonomy (snake_case, verb-led, present-tense, per https://posthog.com/docs/product-analytics/best-practices). Per-tenant property propagation rides through `internal/correlation/` (extended with a `TenantID` field) and PostHog **Group Analytics** (`Group("tenant", tenant_id, ...)`). Funnels and cohorts ship as importable Markdown specifications under `docs/analytics/`.

## 2. Stack & Versions

- Go 1.22+ (matches root `go.mod`).
- `github.com/posthog/posthog-go` — latest tagged release at build time (pinned via `go.mod`; default batch size 250 events, configurable flush interval). The SDK is the official Go library maintained by PostHog.
- PostHog backend: SaaS (`https://us.i.posthog.com` or `https://eu.i.posthog.com`) **or** self-hosted (same API surface). Host is configurable via `POSTHOG_HOST`.
- No new transitive heavy deps; no CGO.

## 3. Existing Patterns to Follow (REUSE — DO NOT DUPLICATE)

- **DSN-aware client shape:** `internal/coderadar/coderadar.go` — `FromEnv()` constructor, no-op when env empty, `Enabled()` predicate, panic-safe capture wrappers. Mirror that file byte-for-byte in structure.
- **In-process metrics:** `internal/metrics/metrics.go` — `Counter`, `Gauge`, `Timer`, `DefaultRegistry`. Used to expose dropped-event counters.
- **Bus subscriber pattern:** `internal/hub/builtin/cost_tracker.go` and `internal/hub/builtin/honesty.go` — `Register(bus *hub.Bus)` + `handle(ctx, ev)` shape. The new analytics subscriber follows this shape EXACTLY, registered in `Mode: ModeObserve` (never gates).
- **Bus event enum:** `internal/bus/bus.go` — events like `EvtMissionStarted`, `EvtMissionCompleted`, `EvtMissionAborted`, `EvtSkillLoaded`, `EvtBusHandlerPanic`. Reuse these — DO NOT introduce new bus events solely to feed analytics.
- **Correlation IDs:** `internal/correlation/correlation.go` — extend `IDs{}` with `TenantID string`. Update `ApplyHeaders` to set an `X-R1-Tenant-ID` header alongside session/agent/task IDs.
- **Telemetry collector (in-memory):** `internal/telemetry/collector.go` — keep as-is for in-process aggregation; the analytics package is a **separate concern** (off-host).
- **Redaction:** `internal/ledger/redact.go`, `internal/ledger/redact_sign.go` — pattern for `[REDACTED]` placeholders. Reuse the redaction-aware property scrubbing.

## 4. Library Preferences

- HTTP: posthog-go uses its own HTTP client; do not wrap.
- Validation: tag-driven; no runtime schema lib needed — properties are `map[string]any` per PostHog SDK.
- Logging: standard `log` package, prefix `"analytics:"` (matches `bus:` and `hub:` prefixes elsewhere).
- Time: `time.Time` UTC; PostHog accepts `timestamp` property when explicitly set.

## 5. Data Model — Internal Types

### `analytics.Event` (internal representation before mapping)

| Field | Type | Source | Notes |
|---|---|---|---|
| Name | string | derived from bus EventType | snake_case verb-led, e.g. `mission_completed` |
| DistinctID | string | session.UserID or `anon_<session_id>` | distinct_id required by PostHog |
| TenantID | string | `correlation.FromContext(ctx).TenantID` | empty allowed; group attached when non-empty |
| Properties | map[string]any | event-specific | redacted before send |
| Timestamp | time.Time | bus event timestamp | UTC |

### `analytics.Config`

| Field | Type | Env Var | Default |
|---|---|---|---|
| APIKey | string | `POSTHOG_API_KEY` | "" → no-op client |
| Host | string | `POSTHOG_HOST` | `https://us.i.posthog.com` |
| Environment | string | `R1_ENV` or `POSTHOG_ENV` | `development` |
| Disabled | bool | `ANALYTICS_DISABLED=1` OR `r1.policy.yaml: analytics.disabled: true` | false |
| FlushAt | int | `POSTHOG_FLUSH_AT` | 100 |
| FlushInterval | time.Duration | `POSTHOG_FLUSH_INTERVAL` | 5s |
| QueueDepth | int | `POSTHOG_QUEUE_DEPTH` | 8192 |

## 6. Event Taxonomy (the 24 events)

All events follow PostHog's `category:object_action` framework with present-tense verbs and snake_case names. **No PII in event names or properties.** Cardinality notes call out properties that should never be high-cardinality strings (use enums where shown).

### 6.1 Session lifecycle (4)

| Event | Properties (type, cardinality) |
|---|---|
| `session_started` | `session_id`(str, high), `mode`(enum: interactive/oneshot/server/d, low), `tenant_id`(str, low), `policy_phase`(enum: plan/execute/verify, low) |
| `session_resumed` | `session_id`(str, high), `prior_seq`(int), `resume_source`(enum: wal/eventlog/handoff, low) |
| `session_ended` | `session_id`(str, high), `duration_ms`(int), `reason`(enum: clean_exit/error/timeout/signal, low) |
| `session_killed` | `session_id`(str, high), `signal`(enum: SIGTERM/SIGINT/SIGKILL/panic, low), `recovered`(bool) |

### 6.2 Mission lifecycle (7)

Sourced from `internal/mission/runner.go`, `internal/mission/handlers.go`, and bus events `EvtMissionStarted`/`EvtMissionCompleted`/`EvtMissionAborted`.

| Event | Properties |
|---|---|
| `mission_started` | `mission_id`(str), `mission_kind`(enum: feature/fix/refactor/audit, low), `branch_id`(str), `plan_size_loc`(int) |
| `mission_plan_emitted` | `mission_id`, `plan_step_count`(int), `plan_token_count`(int) |
| `mission_execute_started` | `mission_id`, `worker_id`(str, mid), `lobe_count`(int) |
| `mission_verify_passed` | `mission_id`, `duration_ms`, `checks_run`(int) |
| `mission_verify_failed` | `mission_id`, `failure_class`(enum: build/test/lint/honesty/dishonest_completion, low), `attempts`(int) |
| `mission_completed` | `mission_id`, `total_duration_ms`, `total_tokens`(int), `total_cost_usd`(float) |
| `mission_aborted` | `mission_id`, `abort_reason`(enum: budget_halt/user_cancel/policy_violation/panic, low) |

### 6.3 Cortex / Lobes (3)

Sourced from `internal/cortex/lobes/` (antitrunc, clarifyq, llm, memorycurator, memoryrecall, planupdate, rulecheck, walkeeper).

| Event | Properties |
|---|---|
| `lobe_invoked` | `lobe_name`(enum from lobes dir listing, low), `model`(enum: claude-opus-4-7/sonnet/haiku, low), `input_tokens`(int), `output_tokens`(int), `latency_ms`(int), `cached_tokens`(int) |
| `cortex_round_completed` | `round_id`(str), `lobe_invocations`(int), `winner_lobe`(str, low), `convergence`(float) |
| `cortex_spotlight_changed` | `from_lobe`(str, low), `to_lobe`(str, low), `reason`(enum: scheduled/score/clarify_request, low) |

### 6.4 Anti-truncation (2)

Sourced from `internal/antitrunc/gate.go` and `internal/antitrunc/phrases.go`.

| Event | Properties |
|---|---|
| `anti_trunc_fired` | `layer`(enum: scopecheck/checklist/phrase/gate, low), `phrase_class`(enum, low — NEVER the raw phrase), `severity`(enum: warn/block, low), `mission_id`(str) |
| `anti_trunc_overridden` | `override_authority`(enum: operator/supervisor, low), `mission_id` |

### 6.5 Cost (2)

Sourced from `internal/costtrack/`.

| Event | Properties |
|---|---|
| `budget_alert_fired` | `threshold_pct`(int: 50/75/90/100), `total_usd`(float), `budget_usd`(float), `mission_id`(str) |
| `cost_event_recorded` | `model`(enum, low), `input_tokens`(int), `output_tokens`(int), `cost_usd`(float), `category`(enum: lobe/tool/cortex/system, low) |

### 6.6 Auth / B-track (3)

Sourced from B-track SSO work (`DEPENDS_ON: relayone-sso`).

| Event | Properties |
|---|---|
| `sso_login_succeeded` | `tenant_id`(str), `provider`(enum: relayone/google/github, low), `is_new_user`(bool) |
| `sso_login_failed` | `provider`(enum, low), `failure_class`(enum: invalid_token/expired/network/policy, low) |
| `session_token_refreshed` | `tenant_id`(str), `expires_in_s`(int) |

### 6.7 Tooling (3)

| Event | Properties |
|---|---|
| `tool_call_succeeded` | `tool_name`(enum: Read/Edit/Write/Bash/Glob/Grep/MCP_*, mid), `duration_ms`(int), `mission_id`(str) |
| `tool_call_failed` | `tool_name`(enum, mid), `failure_class`(enum: timeout/permission/exit_nonzero/panic, low), `mission_id` |
| `promptguard_threat_detected` | `category`(enum: prompt_injection/data_exfil/secret_leak/jailbreak, low), `severity`(enum: warn/block, low), `source`(enum: user/tool_output/file, low) |

**Total: 24 events.**

## 7. Per-Tenant Property Propagation (T3)

PostHog **Group Analytics** is the native primitive for per-tenant dashboards. The SDK supports `client.Enqueue(posthog.GroupIdentify{ Type: "tenant", Key: tenantID, Properties: ...})` and per-event `Groups: posthog.Groups{"tenant": tenantID}` (https://pkg.go.dev/github.com/posthog/posthog-go).

**Mechanism:**

1. Extend `internal/correlation/correlation.go` `IDs` struct with `TenantID string`. `WithIDs`, `FromContext`, and `ApplyHeaders` all handle the new field (no breaking change — empty TenantID is a no-op).
2. On SSO login (event `sso_login_succeeded`), the analytics package calls `client.Group("tenant", tenant_id, {plan_tier, signup_date, region})` once per session.
3. The bus subscriber pulls `correlation.FromContext(evtCtx).TenantID` and attaches it as both a top-level `tenant_id` property **and** as `Groups{"tenant": tenant_id}` on every capture. Empty tenant → omit the group binding entirely (matches no-empty-string-header policy of `correlation.ApplyHeaders`).
4. The bus does not carry context — events stored in the WAL embed `Scope` only. To bridge: the analytics subscriber consults a lightweight `internal/analytics/contextlookup.go` map keyed by `Scope.MissionID` (populated at mission start with the parent ctx tenant) so the subscriber can hydrate tenant_id at delivery time.

## 8. Bus Subscriber Wiring (T4)

New file `internal/hub/builtin/analytics_subscriber.go`:

- Implements the `hub.Subscriber` shape used by `cost_tracker.go`.
- Subscribes to **all** existing event prefixes (`session.`, `mission.`, `lobe.`, `cortex.`, `antitrunc.`, `cost.`, `auth.`, `tool.`, `promptguard.`) — but never to `bus.*` self-events (no recursion risk; the bus already protects against panics via WAL `bus.handler.panic`).
- Wraps a bounded `chan analytics.Event` (default cap = `POSTHOG_QUEUE_DEPTH` = 8192). On overflow, increments `metrics.DefaultRegistry.Counter("analytics.dropped")` and logs at most once per second. **Never blocks the bus delivery goroutine.**
- A single drainer goroutine pulls from the channel and calls `client.Enqueue(posthog.Capture{...})` (posthog-go is itself async-batched; double-buffering here protects R1's hot path from any synchronous SDK behavior — e.g. cold-start DNS lookup, allocator pressure under burst).
- Maps bus `EventType` → analytics event name via a small lookup table colocated in the same file. The table is the single canonical mapping from bus → analytics taxonomy.
- Property hydration uses a per-event-type adapter (table-driven) — the adapter extracts known fields from `evt.Payload` (JSON), applies redaction, and returns `map[string]any`.

## 9. Funnel Definitions (T5)

`docs/analytics/funnels.md` — versioned in-repo so funnel definitions are reviewable in PRs. PostHog UI imports via copy-paste of the JSON snippet at the bottom of each definition (PostHog's `/api/projects/{id}/insights/` endpoint accepts a funnel JSON body).

### 9.1 Activation funnel (target ≥40% conversion, 7-day window)

```
sso_login_succeeded
  → session_started
  → mission_started
  → mission_completed
```

Insight type: `Funnel`. Conversion window: 7 days. Breakdowns: by `mode`, by tenant group.

### 9.2 Honesty value funnel

Proves that anti-truncation actually fires inside successful missions (not just in failures).

```
mission_started
  → anti_trunc_fired (any severity)
  → mission_completed
```

If this funnel collapses (most completions had NO anti-trunc fire), it suggests anti-truncation is dormant; if step 3 rate is far below step 1 (most fires kill the mission), it suggests over-aggressive blocking.

### 9.3 Cost engagement funnel (advisory — measures budget visibility uptake)

```
session_started
  → cost_event_recorded (at least one)
  → budget_alert_fired
```

Used to gauge whether operators are pushing missions close enough to budget that alerting is meaningful.

## 10. Cohort Definitions (T6)

`docs/analytics/cohorts.md`:

- **Power users**: distinct_ids with `count(mission_completed) >= 10 in last 7d`.
- **Anti-trunc beneficiaries**: distinct_ids with `count(anti_trunc_fired) >= 3 AND count(mission_completed) >= 1 in last 14d` (anti-trunc fired AND they still completed → the gate helped).
- **At-risk**: distinct_ids with `count(mission_verify_failed) / (count(mission_verify_failed) + count(mission_verify_passed)) > 0.30 in last 14d`.
- **Cost-sensitive tenants** (group cohort, tenant): groups with `sum(cost_event_recorded.cost_usd) > $50 in last 7d`.

Each cohort spec includes the PostHog JSON body for `POST /api/projects/{id}/cohorts/`.

## 11. Privacy + Redaction Integration (T7)

- **No raw prompts, tool outputs, file paths beyond top-level repo root, or model completions ever cross the analytics boundary.** This is enforced by the property hydration adapter (table-driven allowlist — properties not listed for an event are dropped).
- **PII fields** (`user_email`, `user_name`, raw `prompt`, `completion`, full file paths) are never extracted. Distinct ID is the opaque user ID from SSO (UUID), never the email.
- **Redacted ledger nodes** (per `internal/ledger/redact_sign.go`): if an analytics event would carry content from a ledger node whose `redacted` flag is set, the property is replaced with the string `[REDACTED]`. The redaction check uses the existing `ledger.IsRedacted(nodeID)` helper.
- **Opt-out**: `r1.policy.yaml` may set `analytics.disabled: true` at the top level, which short-circuits `FromEnv()` to a no-op client. Env var `ANALYTICS_DISABLED=1` does the same. Per-tenant opt-out is delivered by setting `analytics.tenant_optouts: [tenant_uuid_1, ...]` in `r1.policy.yaml`; the subscriber checks this on every event and drops captures for matching tenants.
- **Event name hygiene**: no event name contains a user identifier, file name, or model output snippet. Reviewed in T8 unit tests (regex assertion: event names match `^[a-z][a-z0-9_]*$`).

## 12. Cloud Run Wiring (T9)

R1 ships nine Cloud Run services (per existing convention). Each service that talks to the bus needs:

- New Secret Manager secret `POSTHOG_API_KEY` (project-level, version-pinned). Reference: existing `CODERADAR_DSN` secret convention — same shape.
- Env var injection per service:
  - `r1-server`: enabled by default.
  - `r1d` (daemon): enabled by default.
  - `r1` (CLI `oneshot` mode): **disabled by default** — cold-start cost outweighs the value of a single capture. Operators can opt in with `POSTHOG_API_KEY` exported.
  - All other services: env-passthrough.
- `POSTHOG_HOST` defaults to the US PostHog SaaS host; staging clusters override to a self-hosted dev project to keep dev/prod separation.
- Cloud Build manifest update: add `POSTHOG_API_KEY` to the per-service `secrets:` mapping in `cloudbuild.yaml`.

## 13. Documentation (T10)

`docs/integrations/posthog.md` — operator runbook covering:

- How to provision a PostHog project (SaaS or self-hosted).
- Where to find the project API key.
- How to set the secret in Cloud Run.
- How to opt out per-tenant.
- Dashboard JSON files (`docs/analytics/dashboards/*.json`) and how to import via PostHog UI.
- Retention + sampling tradeoffs (PostHog default retention is 7yr SaaS / unlimited self-hosted; sampling can be set per-project).
- Troubleshooting: how to verify capture is reaching the project (look for the `analytics.captured` counter via `r1 admin metrics`).

## 14. Boundaries — What NOT To Do

- DO NOT capture PII: no user names, no emails, no raw prompts, no completions, no full file paths.
- DO NOT block hot paths: analytics calls are always async, double-buffered through a bounded channel.
- DO NOT add new bus event types **solely** for analytics — reuse the existing events listed in §6.
- DO NOT call PostHog from `--one-shot` mode by default (cold start cost; opt-in via env).
- DO NOT introduce a second observability stack — `internal/coderadar/` handles errors, `internal/telemetry/` handles in-process metrics, `internal/metrics/` handles counters/gauges. The new package is **product analytics only**.
- DO NOT mutate bus event payloads in the subscriber — read-only.
- DO NOT extend `internal/bus/` event types unless an event in §6 has no existing bus emitter (in which case, file a separate spec; do not bundle here).
- DO NOT block process shutdown waiting on flush longer than 2s — call `client.Shutdown()` with a 2s context timeout in r1's exit path.

## 15. Acceptance Criteria

- **A1** WHEN 24 distinct event types are triggered in a smoke test with `POSTHOG_API_KEY` set THEN all 24 reach the mock PostHog server with the correct `tenant_id` property.
- **A2** WHEN 10,000 events are pushed through the bus in 60s THEN process RSS overhead attributable to analytics stays under 1 MB AND dropped-event counter stays below 1% (≤100 drops).
- **A3** WHEN `analytics.disabled: true` is set in `r1.policy.yaml` THEN zero PostHog HTTP requests fire across a 100-event smoke run.
- **A4** WHEN a tenant is in `analytics.tenant_optouts` THEN zero events for that tenant reach PostHog (events for other tenants pass normally).
- **A5** WHEN funnel JSON files in `docs/analytics/funnels.md` are imported via PostHog UI THEN all three funnels render and resolve their steps against captured event names.
- **A6** WHEN `POSTHOG_API_KEY` is empty THEN `analytics.FromEnv()` returns a no-op client whose `Enabled()` is false AND zero HTTP requests fire.
- **A7** WHEN a ledger node is redacted THEN any later analytics event referencing it carries `[REDACTED]` in lieu of the redacted property.
- **A8** WHEN the process receives SIGTERM THEN the analytics client flushes within 2s OR drops remaining events with a logged count (no infinite hang).

## 16. Implementation Checklist

1. [ ] **Add `posthog-go` dependency.** Run `go get github.com/posthog/posthog-go@latest`. Pin the resolved version in `go.mod` and `go.sum`. Verify the SDK's `posthog.NewWithConfig(apiKey, posthog.Config{Endpoint, Interval, BatchSize, Verbose})` signature against `pkg.go.dev/github.com/posthog/posthog-go`. Add a single import line; do NOT vendor a fork.

2. [ ] **Create `internal/analytics/analytics.go`.** Mirror the structure of `internal/coderadar/coderadar.go` byte-for-byte: `type Client struct { sdk posthog.Client }`, `FromEnv(serviceName string) *Client` reading `POSTHOG_API_KEY` + `POSTHOG_HOST` + `R1_ENV`, empty key returns no-op `&Client{}`, `Enabled()` returns `c.sdk != nil`. Add `func detectEnvironment() string` matching coderadar's helper. Capture/Identify/Group methods are no-ops when `!Enabled()`.

3. [ ] **Implement `Capture(ctx, event, props)`.** Pulls `correlation.FromContext(ctx)` for `SessionID` (as fallback distinct_id) and `TenantID` (as Groups). Calls `c.sdk.Enqueue(posthog.Capture{DistinctId, Event, Properties, Groups, Timestamp})`. Properties map is shallow-copied before send (avoid aliasing). On Enqueue error, log once and continue — never propagate to caller.

4. [ ] **Implement `Identify(ctx, userID, props)`.** Calls `c.sdk.Enqueue(posthog.Identify{DistinctId, Properties})`. Used by SSO login handler on `sso_login_succeeded`.

5. [ ] **Implement `Group(groupType, groupID, props)`.** Calls `c.sdk.Enqueue(posthog.GroupIdentify{Type, Key, Properties})`. Used once per session start to bind the tenant group; idempotent server-side.

6. [ ] **Implement `Shutdown(ctx)`.** Wraps `c.sdk.Close()` with the provided context (2s deadline at the caller). Returns nil if no-op client.

7. [ ] **Extend `internal/correlation/correlation.go`.** Add `TenantID string` to `IDs` struct. Update `WithIDs` empty check to include TenantID. Update `ApplyHeaders` to set `X-R1-Tenant-ID` (canonical) and `X-Stoke-Tenant-ID` (legacy dual-send during the 2026-05-23 window — matches the existing pattern in the file's docstring). Update tests in `correlation_test.go` to cover the new field including the empty case.

8. [ ] **Create `internal/analytics/taxonomy.go`.** Holds the canonical `BusToAnalytics map[bus.EventType]string` mapping from the 24 events in §6 plus a `PropertyAdapter` table — `map[bus.EventType]func(bus.Event) map[string]any` — that extracts allowlisted fields from each event's JSON payload. Each adapter is ≤20 lines. The adapter table is the SOLE place property extraction logic lives.

9. [ ] **Create `internal/analytics/redact.go`.** Wraps each property map with the redaction filter. Pulls `ledger.IsRedacted(nodeID)` for any property whose name ends in `_node_id` or equals `mission_id` when that mission has redacted-content nodes; replaces value with `"[REDACTED]"`. Includes a regex guard rejecting any property whose value contains an `@` (email-like) or matches the project's path patterns.

10. [ ] **Create `internal/hub/builtin/analytics_subscriber.go`.** Implements the `Register(bus *hub.Bus)` shape from `cost_tracker.go`. Subscribes with `Mode: hub.ModeObserve` on all 9 prefixes from §6. Owns a `chan analyticsItem` buffer of size 8192, a single drainer goroutine, and uses `metrics.DefaultRegistry.Counter("analytics.captured")` + `Counter("analytics.dropped")` for observability. On panic in the handler, recovers and logs (matches bus subscriber recovery convention — `bus.go` already records panics, so no double-report).

11. [ ] **Wire the subscriber at startup.** Edit the startup paths in `cmd/r1-server/main.go`, `cmd/r1d/main.go`, and `cmd/r1/oneshot.go` (the three known bus-bootstrapping entry points) to instantiate `analytics.FromEnv(serviceName)` and call `subscriber.Register(bus)` after the existing `CostTracker` and `HonestyGate` registrations. For `oneshot`, gate registration behind `POSTHOG_API_KEY != ""` to honor the §14 cold-start rule.

12. [ ] **Implement context-to-mission tenant lookup `internal/analytics/contextlookup.go`.** Tiny in-memory `sync.Map` keyed by `mission_id` → `tenant_id`, populated when `EvtMissionStarted` is observed (extracted from the event's emitter context via a small hook), consulted when later mission events arrive without a context tenant. Eviction on `EvtMissionCompleted` / `EvtMissionAborted`. Bounded at 10k entries with FIFO eviction as a safety net.

13. [ ] **Extend `r1.policy.yaml` schema.** Add an `analytics:` block with `disabled: bool` and `tenant_optouts: [string]`. Update the policy loader in `internal/policy/` to surface these fields (mirror the existing `phases:` parsing). Empty/missing block = analytics enabled.

14. [ ] **Hook policy into client.** `FromEnv` reads the resolved policy via the existing policy singleton, honoring `analytics.disabled`. Subscriber consults `analytics.tenant_optouts` per event and silently drops.

15. [ ] **Add Cloud Run secret references.** Edit `cloudbuild.yaml` and any `services/*.yaml` deploy manifests to inject `POSTHOG_API_KEY` from Secret Manager into `r1-server` and `r1d`. Do NOT inject into `oneshot` builds.

16. [ ] **Unit tests `internal/analytics/analytics_test.go`.**
   - `TestFromEnv_NoKey_ReturnsNoOp` — empty `POSTHOG_API_KEY` ⇒ `Enabled() == false`, all methods return nil with zero HTTP.
   - `TestCapture_PropagatesTenant` — set `TenantID` on ctx, capture, assert Groups{"tenant"} populated.
   - `TestCapture_OmitsTenantWhenEmpty` — empty TenantID ⇒ no Groups binding.
   - `TestEventNameHygiene` — every value in `BusToAnalytics` matches `^[a-z][a-z0-9_]*$`.
   - `TestPolicyOptOut` — `analytics.disabled: true` ⇒ zero captures.
   - `TestTenantOptOut` — opted-out tenant ⇒ zero captures for that tenant.

17. [ ] **Unit tests for subscriber `internal/hub/builtin/analytics_subscriber_test.go`.**
   - `TestSubscriber_DrainsUnderLoad` — push 10k events through a fake bus, assert `analytics.captured` counter ≥ 9900 (≤1% drop) AND `analytics.dropped` < 100.
   - `TestSubscriber_NonBlocking` — block the analytics client (override Enqueue with a 100ms sleep), push 1k events, assert bus Publish() returns within 50ms for the 999th publish (no back-pressure on the bus).
   - `TestSubscriber_NeverCalledForRedactedNodes` — emit a mission event referencing a redacted ledger node, assert the captured property is `"[REDACTED]"`.

18. [ ] **Integration test `internal/analytics/integration_test.go`.** Spin up an `httptest.Server` matching POST `/i/v0/e` and `/batch/`. Configure `POSTHOG_HOST` to point at it. Run a scripted bus session emitting one of each of the 24 events. Assert: (a) all 24 reach the server, (b) every captured event carries `tenant_id`, (c) the funnel-relevant subset (the 4 activation events) arrive in causal order. Use a synchronization barrier on `client.Shutdown(ctx)` before assertions.

19. [ ] **Throughput benchmark `internal/analytics/bench_test.go`.** `BenchmarkBus10kEventsPerMinute` — push 10k events over 60s through a real subscriber + mock server. Record RSS delta via `runtime.MemStats.HeapAlloc` before/after. Fail if RSS delta > 1 MB (matches §15 A2).

20. [ ] **Write `docs/analytics/funnels.md`.** Three funnels per §9, each with prose, ASCII step diagram, and a fenced JSON block ready to paste into PostHog's `Create Insight → Funnel → Import` flow.

21. [ ] **Write `docs/analytics/cohorts.md`.** Four cohorts per §10, each with prose, the underlying query in PostHog HogQL, and a fenced JSON body for `POST /api/projects/{id}/cohorts/`.

22. [ ] **Write `docs/analytics/dashboards/r1-overview.json`.** A PostHog dashboard JSON with four tiles: (1) mission completion rate, (2) anti-trunc fire rate per mission, (3) cost-per-mission p50/p90, (4) tenant active-mission count. Importable via PostHog UI `Dashboards → New → Import JSON`.

23. [ ] **Write `docs/integrations/posthog.md`.** Operator runbook per §13: provisioning, secret setup, enabling/disabling, dashboard import, troubleshooting (`r1 admin metrics | grep analytics`), retention/sampling guidance, and the per-tenant opt-out workflow.

24. [ ] **Plumb graceful shutdown.** Edit `cmd/r1-server/main.go` and `cmd/r1d/main.go` SIGTERM handlers to call `analyticsClient.Shutdown(ctx)` with a 2s deadline before bus.Close(). Test: send SIGTERM mid-burst; verify no panics, no goroutine leaks (`go test -race`), and a final flush attempt is logged.

25. [ ] **Mark spec done.** After all 24 prior items pass tests + lint, update the frontmatter to `<!-- STATUS: done -->` and add `<!-- BUILD_COMPLETED: YYYY-MM-DD -->` matching the pattern used in `specs/ledger-redaction.md`.

## 17. Open Questions / Future Work (out of scope for B1)

- Client-side analytics from the `r1-server` web UI (PostHog `posthog-js`) — defer to a follow-up; this spec is server-side only.
- Feature flags via PostHog server-side eval — possible but introduces a `personalApiKey` dependency; not required by SOW B1.
- Session replay — explicitly out of scope (PII + redaction surface too large for B1 timeline).

## 18. Estimate

1 week, single engineer. Items 1–9 ≈ 2 days (package + taxonomy + redaction). Items 10–15 ≈ 1 day (wiring + policy + secrets). Items 16–19 ≈ 1.5 days (tests + benchmark). Items 20–25 ≈ 0.5 day (docs + shutdown + spec close).
