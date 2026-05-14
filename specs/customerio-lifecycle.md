<!-- STATUS: done -->
<!-- CREATED: 2026-05-11 -->
<!-- BUILD_COMPLETED: 2026-05-14 -->
<!-- DEPENDS_ON: relayone-sso, posthog-analytics -->
<!-- BUILD_ORDER: 39 -->

# Customer.io Retention + Lifecycle Email Integration — Implementation Spec

## 1. Overview

R1 emits a typed bus-event stream covering session, mission, anti-truncation, and budget-alert lifecycles (see `internal/bus/bus.go` and `internal/hub/events.go`). It lacks a delivery surface for user-visible lifecycle email: welcome, activation nudges, first-mission prompts, re-engagement, and account-health touchpoints. This spec wires a Customer.io subscriber as a sibling of B1's PostHog analytics subscriber, so the six canonical lifecycle moments — `signup`, `activation`, `first_mission`, `first_completion`, `anti_trunc_fired`, `budget_alert` — drive marketing-automation flows owned by the operator's Customer.io workspace.

Scope is triggers and identity sync only. R1 does not author email copy in code, does not template, does not deliver. Customer.io owns delivery; R1 owns the event vocabulary and the first-time debounce that makes the milestone events meaningful. The package is opt-in via environment (`CUSTOMERIO_SITE_ID` + `CUSTOMERIO_API_KEY` empty → no-op client), tenant-suppressible via `r1.policy.yaml`, and user-suppressible via Customer.io's native `unsubscribed` flag — all three layers must be honored.

SOW estimate: 2 weeks. Build order 39 because both B1 (PostHog) and A2 (SSO emit) must land first: the subscriber is functionally identical in shape to the analytics one (twin pattern), and it requires real `SSOLoginSucceeded` payloads with a stable `user_id`+`email`.

## 2. Stack and Versions

- Go 1.23 (existing repo).
- Customer.io SDK: `github.com/customerio/go-customerio/v3` (Journeys Track API client). If a newer Data-Pipelines client (`github.com/customerio/cdp-analytics-go`) is generally available at build time, prefer it and adjust the wrapper signature — the spec's `Client` interface insulates callers from the choice.
- Bus: existing `internal/hub` `Bus` — same surface as `internal/hub/builtin/cost_tracker.go`. Do NOT use the lower-level `internal/bus` WAL bus; that one is for descent/worker events.
- Storage: existing SQLite layer used by `internal/wisdom/sqlite.go` and `internal/mission/store.go` (modernc.org/sqlite). The new `internal/lifecycle/flagstore.go` opens its own DB file at `~/.r1/lifecycle.db` — separate file so a wipe of lifecycle state never touches mission/wisdom.
- HTTP test mock: `net/http/httptest` (already used in `internal/coderadar`).

## 3. Existing Patterns to Follow

- DSN-aware client shape (canonical): `internal/coderadar/coderadar.go` — `New(dsn, serviceName, environment) *Client`, `FromEnv(serviceName) *Client`, `Enabled()` predicate, no-op when env empty, never panics. The lifecycle client mirrors this exactly: empty creds → `&Client{}`, every method short-circuits via `Enabled()`.
- Bus-subscriber registration: `internal/hub/builtin/cost_tracker.go` and `internal/hub/builtin/secret_scanner.go` — `Register(bus *hub.Bus)`, `Mode: hub.ModeObserve` for non-blocking observers, `Priority: 9000` (lifecycle is observe-only, low priority, never blocks). Always return `&hub.HookResponse{Decision: hub.Allow}` from the handler.
- Async delivery and dropped-event counter: `internal/hub` already provides per-subscriber goroutines with bounded channels and an overflow counter (`recordSubscriberOverflow`). Lifecycle adds a second layer: the subscriber drops events onto its own bounded channel before the SDK call, with its own counter, so a stalled Customer.io HTTP roundtrip never back-pressures the bus.
- Correlation: `internal/correlation/correlation.go` carries `SessionID/AgentID/TaskID` on context. The lifecycle subscriber attaches `task_id` and `session_id` (when present) to the Customer.io event `data` map for funnel tracing inside Customer.io segments.
- Spec frontmatter and section style: `specs/retention-policies.md`.

## 4. Library Preferences

