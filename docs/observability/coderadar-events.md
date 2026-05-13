# CodeRadar Canonical Events — R1 Dogfood (B3)

The 18 canonical events the R1 → CodeRadar subscriber emits. The
source of truth is `internal/coderadar/events.go`: the
`CanonicalEvents` slice pins inventory, `SchemaVersions` pins per-event
wire version, and `AllowedProps` enforces the redaction allowlist.

| # | Event | Schema | Hub source | Sample (prod) | Notes |
|---|-------|--------|------------|---------------|-------|
| 1 | `service_started`        | 1.0.0 | `cmd/*/main.go` boot via `EmitLifecycle` | 100% | `version`, `pid`, `commit_sha` |
| 2 | `service_stopped`        | 1.0.0 | shutdown handler via `EmitLifecycle`     | 100% | `reason`, `uptime_ms` |
| 3 | `service_health_check`   | 1.0.0 | 60s heartbeat goroutine                  | 100% | `status`, `subsystems`, `coderadar_circuit_open` |
| 4 | `mission.plan`           | 1.0.0 | `hub.EventMissionPlanDone`               | 100% | `mission_id`, `plan_steps`, `model` |
| 5 | `mission.execute`        | 1.0.0 | `hub.EventMissionExecuteStart`           | 100% | `mission_id`, `tasks_dispatched` |
| 6 | `mission.verify`         | 1.0.0 | `hub.EventVerifyConvergenceStart`        | 100% | `mission_id`, `verify_kind` |
| 7 | `mission.review`         | 1.0.0 | `hub.EventVerifyCriticReview`            | 100% | `mission_id`, `reviewer` |
| 8 | `mission.completed`      | 1.0.0 | `hub.EventMissionConverged`              | 100% | `mission_id`, `duration_ms`, `total_cost_usd` |
| 9 | `mission.aborted`        | 1.0.0 | `hub.EventMissionFailed` + `EventMissionCancelled` | 100% | `mission_id`, `abort_reason` (failed/cancelled) |
| 10 | `cortex.lobe_invoked`   | 1.0.0 | `hub.EventCortexLobeStarted`             | 100% | `lobe_name`, `mission_id` |
| 11 | `cortex.round_completed`| 1.0.0 | `hub.EventCortexRoundCompleted`          | 100% | `round`, `notes_published` |
| 12 | `antitrunc.fired`       | 1.0.0 | `hub.EventAntiTruncFired`                | 100% | `pattern_matched`, `phase`, `evt_id` |
| 13 | `antitrunc.overridden`  | 1.0.0 | `hub.EventAntiTruncOverridden`           | 100% | `override_actor`, `justification_hash` |
| 14 | `provider.call_started` | 1.0.0 | `hub.EventModelPreCall`                  | 100% | `provider`, `model`, `gen_ai.*` |
| 15 | `provider.call_completed`| 1.0.0 | `hub.EventModelPostCall`                | **10%** | input/output_tokens, cost_usd, gen_ai.usage.* |
| 16 | `provider.call_errored` | 1.0.0 | `hub.EventModelError`                    | 100% | `error_class`, `http_status`, `error_message{,_sha256}` |
| 17 | `provider.fallback`     | 1.0.0 | `hub.EventModelFallback`                 | 100% | `from_model`, `to_model`, `reason` |
| 18 | `tool.call_completed`   | 1.0.0 | `hub.EventToolPostUse`                   | **10%** | `tool_name`, `exit_code`, `duration_ms`, `file_path_sha256` |

## Correlation IDs

Every event carries a `correlation_id` envelope field plus the three
prop-form IDs:

- `r1.session_id` — session-scoped ULID (or `mission_id` fallback)
- `r1.agent_id`   — per-agent ULID
- `r1.task_id`    — per-task ULID

The envelope `correlation_id` composes them as `s:<sess>/a:<agent>/t:<task>`
so a single CodeRadar query for a session uses a prefix match.

## Redaction layers

See `internal/coderadar/redactor.go`:

1. **Allowlist (hard).** Any prop key not in `AllowedProps[event_name]`
   is dropped silently. Never logged with content.
2. **Promptguard scrub.** Free-form strings (`error_message`,
   `abort_reason`, `reason`) are run through `internal/promptguard.Sanitize`
   with `ActionStrip` — detected injection / leaked-secret patterns are
   replaced with `[REDACTED-PROMPT-INJECTION]` markers.

Hard prohibitions (asserted in `redactor_test.go`):

- Raw prompts → never emitted (not on any allowlist).
- Raw tool inputs → never emitted (not on any allowlist).
- Plain `file_path` → automatically rewritten to `file_path_sha256`.
- `error_message` > 200 chars → truncated; SHA256 of original preserved.

## Sample rate

`CODERADAR_SAMPLE_RATE` env var sets the default rate; `SamplerForEnv`
overlays prod-only overrides:

| Env       | Default | provider.call_completed | tool.call_completed |
|-----------|---------|-------------------------|---------------------|
| local     | 0.0     | 0.0                     | 0.0                 |
| dev       | 1.0     | 1.0                     | 1.0                 |
| staging   | 1.0     | 1.0                     | 1.0                 |
| prod      | 1.0     | 0.1                     | 0.1                 |

Mission lifecycle, anti-trunc, service lifecycle, cortex, and provider
error/fallback events stay at 100% regardless of env: low-volume,
high-value.

## Adding a new event

Adding a 19th event is a coordinated change. The procedure:

1. Amend `specs/coderadar-dogfood.md` T1.
2. Add the name + schema version + props to
   `internal/coderadar/events.go` (`CanonicalEvents`, `SchemaVersions`,
   `AllowedProps`).
3. Add a hub event type if needed (or wire to an existing one).
4. Extend `mapHubToCanonical` in
   `internal/hub/builtin/coderadar_subscriber.go`.
5. Update the dashboards / alerts JSON in this directory.
6. Bump schema in `coderadar-admin` sibling repo.
