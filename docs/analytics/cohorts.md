# R1 Product Cohorts

Four cohorts back the per-tenant dashboarding promise in
`specs/posthog-analytics.md` §10. Each cohort is defined twice — first as
prose so a reviewer can sanity-check the intent, then as a fenced JSON
body suitable for `POST /api/projects/{id}/cohorts/`.

Cohorts feed funnels, dashboards, and retention queries, so renaming an
event in `internal/analytics/taxonomy.go` MUST be paired with an edit
here.

## 1. Power users

Distinct IDs that complete at least 10 missions in the last 7 days. The
target audience for advanced-features beta invitations.

HogQL query (paste into PostHog's cohort builder when "advanced" is on):

```hogql
SELECT distinct_id
FROM events
WHERE event = 'mission_completed'
  AND timestamp >= now() - INTERVAL 7 DAY
GROUP BY distinct_id
HAVING count() >= 10
```

```json
{
  "name": "Power users (>=10 missions / 7d)",
  "is_static": false,
  "filters": {
    "properties": {
      "type": "AND",
      "values": [{
        "type": "behavioral",
        "value": "performed_event_multiple_times",
        "event_type": "events",
        "key": "mission_completed",
        "operator_value": 10,
        "time_value": 7,
        "time_interval": "day"
      }]
    }
  }
}
```

## 2. Anti-trunc beneficiaries

Distinct IDs whose anti-truncation gate fired at least 3 times AND still
completed at least one mission in the last 14 days. The cohort answers
the question: "did the gate help, or did it kill the mission?"

```hogql
WITH fired AS (
  SELECT distinct_id, count() AS fires
  FROM events
  WHERE event = 'anti_trunc_fired'
    AND timestamp >= now() - INTERVAL 14 DAY
  GROUP BY distinct_id
),
completed AS (
  SELECT distinct_id, count() AS done
  FROM events
  WHERE event = 'mission_completed'
    AND timestamp >= now() - INTERVAL 14 DAY
  GROUP BY distinct_id
)
SELECT f.distinct_id
FROM fired f
JOIN completed c ON c.distinct_id = f.distinct_id
WHERE f.fires >= 3 AND c.done >= 1
```

```json
{
  "name": "Anti-trunc beneficiaries (>=3 fires & >=1 completion / 14d)",
  "is_static": false,
  "filters": {
    "properties": {
      "type": "AND",
      "values": [
        {
          "type": "behavioral",
          "value": "performed_event_multiple_times",
          "event_type": "events",
          "key": "anti_trunc_fired",
          "operator_value": 3,
          "time_value": 14,
          "time_interval": "day"
        },
        {
          "type": "behavioral",
          "value": "performed_event_multiple_times",
          "event_type": "events",
          "key": "mission_completed",
          "operator_value": 1,
          "time_value": 14,
          "time_interval": "day"
        }
      ]
    }
  }
}
```

## 3. At-risk

Distinct IDs whose verification failure rate exceeds 30% over the last
14 days — i.e. `failed / (failed + passed) > 0.30`. These accounts are
the highest churn risk; the customer success rota sweeps them weekly.

```hogql
WITH passed AS (
  SELECT distinct_id, count() AS p
  FROM events
  WHERE event = 'mission_verify_passed'
    AND timestamp >= now() - INTERVAL 14 DAY
  GROUP BY distinct_id
),
failed AS (
  SELECT distinct_id, count() AS f
  FROM events
  WHERE event = 'mission_verify_failed'
    AND timestamp >= now() - INTERVAL 14 DAY
  GROUP BY distinct_id
)
SELECT failed.distinct_id
FROM failed
LEFT JOIN passed ON passed.distinct_id = failed.distinct_id
WHERE failed.f / (failed.f + coalesce(passed.p, 0)) > 0.30
```

```json
{
  "name": "At-risk (verify failure rate >30% / 14d)",
  "is_static": false,
  "filters": {
    "properties": {
      "type": "AND",
      "values": [{
        "type": "behavioral",
        "value": "performed_event_multiple_times",
        "event_type": "events",
        "key": "mission_verify_failed",
        "operator_value": 3,
        "time_value": 14,
        "time_interval": "day"
      }]
    }
  }
}
```

Note: the HogQL form is the authoritative definition; the JSON cohort
filter is a simpler approximation that PostHog's cohort builder accepts —
a true rate cohort requires the HogQL path.

## 4. Cost-sensitive tenants (group cohort)

A `tenant` group cohort: every tenant whose summed `cost_event_recorded.cost_usd`
exceeds $50 in the last 7 days. Drives the cost-engagement runbook
described in `docs/integrations/posthog.md`.

```hogql
SELECT $group_0 AS tenant_id, sum(properties.cost_usd) AS total
FROM events
WHERE event = 'cost_event_recorded'
  AND timestamp >= now() - INTERVAL 7 DAY
  AND $group_0 IS NOT NULL
GROUP BY tenant_id
HAVING total > 50
```

```json
{
  "name": "Cost-sensitive tenants (>$50 / 7d)",
  "is_static": false,
  "groups": [{"group_type_index": 0}],
  "filters": {
    "properties": {
      "type": "AND",
      "values": [{
        "type": "behavioral",
        "value": "performed_event_multiple_times",
        "event_type": "events",
        "key": "cost_event_recorded",
        "operator_value": 1,
        "time_value": 7,
        "time_interval": "day"
      }]
    }
  }
}
```

## Importing into PostHog

`POST /api/projects/{id}/cohorts/` with `Content-Type: application/json`
and the body above. The HogQL path is the cohort-builder advanced view —
paste into Cohorts → New → Edit query.

## Operational notes

- Cohorts refresh on PostHog's standard schedule (every 24 hours for
  static cohorts, dynamic on read for behavioral). Force-refresh from
  the cohort detail page if you need fresh data immediately.
- Group cohorts (cohort 4) require the `tenant` group type to be
  registered in Project Settings before import. See
  `docs/integrations/posthog.md` §1 step 4.
- Cohort exports power retention-loop emails; CI deploys to PostHog land
  via a separate Terraform module (out of scope for B1).
