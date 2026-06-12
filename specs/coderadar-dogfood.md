<!-- STATUS: done -->
<!-- CREATED: 2026-05-11 -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 40 -->
<!-- BUILD_COMPLETED: 2026-05-12 -->
<!-- IMPLEMENTATION_NOTE_2026-05-15: Error/observability capture is real, cmd/r1 registers the canonical subscriber, and hosted coord-api telemetry now emits real /v1/track events when CODERADAR_DSN is present. The full 18-event product-analytics/browser rollout described below is still partial. See plans/TRUTH-STATE-2026-05-15.md. -->

# CodeRadar Dogfood Event Streaming — Implementation Spec (B3)

## Overview

R1 is the dogfood consumer of CodeRadar. The Go wrapper at
`internal/coderadar/coderadar.go` (DSN-aware, ~106 LOC) already exists and
is used by `cmd/r1-server/main.go` and `cmd/r1/main.go` for panic / fatal
error capture (`CaptureError`, `CaptureRecovered`). This spec is the
**wiring delta** that turns the existing client into a full canonical
event stream covering 18 R1 lifecycle events. This spec describes the
intended rollout scope; the 2026-05-15 truth-state audit found hosted
product-analytics deployment still partial, but no longer purely
theoretical: the hosted `coord-api` telemetry path now emits real
CodeRadar `/v1/track` events when `CODERADAR_DSN` is present.

The goal is end-to-end self-observability: every mission, every provider
call, every anti-truncation firing, every tool call is queryable in
CodeRadar with a correlation_id that ties back to the originating R1
session / agent / task. Naming aligns with OpenTelemetry GenAI semantic
conventions (`gen_ai.*`) where applicable but stays in snake_case dotted
form to match the existing `internal/hub/events.go` taxonomy and the
upstream CodeRadar `/v1/events` ingest schema.

## Stack & Versions

- Go 1.25.5
- CodeRadar SDK: vendored at `vendor/github.com/RelayOne/coderadar/sdks/go/coderadar`
  (`CaptureError` only — this spec adds `Emit` at the wrapper layer that POSTs
  directly to `/v1/events`)
- Existing wrapper: `internal/coderadar/coderadar.go` (DSN parse + env detection)
- Reuses: `internal/hub/` (typed event bus), `internal/bus/` (durable WAL bus),
  `internal/correlation/` (X-R1-* IDs), `internal/promptguard/` (redaction)

## Existing patterns to follow

- `internal/hub/builtin/cost_tracker.go` — sibling subscriber pattern; same
  `Register(bus *hub.Bus)` method that calls `bus.Register(hub.Subscriber{})` with the canonical subscriber config.
