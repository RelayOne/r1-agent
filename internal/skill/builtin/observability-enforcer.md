# observability-enforcer

> Ensures all services ship with OpenTelemetry instrumentation, SLO-based alerting, structured logging, and health checks following Google SRE best practices

<!-- keywords: observability, monitoring, alerting, sre, opentelemetry, prometheus, grafana, tracing, metrics, logging, healthcheck, slo -->

## When to Use
- Creating a new service, HTTP handler, gRPC server, or middleware
- Adding monitoring, alerting, dashboards, or health checks
- Writing Kubernetes manifests for a service deployment
- Reviewing code that handles request processing or error reporting
- Setting up CI/CD pipelines that need observability gates

## When NOT to Use
- Pure CLI tools or one-off scripts with no production runtime
- Library packages that don't run as standalone services
- Static site generators or build tooling

## Behavioral Guidance

### Every New Service Must Include

**Health endpoints:**
- `GET /livez` -- returns 200 if process is alive (NO dependency checks)
- `GET /readyz` -- returns 200 when ready to serve (checks owned DB/cache only)
- `GET /metrics` -- Prometheus-format metrics endpoint

**OpenTelemetry initialization:**
- Tracer provider in `main()` with `service.name`, `service.version`, `deployment.environment`
- W3C TraceContext propagation (`OTEL_PROPAGATORS="tracecontext,baggage"`)
- Auto-instrumentation for HTTP/gRPC/DB clients
- Shutdown hook registered for telemetry flushing
- `OTEL_SDK_DISABLED=true` support as emergency kill switch

**RED metrics (minimum):**
- `http_request_duration_seconds` histogram with labels: method, path, status_code
- `http_requests_total` counter with labels: method, path, status_code

**Structured JSON logging:**
- All logs as JSON with: `timestamp`, `level`, `message`, `service`, `trace_id`, `span_id`
- PII masking at the source for sensitive fields
- Production log level set to INFO

**Kubernetes probes (when deploying to K8s):**
- Startup, liveness, and readiness probes configured
- Resource requests and limits set
- PodMonitor or ServiceMonitor for Prometheus scraping

### Probe Configuration

```yaml
startupProbe:
  httpGet: { path: /readyz, port: 8080 }
  periodSeconds: 5
  failureThreshold: 12        # Up to 60s to start

livenessProbe:
  httpGet: { path: /livez, port: 8080 }
  periodSeconds: 10
  timeoutSeconds: 2
  failureThreshold: 3         # 30s before restart

readinessProbe:
  httpGet: { path: /readyz, port: 8080 }
  periodSeconds: 5
  timeoutSeconds: 1
  failureThreshold: 2
  successThreshold: 2
```

### OTel Collector Pipeline Ordering

`memory_limiter` first, then `resourcedetection`, then `transform/filter`, then `batch` last. Always deploy an OTel Collector between applications and backends. Use agent+gateway pattern: DaemonSet agents per node, gateway Deployments for centralized processing.

### SLO-Based Alerting (Multi-Window Multi-Burn-Rate)

| Severity | Long Window | Short Window | Burn Rate | Budget Consumed |
|----------|-------------|--------------|-----------|-----------------|
| Page     | 1 hour      | 5 minutes    | 14.4      | 2%              |
| Page     | 6 hours     | 30 minutes   | 6         | 5%              |
| Ticket   | 24 hours    | 2 hours      | 3         | 10%             |
| Ticket   | 3 days      | 6 hours      | 1         | 10%             |

Use AND logic: long window AND short window must both exceed threshold. Pre-compute SLIs as recording rules. Use Sloth to auto-generate burn-rate rules.

### Metrics Naming and Cardinality

- Snake_case with namespace prefix and unit suffix: `_seconds`, `_bytes`, `_total`
- Never use user IDs, request IDs, trace IDs, or dynamic URL segments as labels
- Use exemplars for trace ID correlation without cardinality cost
- Set `sample_limit: 5000` per scrape target
- 10-15 exponentially-spaced histogram buckets; prefer Native Histograms when available

### Log Level Discipline

| Level | Use | Alert? |
|-------|-----|--------|
| FATAL | Unrecoverable, process must exit | Immediate page |
| ERROR | Operation failed, user request cannot complete | High-priority |
| WARN  | Recoverable issue, retry succeeded, approaching threshold | Slack/ticket |
| INFO  | Significant business events | No |
| DEBUG | Implementation details -- OFF in production | No |

### Cost Control

- Filter health check logs at Collector level (eliminates 20-40% volume)
- Tail-based sampling: 100% errors, 100% slow >2s, 5-10% everything else
- Tier storage: Hot 7d, Warm 30d, Cold 365d on object storage
- Target 1-3 log lines per normal request in production

## Gotchas
- **Never check downstream dependencies in liveness probes.** When a downstream fails, liveness checks trigger mass restarts, turning partial outage into cascading failure.
- **Always register OTel shutdown hooks.** Without them, the final batch of spans is lost on process exit.
- **`OTEL_SERVICE_NAME` must be set explicitly.** Deploying with `unknown_service` makes traces useless.
- **HTTP semantic conventions renamed in OTel 1.40+.** `http.method` is now `http.request.method`; `http.status_code` is now `http.response.status_code`. Opt in via `OTEL_SEMCONV_STABILITY_OPT_IN=http`.
- **Invalid user input logged as ERROR is wrong.** Validation failures are DEBUG/INFO. Successful retries are WARN, not ERROR.
- **Clear MDC/context in finally blocks.** Failing to do so leaks correlation IDs across requests in thread pools.
- **Cardinality explosion from one bad metric.** A metric with `method` x `handler` x `status_code` x `customer_id` can produce millions of series.
- **Alerts must be urgent, important, actionable, and real.** Alert on symptoms from the user's perspective, not infrastructure causes. If you can ignore an alert knowing it is benign, remove it.
- **Every alert must link to a runbook** via the `runbook_url` annotation.
- **Background health check pattern:** Run dependency checks in a goroutine every 10s, cache the result, return cached status from probe endpoint. Probes respond in under 1ms with predictable dependency load.