- HTTP: `net/http` via the SDK's transport; do not hand-roll requests.
- JSON: `encoding/json` (matches every other R1 subscriber).
- Config: read env in `FromEnv`. Do NOT layer YAML on top of env — `r1.policy.yaml` only carries the per-tenant kill switch, never the credentials.
- Time: `time.Now().UTC()` for all `signup_date`-style traits.

## 5. Data Models

### 5.1 lifecycle.Client (Go)

```go
package lifecycle

type Client interface {
    Enabled() bool
    Identify(ctx context.Context, userID, email string, traits map[string]any) error
    Track(ctx context.Context, userID, event string, props map[string]any) error
    MergeIdentities(ctx context.Context, oldID, newID string) error
    Delete(ctx context.Context, userID string) error  // suppress + delete (GDPR DSAR)
}
```

A concrete `httpClient` implements `Client` using `go-customerio`. A `noopClient` returns `nil` for every method and `Enabled()=false`; constructors return `noopClient` when env is empty.

### 5.2 lifecycle.FlagStore (SQLite)

| Column         | Type    | Notes                                                       |
|----------------|---------|-------------------------------------------------------------|
| tenant_id      | TEXT    | PK part 1; empty string == single-tenant local dev          |
| event          | TEXT    | PK part 2; one of `signup`, `activation`, `first_mission`, `first_completion` |
| user_id        | TEXT    | the subject — DSAR deletes rows by this column              |
| first_fired_at | INTEGER | unix-millis; non-null implies already-fired, do not refire  |

DDL: `CREATE TABLE IF NOT EXISTS lifecycle_first_flags (tenant_id TEXT NOT NULL, event TEXT NOT NULL, user_id TEXT NOT NULL, first_fired_at INTEGER NOT NULL, PRIMARY KEY (tenant_id, event, user_id));`

Single transaction per check: `INSERT OR IGNORE ... RETURNING *` (modernc.org/sqlite supports `RETURNING`); rows-affected == 1 → first-time, rows-affected == 0 → already-fired. Atomic across replicas because SQLite holds the write lock for the duration of the statement.

### 5.3 Cloud Run / Secret Manager bindings

| Secret name             | Required? | Maps to                  |
|-------------------------|-----------|--------------------------|
| `CUSTOMERIO_SITE_ID`    | yes       | `Site-ID` HTTP basic user |
| `CUSTOMERIO_API_KEY`    | yes       | basic-auth password      |
| `CUSTOMERIO_REGION`     | optional  | `us` (default) / `eu`    |

Injected into `r1-server` only. `r1` one-shot CLI never reads them — see boundary in §11.

## 6. Implementation Detail

### 6.1 Package layout

```
internal/lifecycle/
    client.go                 // interface + FromEnv + httpClient + noopClient
    client_test.go
    flagstore.go              // SQLite-backed first-time guard
    flagstore_test.go
    region.go                 // us→track.customer.io, eu→track-eu.customer.io URL select
    traits.go                 // Build(tenant_id, plan_tier, signup_date) — PII-minimization helper
internal/hub/builtin/
    lifecycle_subscriber.go       // bus → Client adapter
    lifecycle_subscriber_test.go
docs/integrations/
    customerio.md             // operator runbook
docs/lifecycle/
    campaigns.md              // campaign catalog (operator/marketing facing, not built in code)
cmd/r1-server/
    main.go (edit)            // call lifecycle.FromEnv + register subscriber
```

### 6.2 Region selection (region.go)

| `CUSTOMERIO_REGION` | Base URL                                |
|---------------------|-----------------------------------------|
| `"" or "us"`        | `https://track.customer.io/api/v1`      |
| `"eu"`              | `https://track-eu.customer.io/api/v1`   |
| anything else       | log warning, fall back to `us`          |

Pass the URL into `customerio.NewTrackClient(siteID, apiKey, customerio.WithURL(...))`.

### 6.3 Subscriber wiring (lifecycle_subscriber.go)

The subscriber subscribes to these exact event types and maps them:

| Bus event (hub.EventType)            | Customer.io track event       | First-time milestone fired?                 |
|--------------------------------------|-------------------------------|---------------------------------------------|
| `EventSSOLoginSucceeded` (B1)        | `signup` (first time) / Identify-only otherwise | yes: `signup`             |
| `EventSessionInit`                   | `session_started`             | yes: `activation` (first after signup)      |
| `EventMissionCreated`                | `mission_started`             | yes: `first_mission`                        |
| `EventMissionConverged`              | `mission_completed`           | yes: `first_completion`                     |
| `EventAntiTruncFired` (new)          | `anti_trunc_fired`            | no                                          |
| `EventCostBudget80`/`_90`/`_Exceeded`| `budget_alert`                | no (one Customer.io event for all three thresholds) |

