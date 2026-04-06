# monitoring-observability

> Production monitoring, structured logging, metrics, distributed tracing, and alert design

<!-- keywords: monitoring, observability, logging, alerting, metrics, tracing, prometheus, grafana -->

## Three Pillars

1. **Logs:** Discrete events. What happened and when. Best for debugging specific requests.
2. **Metrics:** Aggregated numbers over time. Request rate, error rate, latency percentiles. Best for dashboards and alerts.
3. **Traces:** Request flow across services. Best for understanding latency and dependencies in distributed systems.

Use all three together. Correlate them with a shared `trace_id` on every log line and metric label.

## Structured Logging

1. **Always use structured format.** JSON logs, not free-text. Every log must be machine-parseable.
2. **Standard fields on every log line:** `timestamp`, `level`, `service`, `trace_id`, `message`.
3. **Use log levels correctly.** ERROR = requires human attention. WARN = degraded but functioning. INFO = business events. DEBUG = off in production.
4. **Never log sensitive data.** PII, tokens, passwords, credit card numbers. Redact or mask.
```go
log.Info("order processed",
    "order_id", order.ID,
    "user_id", order.UserID,
    "amount_cents", order.TotalCents,
    "duration_ms", elapsed.Milliseconds(),
)
```
5. **Include request context.** HTTP method, path, status code, duration, and request ID on every request log.
6. **Rate-limit repetitive logs.** A tight loop logging the same error 10,000 times per second kills your log pipeline and budget.

## Prometheus Metrics Types

| Type | Use Case | Example |
|------|----------|---------|
| Counter | Monotonically increasing totals | `http_requests_total{method="GET", status="200"}` |
| Gauge | Values that go up and down | `active_connections`, `queue_depth` |
| Histogram | Distribution of values in buckets | `http_request_duration_seconds` |
| Summary | Pre-computed quantiles (client-side) | Avoid -- prefer histograms for aggregation |

**Naming conventions:** `<namespace>_<name>_<unit>`. Use `_total` suffix for counters, `_seconds` or `_bytes` for units. Never use camelCase.

**Cardinality warning:** Every unique label combination creates a time series. `user_id` as a label will destroy your Prometheus instance. Use high-cardinality values in logs, not metrics.

## Distributed Tracing with OpenTelemetry

1. **Instrument at service boundaries.** HTTP handlers, gRPC interceptors, database calls, external API calls.
2. **Propagate context.** Use W3C `traceparent` header. Every outgoing HTTP request must forward the trace context.
3. **Add span attributes.** `db.statement`, `http.status_code`, `user.id`. These make traces searchable.
4. **Set span status on errors.** `span.SetStatus(codes.Error, err.Error())` so traces show failures visually.
```go
ctx, span := tracer.Start(ctx, "ProcessOrder",
    trace.WithAttributes(attribute.String("order.id", orderID)))
defer span.End()
```
5. **Sample in production.** 100% tracing is expensive. Use head-based sampling at 1-10% for normal traffic, tail-based sampling to keep all error traces.

## Alert Design

**Every alert must be actionable.** If the on-call person cannot do anything about it, delete the alert.

### SLO-Based Alerting (Recommended)
1. Define SLI: `successful_requests / total_requests` (availability) or `requests_below_300ms / total_requests` (latency).
2. Set SLO target: 99.9% availability over a 30-day window.
3. Calculate error budget: 0.1% of requests can fail = ~43 minutes of downtime/month.
4. Alert when burn rate exceeds threshold: consuming 14.4x budget in 1 hour (fast burn) or 3x budget in 6 hours (slow burn).

### Alert Anti-Patterns
- **Alerting on causes, not symptoms.** Alert on "error rate > 1%" not "CPU > 80%". High CPU with happy users is fine.
- **Too many alerts.** Alert fatigue kills response quality. Fewer, higher-signal alerts win.
- **Missing runbook link.** Every alert must link to a runbook with diagnosis steps and remediation actions.

## Dashboard Design

1. **USE method for infrastructure:** Utilization, Saturation, Errors for each resource (CPU, memory, disk, network).
2. **RED method for services:** Rate, Errors, Duration for each service endpoint.
3. **Top row = business KPIs.** Requests/sec, error rate, p99 latency. These tell you if users are happy.
4. **Drill-down layout.** Overview dashboard links to per-service dashboards, which link to individual instance views.
5. **Time range matters.** Default to last 1 hour. Provide easy toggles for 6h, 24h, 7d, 30d.

## Error Budgets and SLI/SLO/SLA

- **SLI** (Service Level Indicator): The measurement. `request_latency_p99 < 300ms`.
- **SLO** (Service Level Objective): Internal target. "99.9% of requests under 300ms over 30 days."
- **SLA** (Service Level Agreement): External contract with penalties. Always set SLA lower than SLO to give yourself margin.
- **Error budget** = `1 - SLO`. At 99.9% SLO, you have 0.1% budget. Track consumption weekly. When budget is exhausted, freeze feature releases and focus on reliability.

## Incident Response Runbooks

Every runbook follows this template:
1. **Detection:** What alert fired? What does it mean?
2. **Impact assessment:** Which users/features are affected? Is it partial or total?
3. **Diagnosis steps:** Specific queries to run, dashboards to check, logs to search.
4. **Remediation:** Step-by-step fix. Include rollback commands, scaling commands, or failover procedures.
5. **Escalation:** When to page additional people. Include contact info and escalation criteria.
6. **Post-incident:** Link to post-mortem template. Every SEV1/SEV2 gets a post-mortem within 48 hours.
