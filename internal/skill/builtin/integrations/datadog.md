# datadog

> Datadog: metrics, events, logs, traces. `DD-API-KEY` header for most ingestion; some endpoints also need `DD-APPLICATION-KEY` for read/admin. Site-specific subdomains (us1, us3, us5, eu1, ap1).

<!-- keywords: datadog, metrics, apm, logs, observability, dd-api-key, ddtrace, dd-agent -->

**Official docs:** https://docs.datadoghq.com/api/latest/  |  **Verified:** 2026-04-14 via web search.

## Base URL (pick your site)

- US1 (default): `https://api.datadoghq.com/api/v2/`
- US3: `https://api.us3.datadoghq.com/api/v2/`
- US5: `https://api.us5.datadoghq.com/api/v2/`
- EU1: `https://api.datadoghq.eu/api/v2/`
- AP1: `https://api.ap1.datadoghq.com/api/v2/`
- Gov: `https://api.ddog-gov.com/api/v2/`

Wrong site = 403 Forbidden with misleading message. Match the site from your Datadog UI URL.

## Auth headers

```
DD-API-KEY: <api-key>                       # ingestion (metrics, events, logs)
DD-APPLICATION-KEY: <app-key>               # ALSO required for read / admin / dashboards / monitors
Content-Type: application/json
```

API key + optional App key. Rotate both on suspected compromise; API key alone CAN write but CAN'T read configured resources.

## Submit metrics

```
POST /api/v2/series
{
  "series": [{
    "metric": "orders.placed",
    "type": 3,                    // 1=count, 2=rate, 3=gauge
    "points": [{ "timestamp": 1713100000, "value": 1 }],
    "tags": ["env:prod", "service:checkout"],
    "unit": "order"
  }]
}
```

Prefer the Datadog Agent + DogStatsD (UDP socket `127.0.0.1:8125`) for high-volume in-process metrics — cheaper and avoids per-metric HTTP overhead. HTTP API is for edge/serverless where no agent is available.

## Submit events

```
POST /api/v1/events
{
  "title": "Deploy complete",
  "text": "Release v1.4.2 shipped to prod",
  "tags": ["env:prod", "deploy"],
  "alert_type": "info",           // info / warning / error / success
  "aggregation_key": "deploy-v1.4.2",
  "source_type_name": "ci"
}
```

Events show up in Event Explorer + can fire monitors.

## Submit logs

```
POST https://http-intake.logs.datadoghq.com/api/v2/logs
Content-Type: application/json
DD-API-KEY: ...

[{
  "ddsource": "my-app",
  "ddtags": "env:prod,service:api",
  "hostname": "web-1",
  "message": "user 42 logged in",
  "service": "api",
  "status": "info"
}]
```

Max 5MB per request, 1000 log entries per batch. Use `Content-Encoding: gzip` for >10KB payloads.

## APM / distributed tracing

Install the tracer:

```bash
pnpm add dd-trace
```

```ts
import tracer from "dd-trace";
tracer.init({ service: "my-api", env: "prod", version: "1.4.2" });
```

Auto-instruments Express, http, pg, mongodb, redis, graphql, etc. Agent exports to Datadog over UDP/HTTP. For serverless (Lambda/Cloudflare Workers), use `datadog-lambda-js` + the Datadog Forwarder.

## Monitors + alerting (API for IaC)

```
POST /api/v2/monitors
{
  "type": "metric alert",
  "query": "avg(last_5m):avg:orders.placed{env:prod}.as_rate() < 0.1",
  "name": "Order rate dropped",
  "message": "Investigate checkout. @slack-alerts",
  "tags": ["severity:high"]
}
```

Monitors-as-code: `datadog-ci` CLI or the Terraform provider for repeatable setup.

## Common gotchas

- **Wrong site → 403**: always check your UI URL's subdomain and match the API base.
- **Tags must be lowercased, `<key>:<value>` form**; higher-case or missing colon gets silently dropped.
- **`ddtags` on logs uses commas, not semicolons.**
- **`DD-APPLICATION-KEY` scope**: app keys have explicit permissions; missing a scope returns 403 with `auth_scopes_missing`.
- **Rate limits on monitor API**: 3600/hour per app key. Batch where possible.

## Key reference URLs

- API reference: https://docs.datadoghq.com/api/latest/
- Logs API: https://docs.datadoghq.com/api/latest/logs/
- Metrics API: https://docs.datadoghq.com/api/latest/metrics/
- APM (dd-trace): https://docs.datadoghq.com/tracing/
- Serverless: https://docs.datadoghq.com/serverless/