- `internal/bridge/cost.go` — `EmitErrorCount` + `LastEmitError` + `SetOnEmitError`
  observability pattern for emit failures. Replicate verbatim in the CodeRadar
  subscriber to satisfy the audit-cleanup convention from the bridge cost
  emit-errors fix (PR #219).
- `services/cloudbuild-deploy.yaml` — per-service `gcloud run deploy` block with
  `--set-secrets=NAME=secret-name:latest` and `--set-env-vars=R1_ENV=${_ENV}`.
  Pattern to extend with `CODERADAR_DSN` and `CODERADAR_SAMPLE_RATE`.

## Why these design choices

**Why a wrapper-level `Emit` instead of changing the upstream SDK.** The
upstream Go SDK (`vendor/github.com/RelayOne/coderadar/sdks/go/coderadar/client.go`)
exposes only `CaptureError` against `/v1/errors`. The CodeRadar ingest API
(`apps/ingest-api/src/routes/events.ts`) exposes `/v1/events` with a batched
schema (`level`, `message`, `service_name`, `environment`, `tags`,
`runtime_ctx`, etc.). We do NOT modify the vendored SDK in this spec; we add
an `Emit(ctx, eventName, props)` method directly to the R1 wrapper at
`internal/coderadar/coderadar.go` that builds the `/v1/events` batched payload.
The upstream SDK does not currently expose `Emit`; this spec adds the method at the R1 wrapper layer only. The wrapper API
stays stable across that migration because the method signature is ours.

**Why snake_case dotted names instead of pure OpenTelemetry GenAI names.**
The R1 hub taxonomy (`internal/hub/events.go`) already standardized on dotted
snake_case (`mission.created`, `cortex.lobe.started`, `model.post_call`). We
mirror that into the canonical CodeRadar event names and add OTEL semconv
attributes as event PROPS (`gen_ai.system`, `gen_ai.request.model`,
`gen_ai.operation.name`) for tools that want to consume CodeRadar via OTEL
adapters. This is the same pragmatic bridge the GenAI semconv docs recommend
for in-house taxonomies.

**Why local=no-op.** Avoids developers blowing through CodeRadar quota during
unit tests + IDE runs.

## What this spec ships

A complete, dogfood-ready event pipeline:

- 18 canonical events with schema versioning.
- A non-blocking, bounded-buffer subscriber that mirrors hub events to CodeRadar.
- Per-env config (dev/staging/prod/local) with sampling.
- Secret + Cloud Build wiring for the hosted service footprint active at deployment time.
- Privacy/redaction enforcement (allowlist + SHA256 of high-cardinality fields).
- Correlation propagation end-to-end.
- A smoke test runnable in CI per env.
- Hero dashboards + alerts documented for handoff to ops.

---

## T1. Canonical event schema

**File:** `internal/coderadar/events.go` (new)

Define a single typed envelope for every CodeRadar event:

```go
// Event is the canonical CodeRadar event envelope. All 18 R1 events
// share this shape. schema_version bumps require a coordinated change
// in the CodeRadar admin dashboard (sibling repo coderadar-admin).
type Event struct {
    Name          string         // snake_case verb-led, dotted
    SchemaVersion string         // semver, e.g. "1.0.0"
    Service       string         // e.g. r1-server, r1d, r1-cli, r1-coord-api, r1-acp, r1-a2a, r1-gateway, r1-mcp, r1-admin
    Env           string         // dev | staging | prod | local
    Timestamp     time.Time
    CorrelationID string         // from internal/correlation.FromContext
    TenantID      string         // optional
    LatencyMs     int64          // optional
    Props         map[string]any // free-form, allowlisted
}
```

The 18 canonical event names. Each entry: name | schema_version | required
props beyond the envelope | source (where it fires from).

### Service lifecycle (3)
1. `service_started` | 1.0.0 | `version`, `pid`, `commit_sha` | `cmd/*/main.go` boot.
2. `service_stopped` | 1.0.0 | `reason`, `uptime_ms` | shutdown handlers.
3. `service_health_check` | 1.0.0 | `status`, `subsystems` (map) | heartbeat goroutine (60s).

### Mission (6)
4. `mission.plan` | 1.0.0 | `mission_id`, `plan_steps` (int), `model` | `hub.EventMissionPlanDone`.
5. `mission.execute` | 1.0.0 | `mission_id`, `tasks_dispatched` (int) | `hub.EventMissionExecuteStart`.
6. `mission.verify` | 1.0.0 | `mission_id`, `verify_kind` | `hub.EventVerifyConvergenceStart`.
7. `mission.review` | 1.0.0 | `mission_id`, `reviewer` | `hub.EventVerifyCriticReview`.
8. `mission.completed` | 1.0.0 | `mission_id`, `duration_ms`, `total_cost_usd` | `hub.EventMissionConverged`.
9. `mission.aborted` | 1.0.0 | `mission_id`, `abort_reason` | `hub.EventMissionFailed` and `EventMissionCancelled` (merged into one canonical event with `abort_reason` discriminator).

### Cortex (2)
10. `cortex.lobe_invoked` | 1.0.0 | `lobe_name`, `mission_id` | `hub.EventCortexLobeStarted`.
11. `cortex.round_completed` | 1.0.0 | `round`, `notes_published` (int) | wired in cortex Workspace round-tick.

### Anti-trunc (2)
12. `antitrunc.fired` | 1.0.0 | `pattern_matched`, `phase`, `evt_id` | `internal/antitrunc/gate.go` (publish a new bus event `antitrunc.fired` from gate, then bridge).
13. `antitrunc.overridden` | 1.0.0 | `override_actor`, `justification_hash` | supervisor override path.

### Provider (4)
14. `provider.call_started` | 1.0.0 | `provider`, `model`, `gen_ai.operation.name` | `hub.EventModelPreCall`.
15. `provider.call_completed` | 1.0.0 | `provider`, `model`, `input_tokens`, `output_tokens`, `cost_usd`, `cached_tokens`, `stop_reason`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens` | `hub.EventModelPostCall`.
16. `provider.call_errored` | 1.0.0 | `provider`, `model`, `error_class`, `http_status` | `hub.EventModelError`.
17. `provider.fallback` | 1.0.0 | `from_model`, `to_model`, `reason` | `hub.EventModelFallback`.

### Tooling (1)
18. `tool.call_completed` | 1.0.0 | `tool_name`, `exit_code`, `duration_ms`, `file_path_sha256` (when applicable) | `hub.EventToolPostUse`.

Add a `CanonicalEvents []string` slice in `events.go` listing all 18 names
for use by the smoke test and the cardinality lint. Add an `AllowedProps
map[string][]string` keyed by event name for the T7 allowlist enforcement.

---

## T2. Bus subscriber

**File:** `internal/hub/builtin/coderadar_subscriber.go` (new)

Mirror the `CostTracker` shape (existing sibling at
`internal/hub/builtin/cost_tracker.go`):

```go
type CoderadarSubscriber struct {
    client     *coderadar.Client
    queue      chan coderadar.Event       // bounded buffer
    queueDepth int                        // capacity, default 4096
    sampler    *Sampler                   // T5
    redactor   *Redactor                  // T7
    emitErrors atomic.Uint64
    lastErr    atomic.Pointer[emitErr]
    onErr      atomic.Pointer[func(stage string, err error)]
    wg         sync.WaitGroup
    stop       chan struct{}
}

func NewCoderadarSubscriber(client *coderadar.Client) *CoderadarSubscriber // constructor sets queueDepth=4096, allocates sampler+redactor, starts single drain goroutine, returns ready subscriber

// Register wires this subscriber to every hub.EventType that maps to one
// of the 18 canonical events (see T1). Mode = ModeObserve (async,
// fire-and-forget). Single goroutine drains queue → coderadar.Client.Emit.
func (s *CoderadarSubscriber) Register(bus *hub.Bus) // registers a hub.ModeObserve subscriber for the EventType set declared in mapHubToCanonical

// EmitErrorCount / LastEmitError / SetOnEmitError mirror the
// internal/bridge/cost.go pattern (verbatim — see PR #219 audit-cleanup).
func (s *CoderadarSubscriber) EmitErrorCount() uint64 // returns cumulative emit failures (verbatim copy of internal/bridge/cost.go pattern)
func (s *CoderadarSubscriber) LastEmitError() (stage string, err error, when time.Time, ok bool) // returns most recent emit failure with stage tag
func (s *CoderadarSubscriber) SetOnEmitError(fn func(stage string, err error)) // installs push-callback for emit failures; nil clears
```

Key behaviors:
- **Non-blocking enqueue:** if the bounded queue is full, increment a
  dropped-event counter and call `recordEmitError("queue.full", ...)`.
  Never block the publisher (mission, provider, tool hot paths must not
  stall on observability).
- **Single drain goroutine:** batches up to 20 events / 500 ms before
  POSTing to `/v1/events` (matches the upstream batch size cap of 100,
  conservative below to keep payloads <256 KB).
- **Per-event mapping:** a private `mapHubToCanonical(ev *hub.Event) (coderadar.Event, bool)`
  function. The second return is false for events not in the canonical 18
  (subscriber is registered against the union of mapped EventTypes, so this
  is just defense-in-depth).
- **Mode:** `hub.ModeObserve` (async, fire-and-forget per `internal/hub/bus.go`
  Phase 3). Subscriber timeout for handlers is 10s upstream — well above
  the enqueue path latency.

Add an `Emit(ctx, name, props)` method to `internal/coderadar/coderadar.go`
that POSTs to `<baseURL>/events` with the upstream batch schema. This is
the only change to the existing wrapper file; the existing `CaptureError`
and `CaptureRecovered` stay untouched.

---

## T3. Per-env config (R1_ENV)

**Wire-in points (the existing r1coderadar.FromEnv calls cover only fatal-error capture; this spec extends them to register the full event subscriber):**

1. `cmd/r1-server/main.go` — line 58 already calls
   `r1coderadar.FromEnv("r1-server")`. Extend to also build the subscriber
   and register it on the hub bus:
   ```go
   cr := r1coderadar.FromEnv("r1-server")
   sub := builtin.NewCoderadarSubscriber(cr)
   sub.Register(bus)
   ```
2. `cmd/r1/main.go` — line 638 already calls `r1coderadar.FromEnv("r1")`.
   In interactive shell paths (`launchREPL`, `launchShell`, `chat`) wire the
   subscriber. In one-shot path (`--one-shot`) do NOT register the
   subscriber — that audit stream goes to RelayGate per A3 (see Boundaries).
3. `cmd/r1-acp/main.go`, `cmd/r1-a2a/main.go`, `cmd/r1-gateway/main.go`,
   `cmd/r1-mcp/main.go` — each adds `r1coderadar.FromEnv("<svc-name>")`
   and registers the subscriber on its hub bus instance.
4. `services/r1-coord-api/`, `services/r1-docs/`, `services/r1-downloads-cdn/`,
   `services/r1-admin/` — each service's `main.go` (or equivalent) initializes
   the wrapper with its own service name. The 9 service tags are:
   `r1-server`, `r1`, `r1d`, `r1-acp`, `r1-a2a`, `r1-gateway`, `r1-mcp`,
   `r1-coord-api`, plus per-deploy variants (`r1-admin`, `r1-docs`,
   `r1-downloads-cdn`). The canonical service tag is set per-binary in `main`.

**R1_ENV semantics** (already detected by `coderadar.detectEnvironment()`):
- `local` → `Enabled()` returns true but the subscriber emits to /dev/null
  (drops on enqueue with no error). Implementation: `New("", svc, "local")`
  returns the no-op client when DSN is empty, AND the subscriber checks
  `env == "local"` to skip queue allocation entirely.
- `dev` / `staging` / `prod` → live emit.

---

## T4. Secret + Cloud Build wiring

**Edit:** `services/cloudbuild-deploy.yaml`

Three secret + env additions per `gcloud run deploy` block:

```yaml
--set-env-vars=R1_ENV=${_ENV},R1_VERSION=$SHORT_SHA,CODERADAR_SAMPLE_RATE=${_CODERADAR_SAMPLE_RATE}
--set-secrets=...,CODERADAR_DSN=r1-${_ENV}-shared-CODERADAR_DSN:latest
```

Add a top-level substitution default:
```yaml
substitutions:
  _ENV: prod
  _CODERADAR_SAMPLE_RATE: "0.1"  # overridden per trigger: dev=1.0, staging=1.0, prod=0.1
```

Apply to all 4 existing service-deploy steps in
`services/cloudbuild-deploy.yaml` (coord-api, docs, downloads-cdn, admin).
For the 5 binary-package services (r1-server, r1, r1-acp, r1-a2a,
r1-gateway, r1-mcp, r1d) that ship via `cloudbuild-binaries.yaml` and
`cloudbuild-release.yaml`, ensure CODERADAR_DSN is available at the
process level when deployed downstream — those binaries pick up the env
var from systemd unit or the user's shell.

**Secret Manager naming** (matches existing convention from line 110 of
`services/cloudbuild-deploy.yaml`):
- `r1-dev-shared-CODERADAR_DSN`
- `r1-staging-shared-CODERADAR_DSN`
- `r1-prod-shared-CODERADAR_DSN`

If `CODERADAR_DSN` is already in Secret Manager per the B3 SOW note,
verify each env-scoped name exists; create the missing two as part of the
build PR's manual ops checklist (NOT in code).

---

## T5. Sampling + cardinality control

**File:** `internal/coderadar/sampler.go` (new)

`CODERADAR_SAMPLE_RATE` env, parsed as float64 [0.0, 1.0]. Defaults:
- dev: 1.0
- staging: 1.0
- prod: 0.1 for high-volume events ONLY (`provider.call_completed`,
  `tool.call_completed`), 1.0 for everything else.

```go
type Sampler struct {
    defaultRate float64
    perEvent    map[string]float64 // overrides for high-volume events
}

// HighVolumeEvents in prod: 10% sample rate.
var HighVolumeEvents = map[string]bool{
    "provider.call_completed": true,
    "tool.call_completed":     true,
}

func (s *Sampler) ShouldEmit(eventName string) bool {
    rate := s.defaultRate
    if s.perEvent != nil {
        if r, ok := s.perEvent[eventName]; ok {
            rate = r
        }
    }
    return rand.Float64() < rate
}
```

In `prod` the sampler is constructed as `{defaultRate: 1.0, perEvent:
{"provider.call_completed": 0.1, "tool.call_completed": 0.1}}`. Mission
lifecycle, anti-trunc, service lifecycle, cortex, and provider error/
fallback events stay at 1.0 regardless of env (low-volume, high-value).

**Cardinality control (SHA256 hashing):**
- `file_path` → `file_path_sha256` (32-hex SHA256). The plain path NEVER
  leaves the process.
- Raw prompts and tool inputs → never emitted (see T7).
- `error_message` → truncated to 200 chars + sha256 of full text.

---

## T6. Correlation ID propagation

The existing `internal/correlation/correlation.go` IDs (SessionID, AgentID,
TaskID) form the correlation triple. The CodeRadar `correlation_id` field
is composed:

```go
func correlationKey(ctx context.Context) string {
    ids := correlation.FromContext(ctx)
    // Prefer task > agent > session for the canonical trace key.
    // Concatenate when multiple are set so a CodeRadar query for a
    // session can use prefix match.
    parts := []string{}
    if ids.SessionID != "" { parts = append(parts, "s:"+ids.SessionID) }
    if ids.AgentID   != "" { parts = append(parts, "a:"+ids.AgentID) }
    if ids.TaskID    != "" { parts = append(parts, "t:"+ids.TaskID) }
    return strings.Join(parts, "/")
}
```

The CodeRadar event also gets each ID as a separate prop:
- `r1.session_id`, `r1.agent_id`, `r1.task_id`.

Every subscriber callsite passes `ctx` from the originating hub event; the
hub bus delivers `ctx` to `HandlerFunc` per `internal/hub/bus.go:319`. We
propagate that.

**End-to-end test (T8 covers smoke; this test is at integration level):**
`integration/coderadar_correlation_test.go` (build tag
`coderadar_integration`):
1. Submit a mission via `mission.Runner`.
2. Capture every CodeRadar emission via an httptest server that records
   all events.
3. Assert: every event tagged with the mission's correlation triple shares
   the same `r1.session_id`. At least 6 distinct event names appear
   (mission.plan, mission.execute, provider.call_started, provider.call_completed,
   tool.call_completed, mission.completed).

---

## T7. Privacy + redaction

**File:** `internal/coderadar/redactor.go` (new)

Two layers:

**Layer 1 — Allowlist (HARD enforcement).** Each canonical event has a
declared `AllowedProps map[string][]string` keyed by event name. Anything
not in the allowlist is dropped silently (not emitted, not logged with
content). Example:

```go
var AllowedProps = map[string][]string{
    "provider.call_completed": {
        "provider", "model", "input_tokens", "output_tokens", "cost_usd",
        "cached_tokens", "stop_reason", "duration_ms",
        "gen_ai.system", "gen_ai.request.model",
        "gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens",
        "gen_ai.operation.name",
    },
    "tool.call_completed": {
        "tool_name", "exit_code", "duration_ms", "file_path_sha256",
    },
    // AllowedProps for the remaining 16 canonical events follow the same pattern: each entry lists every prop the redactor will pass through. See T1 enumeration for the full prop set per event.
}
```

**Layer 2 — Promptguard scrub.** For free-form string fields that pass the
allowlist (`error_message`, `abort_reason`), run through
`internal/promptguard/promptguard.go` first. Any detected PII / secret
pattern is replaced with `[REDACTED:<class>]`. If A1 (signed redactions)
ships before this spec, switch the import to `internal/signed_redactions/`.

**Hard prohibitions** (gate test asserts these are never present in any
emission, even with malicious upstream props):
- Raw prompts.
- Raw tool inputs.
- Plain file paths (always `_sha256`-hashed).
- Anthropic/OpenAI API keys (caught by promptguard).
- `Authorization:` header values.

---

## T8. Smoke test per env

**File:** `internal/coderadar/smoke_test.go` (new, build tag `coderadar_smoke`)

```go
//go:build coderadar_smoke

package coderadar

// TestSmokeServiceStartedReachesIngest asserts a service_started event
// emitted via the live CODERADAR_DSN reaches the ingest API within 5s.
// Run via: go test -tags=coderadar_smoke ./internal/coderadar/...
```

**Makefile target:** add `smoke-coderadar` to top-level `Makefile`:
```makefile
.PHONY: smoke-coderadar
smoke-coderadar:
	@test -n "$(ENV)" || (echo "ENV=dev|staging|prod required"; exit 1)
	CODERADAR_DSN=$$(gcloud secrets versions access latest --secret=r1-$(ENV)-shared-CODERADAR_DSN) \
	R1_ENV=$(ENV) \
	go test -tags=coderadar_smoke -count=1 -timeout=30s ./internal/coderadar/...
```

**Cloud Build wiring:** add a `smoke-coderadar` step to
`services/cloudbuild-deploy.yaml`, AFTER `deploy-coord-api` and BEFORE the
final `smoke` step, that runs the Make target for `${_ENV}`. Smoke
failures must block promotion to traffic-100% but not roll back the
deploy (subscriber failures are non-critical to service availability).

The smoke assertions:
1. POST a `service_started` event via the live client.
2. Within 5s, query `/v1/events?service_name=coderadar-smoke-${_ENV}&limit=1`
   on the same DSN's project.
3. Assert response includes the event with matching `correlation_id`.

---

## T9. Dashboards

**File:** `docs/observability/coderadar-dashboards.md` (new)

Four hero dashboards, JSON exports embedded as fenced code blocks in the
doc for `coderadar-admin` to import (the sibling repo's dashboard UI
imports these JSONs directly).

1. **Mission Funnel** — Sankey from `mission.plan` → `mission.execute` →
   `mission.verify` → `mission.review` → `mission.completed` / `mission.aborted`.
   Drop rate at each transition. Time-series of throughput per env.
2. **Anti-Trunc Health** — Time-series of `antitrunc.fired` count grouped
   by `pattern_matched`. Overlay of `antitrunc.overridden` rate. Mean
   `antitrunc.fired / mission.completed` ratio.
3. **Provider SLO** — p50/p95/p99 latency for `provider.call_completed`
   (latency_ms). Error rate from `provider.call_errored`. Fallback flow
   from `provider.fallback` (Sankey: from_model → to_model).
4. **Cost vs Mission Completion** — Daily $ from
   `sum(provider.call_completed.cost_usd)` vs count of
   `mission.completed`. Per-model breakdown.

Each dashboard JSON includes the env filter and uses the CodeRadar
project's standard time-series widget (matches what `coderadar-admin/src/`
expects — verify against `coderadar-admin/src/app/(dashboard)/` shape
before exporting).

---

## T10. Alerting

**File:** `docs/observability/coderadar-alerts.md` (new)

Four alerts, defined as CodeRadar alert rule JSONs:

1. **provider_p99_slow** — `quantile(0.99, provider.call_completed.latency_ms) > 30000` over 5m → PagerDuty oncall ("R1 provider p99 > 30s").
2. **antitrunc_burst** — `rate(antitrunc.fired) > 5/min` AND `env == "prod"` → Slack `#r1-ops` informational.
3. **mission_abort_rate** — `rate(mission.aborted) / rate(mission.completed + mission.aborted) > 0.10` over 1h → PagerDuty oncall.
4. **service_health_stale** — `service_health_check` missing for any of the 9 services over 5m → PagerDuty oncall.

Each alert spec includes: severity, runbook link
(`docs/runbooks/<alert-name>.md` — runbook file created in same PR with full incident response steps), debounce
window, env filter.

---

## T11. Bus event additions for antitrunc

The current `internal/antitrunc/gate.go` returns errors but does not
publish a bus event. The CodeRadar subscriber needs `antitrunc.fired` and
`antitrunc.overridden` events on the bus to mirror. Add to
`internal/bus/bus.go`:

```go
const (
    EvtAntiTruncFired      EventType = "antitrunc.fired"
    EvtAntiTruncOverridden EventType = "antitrunc.overridden"
)
```

And update `internal/antitrunc/gate.go` to `bus.Publish(...)` on each
firing path. This is the only upstream surgery the spec requires beyond
the subscriber file itself; everything else (`mission.*`, `cortex.*`,
`provider.*`, `tool.*`) already emits on the hub bus.

---

## T12. Service lifecycle event emitters

Each of the 9 service `main.go` files needs three calls:

```go
// At successful boot:
sub.EmitLifecycle("service_started", map[string]any{
    "version":    Version,
    "pid":        os.Getpid(),
    "commit_sha": commitSHA(),
})

// On SIGTERM / SIGINT / normal exit:
defer sub.EmitLifecycle("service_stopped", map[string]any{
    "reason":    shutdownReason,
    "uptime_ms": time.Since(start).Milliseconds(),
})

// Heartbeat goroutine, every 60s:
go sub.HealthHeartbeat(ctx, 60*time.Second, subsystemProbe)
```

`EmitLifecycle` and `HealthHeartbeat` are convenience methods on
`*CoderadarSubscriber` that bypass the hub bus (lifecycle events do not
exist on the hub) and call `client.Emit` directly through the same queue.

---

## T13. Cortex round-completed wiring

`cortex.round_completed` is not in `internal/hub/events.go` today. Add it:

```go
// In internal/hub/events.go, under "// --- Cortex (9 events) ---":
EventCortexRoundCompleted EventType = "cortex.round.completed"
```

Emit from `internal/cortex/workspace.go` (or wherever the round tick lives
— grep for `func.*Round` in `internal/cortex/`) at the end of each round.

---

## T14. Idempotency + deduplication

CodeRadar accepts an optional `event_id` (UUID) per the upstream schema.
The wrapper generates one per emission via `uuid.New().String()`. On the
client side, the bounded queue de-duplicates by `event_id` within a
60-second window (sliding LRU of size 4096) to handle hub bus replay
edge cases where the same event might be delivered twice.

---

## T15. Failure isolation

The CodeRadar subscriber MUST NOT panic or deadlock the host service:

- All ingest calls wrapped in `recover()` at the drain goroutine.
- HTTP timeouts hardcoded to 5s per request (separate from the upstream
  SDK's 10s default — observability must not queue behind slow ingest).
- If `EmitErrorCount` crosses a threshold (default: 100 errors in 60s),
  the subscriber self-disables for 5 minutes (circuit-break pattern,
  similar to `internal/hub/circuit.go` `CircuitBreaker`). A
  `service_health_check` emission with `coderadar_circuit_open=true` fires
  on transition (when the circuit recloses, after the 5-min cooldown).

---

## T16. Observability of the observability layer

Expose subscriber metrics through `internal/telemetry/`:

- `coderadar_events_emitted_total` (counter, by event_name)
- `coderadar_events_dropped_total` (counter, by drop_reason: queue_full,
  sample_skip, redaction_drop)
- `coderadar_emit_errors_total` (counter, by stage: enqueue, marshal,
  http, redact)
- `coderadar_queue_depth` (gauge)

Wired into the existing telemetry collector (`internal/telemetry/collector.go`)
via tags. Surfaced through the existing `r1 ops` CLI as
`r1 ops coderadar-stats`.

---

## T17. Tests

Every file gets coverage. Test inventory:

- `internal/coderadar/events_test.go` — round-trip every canonical event
  through the redactor + sampler + Emit path against an `httptest`
  server. Assert allowlist enforcement (insert disallowed prop → asserted
  dropped). Assert sample rate (1000 emissions at 0.1 rate yields
  100±30 actual emissions).
- `internal/coderadar/redactor_test.go` — every disallowed prop class is
  dropped. Plain `file_path` is hashed. Promptguard hooks called on
  `error_message`.
- `internal/coderadar/sampler_test.go` — defaults per env, per-event
  override correctness.
- `internal/hub/builtin/coderadar_subscriber_test.go` — subscriber
  registration, queue full → drop counter increments, EmitErrorCount
  observable, SetOnEmitError callback fired.
- `integration/coderadar_correlation_test.go` (build tag
  `coderadar_integration`) — end-to-end mission → 6 event names → shared
  correlation_id (T6 above).
- `internal/coderadar/smoke_test.go` (build tag `coderadar_smoke`) — live
  ingest probe (T8 above).

`go test ./...` must remain green WITHOUT the smoke/integration build
tags (default CI run does not run them; the Cloud Build deploy step runs
the smoke tag).

---

## T18. Documentation

Update / create:

- `docs/observability/coderadar-events.md` — table of all 18 events, schema,
  required props, allowlist, sample rate, source binding.
- `docs/observability/coderadar-dashboards.md` (T9).
- `docs/observability/coderadar-alerts.md` (T10).
- `docs/observability/coderadar-runbook.md` — operator howto: rotate DSN,
  query events, disable subscriber in an emergency
  (`CODERADAR_DSN=""`), check stats via `r1 ops coderadar-stats`.
- `docs/DEPLOYMENT.md` — add a section "CodeRadar observability
  (dogfood)" pointing to the four docs above.
- `CHANGELOG.md` — entry under `[unreleased]`.

---

## Boundaries

- DO NOT emit raw prompts or tool inputs. Allowlist enforcement is the
  gate; promptguard is defense-in-depth.
- DO NOT block hot paths on emit. Subscriber is async with bounded
  buffer; drop on queue full.
- DO NOT add per-service custom event schemas. Extend the canonical 18
  (propose a new canonical event by amending T1 of this spec and bumping schema_version).
- DO NOT enable the subscriber in `--one-shot` mode by default. One-shot
  is a cold-start, single-task path; CodeRadar emit cost dominates the
  run. Audit data for one-shot flows goes to RelayGate per A3 instead.
- DO NOT modify the vendored upstream SDK
  (`vendor/github.com/RelayOne/coderadar/sdks/go/coderadar/`). All event
  ingest goes through the R1 wrapper's new `Emit` method.

---

## Acceptance criteria

- [ ] `internal/coderadar/events.go` declares 18 canonical events with
      `CanonicalEvents` slice + `AllowedProps` map exported.
- [ ] `internal/coderadar/coderadar.go` exposes `Emit(ctx, name, props)`
      that POSTs to `<baseURL>/events` and returns `error`.
- [ ] `internal/hub/builtin/coderadar_subscriber.go` registers against
      every hub.EventType that maps to one of the 18 canonical events.
- [ ] All 9 service `main.go` files initialize the wrapper AND register
      the subscriber (where applicable — `--one-shot` excluded for r1 CLI).
- [ ] `services/cloudbuild-deploy.yaml` injects `CODERADAR_DSN` and
      `CODERADAR_SAMPLE_RATE` per service per env.
- [ ] `r1-{dev,staging,prod}-shared-CODERADAR_DSN` secrets exist
      (manual ops step documented in `docs/observability/coderadar-runbook.md`).
- [ ] `make smoke-coderadar ENV=dev` passes (event reaches ingest <5s).
- [ ] Smoke passes for ENV=staging and ENV=prod via Cloud Build deploy
      pipeline.
- [ ] Integration test asserts 6+ event names share one correlation_id
      for a sample mission.
- [ ] Allowlist test asserts a disallowed prop never reaches the ingest
      httptest server.
- [ ] In prod, `provider.call_completed` and `tool.call_completed`
      sample at 10% (verified by `coderadar_events_dropped_total{drop_reason="sample_skip"}` is approximately 90% of attempted emits).
- [ ] All 4 dashboards documented in
      `docs/observability/coderadar-dashboards.md` with JSON exports.
- [ ] All 4 alerts documented in
      `docs/observability/coderadar-alerts.md` with rule JSONs.
- [ ] `go test ./...` green without smoke/integration tags.
- [ ] `go test -tags=coderadar_integration ./...` green when run with
      `CODERADAR_DSN` pointing at a local mock.
- [ ] Subscriber survives 100k events with no goroutine leak (drain
      worker exits on `Close`).
- [ ] `r1 ops coderadar-stats` displays live counters from T16.

---

## Implementation order

1. T1 (`events.go`) + T11 (antitrunc bus events) + T13 (cortex round event) — pure additions, no risk.
2. T7 (redactor) + T5 (sampler) — pure functions, full unit test coverage before wiring.
3. Wrapper `Emit` method on `internal/coderadar/coderadar.go`.
4. T2 (subscriber) — uses T1+T5+T7+wrapper.Emit.
5. T6 (correlation propagation) — verified by integration test.
6. T3 (per-env wiring) across the 9 services.
7. T12 (lifecycle events).
8. T4 (Cloud Build YAML).
9. T8 (smoke test + Make target).
10. T15 (failure isolation circuit breaker).
11. T16 (telemetry exposure).
12. T9 + T10 (dashboards + alerts).
13. T17 (full test coverage pass).
14. T18 (docs).

Each item is independently mergeable except T2 depends on T1+T5+T7. T3 can
land service-by-service.
