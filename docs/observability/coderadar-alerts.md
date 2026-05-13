# CodeRadar Alerts — R1 Dogfood (B3)

Alert rules are JSON-defined in `coderadar-admin`. Each block below is a
ready-to-import rule. Severity levels: `page` (PagerDuty oncall) and
`info` (Slack #r1-ops).

The 18 canonical events these rules query are defined in
`internal/coderadar/events.go`.

---

## 1. provider_p99_slow

Pages oncall when the 99th-percentile provider latency stays above 30s
for a 5-minute window. Used to catch wedged-upstream regressions in
Anthropic / OpenAI / OpenRouter.

```json
{
  "name": "provider_p99_slow",
  "version": "1.0.0",
  "severity": "page",
  "destination": "pagerduty:oncall",
  "filters": {"env": "prod"},
  "window": "5m",
  "debounce": "10m",
  "expression": "quantile(0.99, provider.call_completed.duration_ms) > 30000",
  "title": "R1 provider p99 > 30s",
  "runbook": "docs/runbooks/provider_p99_slow.md"
}
```

## 2. antitrunc_burst

Informational ping when the gate fires more than 5 times per minute
in prod — usually correlates with a regression in a system prompt or a
new model variant misbehaving.

```json
{
  "name": "antitrunc_burst",
  "version": "1.0.0",
  "severity": "info",
  "destination": "slack:#r1-ops",
  "filters": {"env": "prod"},
  "window": "1m",
  "debounce": "5m",
  "expression": "rate(antitrunc.fired) > 5",
  "title": "Anti-truncation burst in prod",
  "runbook": "docs/runbooks/antitrunc_burst.md"
}
```

## 3. mission_abort_rate

Pages oncall when 10% or more of mission outcomes are aborts over a
1-hour window. Captures cascading-failure regressions early.

```json
{
  "name": "mission_abort_rate",
  "version": "1.0.0",
  "severity": "page",
  "destination": "pagerduty:oncall",
  "filters": {"env": "prod"},
  "window": "1h",
  "debounce": "1h",
  "expression": "rate(mission.aborted) / (rate(mission.completed) + rate(mission.aborted)) > 0.10",
  "title": "R1 mission abort rate > 10%",
  "runbook": "docs/runbooks/mission_abort_rate.md"
}
```

## 4. service_health_stale

Pages oncall when ANY of the 9 services stops emitting
`service_health_check` for 5 minutes — the 60s heartbeat means a 5m gap
is unambiguous "the binary is wedged or down".

```json
{
  "name": "service_health_stale",
  "version": "1.0.0",
  "severity": "page",
  "destination": "pagerduty:oncall",
  "filters": {"env": "prod"},
  "group_by": ["service_name"],
  "window": "5m",
  "debounce": "10m",
  "expression": "absent(service_health_check, 5m)",
  "title": "R1 service health-check stale",
  "runbook": "docs/runbooks/service_health_stale.md"
}
```

---

## Runbook stubs

Each `runbook:` link above points at a markdown file under `docs/runbooks/`.
The four runbook files referenced here are tracked in [coderadar-runbook.md](./coderadar-runbook.md)
as part of the operator playbook for B3.

## Import procedure

1. Open `coderadar-admin` for the target env's project.
2. Settings → Alerts → Import JSON.
3. Paste each block, confirm the destination, save.
4. Verify firing by manually publishing a test event:
   ```bash
   make smoke-coderadar ENV=dev
   ```
   Confirm the dashboard shows the smoke event without firing an alert
   (the test payload has `service_name=coderadar-smoke-dev` and is
   filtered out of the prod rules by the `env` filter).
