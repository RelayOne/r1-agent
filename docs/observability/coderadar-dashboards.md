# CodeRadar Dashboards — R1 Dogfood (B3)

This document is the hand-off artifact for `coderadar-admin`. Each
section below is a hero dashboard with its JSON export. The
`coderadar-admin/src/app/(dashboard)/` UI imports these JSONs directly.

The 18 canonical events these dashboards consume are defined in
`internal/coderadar/events.go` and listed in
[coderadar-events.md](./coderadar-events.md).

---

## 1. Mission Funnel

Sankey diagram from `mission.plan` → `mission.execute` → `mission.verify`
→ `mission.review` → `mission.completed` / `mission.aborted`, plus a
per-env time-series of throughput.

```json
{
  "name": "Mission Funnel",
  "version": "1.0.0",
  "filters": {"env": "${ENV}"},
  "widgets": [
    {
      "id": "mission-sankey",
      "type": "sankey",
      "source_events": [
        "mission.plan",
        "mission.execute",
        "mission.verify",
        "mission.review",
        "mission.completed",
        "mission.aborted"
      ],
      "join_key": "props.mission_id",
      "stages": [
        {"from": "mission.plan",    "to": "mission.execute"},
        {"from": "mission.execute", "to": "mission.verify"},
        {"from": "mission.verify",  "to": "mission.review"},
        {"from": "mission.review",  "to": "mission.completed"},
        {"from": "mission.review",  "to": "mission.aborted"}
      ]
    },
    {
      "id": "mission-throughput",
      "type": "time_series",
      "metric": "count(mission.completed)",
      "group_by": ["env"],
      "interval": "5m"
    }
  ]
}
```

## 2. Anti-Trunc Health

Time-series of `antitrunc.fired` grouped by `pattern_matched`, plus the
`antitrunc.overridden` overlay and the ratio `antitrunc.fired /
mission.completed`.

```json
{
  "name": "Anti-Trunc Health",
  "version": "1.0.0",
  "filters": {"env": "${ENV}"},
  "widgets": [
    {
      "id": "fired-by-pattern",
      "type": "time_series",
      "metric": "count(antitrunc.fired)",
      "group_by": ["props.pattern_matched"],
      "interval": "1m"
    },
    {
      "id": "override-overlay",
      "type": "time_series",
      "metric": "count(antitrunc.overridden)",
      "overlay_on": "fired-by-pattern",
      "interval": "1m"
    },
    {
      "id": "fired-per-mission",
      "type": "ratio",
      "numerator": "count(antitrunc.fired)",
      "denominator": "count(mission.completed)"
    }
  ]
}
```

## 3. Provider SLO

p50/p95/p99 latency for `provider.call_completed`, error rate from
`provider.call_errored`, and the `provider.fallback` Sankey
(from_model → to_model).

```json
{
  "name": "Provider SLO",
  "version": "1.0.0",
  "filters": {"env": "${ENV}"},
  "widgets": [
    {
      "id": "p99-latency",
      "type": "time_series",
      "metric": "quantile(0.99, provider.call_completed.duration_ms)",
      "group_by": ["props.model"],
      "interval": "1m"
    },
    {
      "id": "p95-latency",
      "type": "time_series",
      "metric": "quantile(0.95, provider.call_completed.duration_ms)",
      "group_by": ["props.model"],
      "interval": "1m"
    },
    {
      "id": "error-rate",
      "type": "ratio",
      "numerator": "count(provider.call_errored)",
      "denominator": "count(provider.call_completed) + count(provider.call_errored)",
      "group_by": ["props.model"]
    },
    {
      "id": "fallback-sankey",
      "type": "sankey",
      "source_events": ["provider.fallback"],
      "stages": [
        {"from": "props.from_model", "to": "props.to_model"}
      ]
    }
  ]
}
```

## 4. Cost vs Mission Completion

Daily $ from `sum(provider.call_completed.cost_usd)` overlaid on count
of `mission.completed`, with a per-model breakdown panel.

```json
{
  "name": "Cost vs Mission Completion",
  "version": "1.0.0",
  "filters": {"env": "${ENV}"},
  "widgets": [
    {
      "id": "daily-cost",
      "type": "time_series",
      "metric": "sum(provider.call_completed.cost_usd)",
      "interval": "1d"
    },
    {
      "id": "daily-missions",
      "type": "time_series",
      "metric": "count(mission.completed)",
      "interval": "1d",
      "overlay_on": "daily-cost"
    },
    {
      "id": "cost-by-model",
      "type": "stacked_bar",
      "metric": "sum(provider.call_completed.cost_usd)",
      "group_by": ["props.model"],
      "interval": "1d"
    }
  ]
}
```

---

## Import procedure

1. Open `coderadar-admin` for the target env's project.
2. Settings → Dashboards → Import JSON.
3. Paste each block above into a new dashboard.
4. The `${ENV}` filter is materialized at import time; pick the
   appropriate env per dashboard or leave un-set for cross-env views.

Schema versions for every consumed event are pinned in
`internal/coderadar/events.go`'s `SchemaVersions` map. Bump there +
re-import on any field rename.
