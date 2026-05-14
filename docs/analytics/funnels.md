# R1 Product Funnels

These three funnels are the activation, honesty, and cost-engagement
measurements `specs/posthog-analytics.md` §9 commits to shipping. They are
checked in here so the funnel definitions are reviewable in PRs; PostHog's
UI imports each via copy-paste of the fenced JSON block.

All event names below correspond exactly to entries in
`internal/analytics/taxonomy.go` — when the taxonomy moves, this file must
move with it. The `TestEventNameHygiene` unit test asserts the regex shape
but does not (and cannot) cross-check funnel-step names — that is the
operator's responsibility on import.

## 1. Activation funnel

The headline B1 measurement. Target: at least 40% conversion from
`sso_login_succeeded` to `mission_completed` within a 7-day window.

```
sso_login_succeeded
  -> session_started
  -> mission_started
  -> mission_completed
```

Conversion window: 7 days. Suggested breakdowns: by `mode`
(interactive/oneshot/server), by tenant group, by `R1_ENV`.

```json
{
  "name": "R1 — Activation",
  "filters": {
    "insight": "FUNNELS",
    "events": [
      {"id": "sso_login_succeeded", "order": 0, "type": "events"},
      {"id": "session_started",     "order": 1, "type": "events"},
      {"id": "mission_started",     "order": 2, "type": "events"},
      {"id": "mission_completed",   "order": 3, "type": "events"}
    ],
    "funnel_window_interval": 7,
    "funnel_window_interval_unit": "day",
    "breakdown_type": "event",
    "breakdown": "mode"
  }
}
```

Read: drop-offs between steps 2 and 3 mean operators land but never start
a mission — usually a CLI ergonomics issue. Drop-offs between 3 and 4 mean
missions are starting but verification gates are failing.

## 2. Honesty value funnel

Proves that anti-truncation actually fires inside successful missions, not
only inside failed ones. If most completions had NO anti-trunc fire, the
gate is dormant; if step 3 collapses far below step 1, the gate is too
aggressive.

```
mission_started
  -> anti_trunc_fired (any severity)
  -> mission_completed
```

```json
{
  "name": "R1 — Honesty value",
  "filters": {
    "insight": "FUNNELS",
    "events": [
      {"id": "mission_started",   "order": 0, "type": "events"},
      {"id": "anti_trunc_fired",  "order": 1, "type": "events"},
      {"id": "mission_completed", "order": 2, "type": "events"}
    ],
    "funnel_window_interval": 24,
    "funnel_window_interval_unit": "hour",
    "breakdown_type": "event",
    "breakdown": "severity"
  }
}
```

Read: a steady "warn" severity at step 1->2 with a healthy step 2->3
conversion is the desired profile. A spike in "block" severity at 2 with
a drop at 3 means the gate is killing missions instead of redirecting them.

## 3. Cost engagement funnel

Measures whether operators are pushing missions close enough to budget that
alerting is meaningful. A funnel that collapses at step 3 means operators
never approach the budget — alerts are unused; one that completes 100% means
the budget is set too low.

```
session_started
  -> cost_event_recorded (at least one)
  -> budget_alert_fired
```

```json
{
  "name": "R1 — Cost engagement",
  "filters": {
    "insight": "FUNNELS",
    "events": [
      {"id": "session_started",      "order": 0, "type": "events"},
      {"id": "cost_event_recorded",  "order": 1, "type": "events"},
      {"id": "budget_alert_fired",   "order": 2, "type": "events"}
    ],
    "funnel_window_interval": 7,
    "funnel_window_interval_unit": "day"
  }
}
```

Read: the conversion at 2->3 is the budget visibility uptake metric.
Steady-state target: 5%-15%. Below 5% means operators ignore budgets;
above 15% means default budgets are too tight.

## Importing into PostHog

1. PostHog UI → Insights → New insight → Funnel.
2. Click the kebab menu → "Edit insight JSON" (advanced view).
3. Paste the JSON block above.
4. Save with the suggested name.

Re-importing after a taxonomy bump: each event name is the literal string
the SDK writes; renames in `internal/analytics/taxonomy.go` MUST be
reflected here before the next release.