Notes on the mapping:

- `EventSSOLoginSucceeded` is defined in B1's spec (`specs/posthog-analytics.md`); if it lands under a different name, update the constant import here and only here.
- R1's existing `EventMissionConverged` (not `MissionCompleted`) is the canonical mission-finished-successfully event in `internal/hub/events.go`. We use it intentionally — `EventMissionCompleted` exists in `internal/bus/bus.go` (the worker-events bus) but the hub.Bus is the authoritative cross-cutting bus.
- `EventAntiTruncFired` is new — B2 itself emits no bus events, but its existence depends on the anti-truncation engine publishing one. If `internal/antitrunc/` does not already publish on the hub, add a single observe-only emit in this package's wiring step (T2.5) — see §6.7.

### 6.4 Channel and drop counter

```go
type LifecycleSubscriber struct {
    client     Client
    flags      *FlagStore
    queue      chan queuedEvent
    dropped    atomic.Uint64
    workers    int        // default 4
}
```

`queue` is buffered to 4096. `Handler` is the bus-side function: it builds a `queuedEvent` and `select`s with `default` onto `queue` — if full, increments `dropped` and returns Allow. `workers` goroutines drain `queue` and call the Customer.io SDK with a 5-second per-call context timeout. On SDK error: log + bump `dropped` (we conflate drop-on-overflow and drop-on-error; both equally mean did-not-reach-Customer.io). Surface `dropped` via the package's `Snapshot()` method.

### 6.5 First-time guard contract

```go
// IsFirstTime atomically checks-and-sets the flag for (tenant, user, event).
// Returns true exactly once across all replicas of r1-server.
// On store error returns (false, err) — the caller logs and skips the milestone
// rather than risk a double-fire.
func (f *FlagStore) IsFirstTime(ctx context.Context, tenantID, event, userID string) (bool, error)
```

Used by the subscriber for `signup`, `activation`, `first_mission`, `first_completion`. NOT used for `anti_trunc_fired` or `budget_alert` (those are recurring engagement signals).

### 6.6 Identify payload contract (PII minimization)

The `traits` map sent to Customer.io is built by `lifecycle.traits.Build(tenantID, planTier, signupDate)` and contains exactly:

```
email          string  (PII — required for delivery)
tenant_id      string  (correlation, not PII)
plan_tier      string  ("free"|"pro"|"enterprise" — derived from policy/billing)
signup_date    string  (RFC3339)
```

No display name, no IP, no user agent, no prompt content, no file paths, no ledger node IDs, no mission titles. The `props` map on `Track` may carry `mission_id`, `session_id`, `task_id`, `cost_usd` (for budget_alert), and the anti-trunc `false_completions_blocked` counter — never raw content.

### 6.7 Anti-truncation hub event (T2.5 — minor cross-cut)

If `internal/antitrunc/` does not already publish on `hub.Bus`, add the smallest possible bridge: a single `bus.Publish(&hub.Event{Type: EventAntiTruncFired, Custom: map[string]any{"false_completions_blocked": n}})` call at the exit of `antitrunc.Engine.Decide()` when the decision is block. Owner of `internal/antitrunc/` reviews this in the build. If the event already exists, reuse it and drop T2.5 from the build checklist.

## 7. Email campaigns (Customer.io UI side — documented only)

Authored by marketing inside Customer.io; this file just freezes the trigger contract. Lives in `docs/lifecycle/campaigns.md`.

| Day | Trigger / Condition                                        | Subject (working)                              | Suppression                  |
|-----|------------------------------------------------------------|------------------------------------------------|------------------------------|
| 0   | `signup` event                                             | "Welcome — how to write your first plan."      | `unsubscribed`               |
| 1   | no `activation` 24h after signup                           | "Start your first session" → platform.r1.run  | activation arrives → cancel  |
| 3   | no `first_mission` 72h after signup                        | "Examples to try"                              | first_mission → cancel       |
| 7   | no `first_completion` 7d after signup                      | "Stuck? Drop the operator a line"              | first_completion → cancel    |
| 14  | `mission_completed` count ≥ 1                              | "Power user playbook"                          | one-shot                     |
| 30  | `anti_trunc_fired` count ≥ 1 in last 30d                   | "How R1's anti-truncation saved you from N false completions" (counter-driven copy) | one-shot/month |
| --  | monthly digest (always-on for active users)                | "Your R1 month in review"                      | active = session in 30d      |
| --  | `budget_alert` event                                       | "Account budget at 80%/90%/exceeded"           | always send                  |

Each campaign documents: trigger event, conditions/cohort, copy outline, suppression rules, on-call owner (operator's marketing lead). Editing the campaign list is a marketing-side change inside Customer.io — code changes only when the trigger event itself changes (which is a R1 release).

## 8. Per-cohort retention sequences

Defined as Customer.io segments + journeys (no code):

- Power users (≥10 `mission_completed` in trailing 7d): monthly insider digest.
- At-risk (3d no `session_started` after `activation`): 3-email re-engagement over 7d; any `session_started` ends the sequence.
- Churn (14d no `session_started`): one final re-engagement.
- Cold (30d no `session_started`): suppression flag set; no further emails until user logs in (next `SSOLoginSucceeded` → call `Identify` with `cold=false` trait → Customer.io segment auto-removes).

The cold→active transition is the only one R1 code participates in: every `SSOLoginSucceeded` calls `Identify` with the full traits map, which lets the segment recompute.

## 9. Privacy / GDPR / opt-out

Three layers, all must be honored:

1. Env-level kill switch — `CUSTOMERIO_SITE_ID`/`CUSTOMERIO_API_KEY` empty → `noopClient`. Default for any deployment that hasn't been explicitly wired.
2. Tenant-level opt-out — `r1.policy.yaml` (`internal/config/policy.go`) gets a new boolean `lifecycle.disabled` (default `false`). When `true`, the subscriber's handler returns Allow without enqueueing. Honored on every call (hot-reload, no restart needed).
3. User-level opt-out — Customer.io's native `unsubscribed` attribute. Set by Customer.io when the user clicks the email's unsubscribe link; R1 never sets this directly. The SDK auto-suppresses sends to unsubscribed customers (Customer.io server-side enforces this).

DSAR flow: operator runs `r1 admin lifecycle delete --user <user_id>`. This:

1. Calls `Client.Delete(ctx, userID)` which issues `DELETE https://track.customer.io/api/v1/customers/{userID}` (suppress + delete).
2. Deletes the `(tenant_id, *, user_id)` rows from `flagstore` so a re-signup would re-fire `signup`.
3. Writes an audit-log entry to `internal/audit/` with `op=lifecycle_delete`, `user_id`, `tenant_id`, operator identity, timestamp.
4. The CLI prints: "Delete queued — Customer.io has a 24h soft-delete window before erasure; record the request ID in your DSAR tracker."

PII inventory: email is the only PII. No raw prompts, file paths, ledger content, mission titles, or task descriptions cross the wire. The `traits.Build` function is the only function permitted to populate identify-trait fields — it is the choke point and its allowlist is enforced by `traits_test.go` reflection.

## 10. Cloud Run wiring

`cmd/r1-server/main.go` gains, after the existing `coderadar.FromEnv("r1-server")` call:

```go
lifecycleClient := lifecycle.FromEnv("r1-server")
if lifecycleClient.Enabled() {
    sub, err := builtin.NewLifecycleSubscriber(lifecycleClient, lifecycleFlags, policy)
    if err != nil { return fmt.Errorf("lifecycle subscriber: %w", err) }
    sub.Register(hubBus)
    defer sub.Close()
}
```

`r1` (the CLI binary at `cmd/r1/`) is NOT edited. CLI runs are anonymous and never reach Customer.io.

Secret Manager: three Cloud Run secrets, mounted as env via the existing `cloudbuild.yaml` template (no schema change — just three new `--update-secrets` lines).

## 11. Boundaries — What NOT to Do

- DO NOT send transactional email from R1 directly. R1 never opens SMTP, never calls SendGrid/Postmark/SES. Customer.io is the only delivery surface.
- DO NOT capture user data beyond `{email, user_id, tenant_id, plan_tier, signup_date}`. The `traits.Build` function is the allowlist; the test asserts the struct has no other exported fields.
- DO NOT block hot paths on Customer.io calls. The bus subscriber is `ModeObserve`. Every SDK call runs on a worker goroutine off the bus thread, with a 5s context timeout.
- DO NOT email anonymous CLI users. Sessions without a `user_id` from SSO are filtered at the top of the handler (`if userID == "" { return Allow }`).
- DO NOT use `Identify` to send marketing copy as traits. Traits are identity metadata only; campaign content lives in Customer.io.
- DO NOT couple lifecycle to PostHog. They share event names (the SOW's six milestones), but the two subscribers are independent — pulling either spec out leaves the other working.
- DO NOT roll a custom A/B test harness. Use Customer.io's native A/B (documented in `docs/integrations/customerio.md`).
- DO NOT add fields to `r1.policy.yaml` beyond `lifecycle.disabled`. Region, credentials, and feature flags belong in env / Secret Manager, not in a checked-in YAML.

## 12. Testing

### 12.1 Unit (`internal/lifecycle/`)

- [ ] `client_test.go`: `FromEnv` with empty `CUSTOMERIO_SITE_ID` returns a client with `Enabled()==false`; all four methods return nil without making HTTP calls (use `httptest.NewServer` that fails the test if hit).
- [ ] `client_test.go`: with creds set, `Identify`/`Track`/`MergeIdentities`/`Delete` produce the exact REST shapes `PUT /customers/{id}`, `POST /customers/{id}/events`, `POST /merge_customers`, `DELETE /customers/{id}` and Basic-Auth header.
- [ ] `client_test.go`: `CUSTOMERIO_REGION=eu` routes to `track-eu.customer.io`; unknown region falls back to `us` with a warning.
- [ ] `flagstore_test.go`: concurrent `IsFirstTime` calls across 50 goroutines for the same `(tenant, event, user)` produce exactly one true result. Test uses 50 goroutines × 10 iterations.
- [ ] `flagstore_test.go`: `Delete(userID)` removes all 4 milestone rows; a follow-up `IsFirstTime` for `signup` returns true again.
- [ ] `traits_test.go`: reflect over the trait struct and assert exact field set `{email, tenant_id, plan_tier, signup_date}` — fails the build if a future PR adds `phone` or `display_name` etc.

### 12.2 Unit (`internal/hub/builtin/lifecycle_subscriber_test.go`)

- [ ] Bus delivers each of the 6 event types → fake Client records the right `Track`/`Identify` call and the right milestone (where applicable).
- [ ] Idempotent identify: 3 `SSOLoginSucceeded` events for the same user → 3 `Identify` calls, 1 `Track("signup")`.
- [ ] At-capacity queue (fill to 4096) → next event increments `dropped` counter and does not block the publishing goroutine (asserted via a `time.After(50ms)` race).
- [ ] Anonymous session (`user_id==""`) → zero SDK calls, zero counter bumps.
- [ ] Tenant opt-out (`policy.LifecycleDisabled(tenantID)==true`) → zero SDK calls; counter unchanged.

### 12.3 Integration

- [ ] `lifecycle_subscriber_integration_test.go` spins up `httptest.NewServer` with handlers for `/api/v1/customers/{id}` and `/api/v1/customers/{id}/events`, runs 100 simulated SSO signups followed by 100 `session_started` events, asserts every signup produced one Identify + one signup-track + one activation-track, and every PUT body has the exact `traits` allowlist set.
- [ ] DSAR end-to-end: signup → activation → `Delete(user)` → assert `DELETE /customers/{user}` hit + flagstore rows gone + audit-log entry present + a follow-up signup re-fires `signup`.
- [ ] Daemon-restart test: start subscriber, fire signup, kill the test binary, restart, fire another `SSOLoginSucceeded` for the same user → exactly one Customer.io `signup` event in the captured traffic across the whole test (flagstore survives across processes because it's SQLite on disk).

### 12.4 Smoke (manual, post-deploy)

- [ ] Operator runs an SSO login against a staging tenant, verifies `signup` + `activation` appear in Customer.io's Live Activity within 60s.
- [ ] Operator triggers a `MissionConverged` from staging, verifies `mission_completed` + `first_completion` appear.
- [ ] Operator runs `r1 admin lifecycle delete --user <staging-user>`, verifies the profile is suppressed in Customer.io's UI within 24h.

## 13. Acceptance Criteria

- WHEN `CUSTOMERIO_SITE_ID` and `CUSTOMERIO_API_KEY` are set AND `lifecycle.disabled` is false AND a user fires their first `SSOLoginSucceeded`, `SessionInit`, `MissionCreated`, `MissionConverged`, `EventAntiTruncFired`, and any `EventCostBudget*` event, THE SYSTEM SHALL deliver to Customer.io exactly these tracks: `signup`, `session_started` + `activation`, `mission_started` + `first_mission`, `mission_completed` + `first_completion`, `anti_trunc_fired`, `budget_alert`.
- WHEN `lifecycle.disabled=true` for a tenant, THE SYSTEM SHALL suppress 100% of `Identify` and `Track` calls for that tenant's users.
- WHEN `r1 admin lifecycle delete --user <id>` returns 0, THE SYSTEM SHALL have issued one `DELETE /api/v1/customers/{id}` call, removed all `flagstore` rows for that user, and written one `op=lifecycle_delete` audit-log entry — all within the 24h Customer.io soft-delete window.
- WHEN the daemon restarts after firing a first-class milestone, THE SYSTEM SHALL NOT re-fire that milestone for the same `(tenant, user, event)` on the next replay.
- WHEN Customer.io is unreachable (5s timeout), THE SYSTEM SHALL drop the event, increment the `dropped` counter, log one line, and continue serving the bus without back-pressure.
- WHEN the user has no SSO identity (anonymous CLI run), THE SYSTEM SHALL make zero Customer.io API calls.

## 14. Implementation Checklist

1. [ ] T1 — `internal/lifecycle/client.go`: implement `Client` interface with `httpClient` (wraps `github.com/customerio/go-customerio/v3`) and `noopClient`. Add `FromEnv(serviceName string) Client` and `New(siteID, apiKey, region, serviceName string) Client`. Empty creds → `noopClient`. Mirror `internal/coderadar/coderadar.go` line-for-line for the env-reading and no-op patterns. Add `Enabled() bool`. Unit tests per §12.1.
2. [ ] T1.1 — `internal/lifecycle/region.go`: `RegionURL(region string) string` returning the correct base URL for `us`/`eu`/fallback. Warn-and-fallback for unknown values via `log.Printf`.
3. [ ] T1.2 — `internal/lifecycle/traits.go`: `Build(email, tenantID, planTier string, signupDate time.Time) map[string]any` — the only function that may populate identify traits. `traits_test.go` reflects over the output keys and fails on any key outside `{email, tenant_id, plan_tier, signup_date}`.
4. [ ] T1.3 — `internal/lifecycle/flagstore.go`: SQLite-backed first-time guard at `~/.r1/lifecycle.db`. Methods: `Open(path string) (*FlagStore, error)`, `IsFirstTime(ctx, tenantID, event, userID) (bool, error)`, `DeleteUser(ctx, userID) error`, `Close()`. Use `INSERT OR IGNORE ... RETURNING` for atomic check-and-set. Concurrency test in §12.1.
5. [ ] T2 — `internal/hub/builtin/lifecycle_subscriber.go`: `LifecycleSubscriber` struct + `NewLifecycleSubscriber(client lifecycle.Client, flags *lifecycle.FlagStore, policy *config.Policy)`; `Register(bus *hub.Bus)` subscribes to the six event types from §6.3 in `ModeObserve` with `Priority: 9000`; bounded `queue chan queuedEvent` size 4096; 4 worker goroutines draining with 5s context timeout per SDK call; `dropped atomic.Uint64`; `Snapshot() (queued, dropped uint64)`; `Close()` drains and exits workers.
6. [ ] T2.1 — event→track mapping: implement the six mapping cases from §6.3. Reuse `correlation.FromContext(ctx)` to attach `session_id`/`task_id` to track props when present.
7. [ ] T2.2 — first-time milestone logic: for `signup`, `activation`, `first_mission`, `first_completion`, call `flags.IsFirstTime` and only `Track` the milestone when true. The non-milestone underlying event (`session_started`, `mission_started`, `mission_completed`) is always tracked.
8. [ ] T2.3 — anonymous-user filter: at the top of `handle`, extract `user_id` from the event (Custom["user_id"] or session lookup); if empty, return Allow immediately.
9. [ ] T2.4 — tenant opt-out gate: check `policy.LifecycleDisabled(tenantID)` after user-id extraction; if true, return Allow without enqueueing.
10. [ ] T2.5 — anti-trunc emit bridge (conditional): if `internal/antitrunc/` does not yet publish `EventAntiTruncFired` on `hub.Bus`, add the emit at the engine's block exit. Single line of code, plus its event constant in `internal/hub/events.go`. Owner-of-antitrunc-package reviews.
11. [ ] T3 — migration / seed: on first `FlagStore.Open`, run `SELECT COUNT(*) FROM lifecycle_first_flags`; if 0 AND `~/.r1/users.db` (or equivalent existing user store from A4) has existing users, write a `signup` row for each existing user with `first_fired_at = NOW` so legacy users never get a backfilled welcome email. Document this in a migration comment in `flagstore.go`. Test: drop in 10 fake legacy users, open store, assert 10 signup rows seeded, assert next `SSOLoginSucceeded` for any of them produces no `Track("signup")`.
12. [ ] T4 — `docs/lifecycle/campaigns.md`: write the campaign catalog table from §7 plus a short "How to author a new campaign" guide referencing Customer.io's segment/journey UI. Each row carries: trigger event name (literal), conditions, copy outline (1 sentence), suppression rule, owner.
13. [ ] T5 — `r1.policy.yaml` schema: add `lifecycle.disabled: bool` to `internal/config/policy.go`. Default `false`. Hot-reloadable via the same path as the existing `retention` block in `specs/retention-policies.md`. Test: flip the flag mid-run, fire an event, assert zero SDK calls. Surface as `policy.LifecycleDisabled(tenantID string) bool`.
14. [ ] T6 — DSAR CLI: `cmd/r1/admin_lifecycle.go` with subcommand `r1 admin lifecycle delete --user <id> [--tenant <id>]`. Calls `Client.Delete`, `FlagStore.DeleteUser`, writes `internal/audit/` entry `op=lifecycle_delete`. Exit 0 on success, 1 on Customer.io failure (audit row still written with `status=failed`). Print the 24h soft-delete note from §9. Test: end-to-end DSAR test from §12.3.
15. [ ] T7 — Cloud Run wiring: edit `cmd/r1-server/main.go` to construct + register the subscriber after the existing coderadar wiring; edit `cloudbuild.yaml` to mount three new secrets (`CUSTOMERIO_SITE_ID`, `CUSTOMERIO_API_KEY`, `CUSTOMERIO_REGION`). r1 CLI unchanged. Smoke per §12.4.
16. [ ] T8 — `docs/integrations/customerio.md`: operator runbook covering: workspace setup, secret provisioning, region selection, opt-out flag, DSAR command usage, A/B test recommendation (use Customer.io native), troubleshooting (where to find the `dropped` counter, how to grep `lifecycle:` log lines).
17. [ ] T9 — Drop-counter metric exposure: extend `r1 admin metrics` output to include `lifecycle.queued` and `lifecycle.dropped` counters drawn from `LifecycleSubscriber.Snapshot()`. If the admin metrics command does not yet exist in this repo, instead expose the same two counters via a Prometheus-style `/metrics` endpoint on `r1-server` (port already exposed); pick whichever surface already exists at build time. The build MUST land at least one operator-visible surface for these counters.
18. [ ] T10 — Audit-log integration: extend `internal/audit/` with op kinds `lifecycle_delete` (above) and `lifecycle_dropped` (logged at INFO once per 1000 drops to detect outages without spamming).
19. [ ] T11 — Integration test fixtures: `internal/hub/builtin/lifecycle_subscriber_integration_test.go` per §12.3 — `httptest.Server` with `/api/v1/customers/{id}` and `/api/v1/customers/{id}/events` handlers, 100-signup harness, daemon-restart harness (use a tempdir for SQLite, spawn subprocess with `os/exec`, kill it, restart).
20. [ ] T12 — Build/CI: confirm `go test ./internal/lifecycle/... ./internal/hub/builtin/...` passes locally; add `customerio` to the smoke-test allowlist in `cloudbuild.yaml` if such a list exists; otherwise no CI changes.
21. [ ] T13 — Self-review pass: re-read §11 boundaries; grep the new code for `email`, `password`, `prompt`, `mission_title`, `ledger`, `file_path` — any match outside `client.go`'s `Identify` call is a bug (PII leak).
22. [ ] T14 — Mark spec done: flip frontmatter `STATUS: ready` → `STATUS: done`, add `BUILD_COMPLETED: <date>`, update `PORTFOLIO-EXECUTION-INDEX.md` row 39.
