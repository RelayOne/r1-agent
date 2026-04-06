# load-testing

> Production load testing with k6, coordinated omission avoidance, and performance baseline methodology

<!-- keywords: load test, k6, vegeta, performance, latency, percentile, benchmark, coordinated omission, soak, capacity, hdr histogram -->

## The #1 Mistake: Coordinated Omission

In a closed-loop load generator, each connection waits for a response before sending the next request. When a request takes 5 seconds, the generator sends nothing during that window -- it coordinates with the system to avoid measuring during problematic periods. Gil Tene demonstrated cases where reported latency was 35,000x off at p99.99.

**Fix:** Use open-model load generation. In k6, use `constant-arrival-rate` instead of `constant-vus`. With Vegeta, constant-rate is the default. The test: "Does the generator fire requests on schedule even when previous requests haven't returned?" If not, your percentile data is fiction.

## Tool Selection

| Tool | Best For | CO Handling |
|------|----------|-------------|
| **k6** (primary) | Complex scenarios, CI gating | constant-arrival-rate executor |
| **Vegeta** | Precise Go endpoint benchmarks | By design (constant rate only) |
| **wrk2** | Gold-standard single-endpoint latency | Gold standard |
| **Artillery** | Playwright browser + API load | Partial |

For CI pipelines, k6 + Vegeta covers nearly everything.

## k6 Open-Model Configuration

```javascript
export const options = {
  scenarios: {
    open_model: {
      executor: 'constant-arrival-rate',
      rate: 500,              // 500 RPS
      timeUnit: '1s',
      duration: '5m',
      preAllocatedVUs: 100,
      maxVUs: 200,
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<300', 'p(99)<800'],
    http_req_failed: [{ threshold: 'rate<0.01', abortOnFail: true }],
  },
};
```

## Latency: Never Report Averages

A service with p50=5ms and p99=2000ms shows "average" ~25ms. In a microservices architecture calling 15 backend APIs, the probability of hitting at least one p99 outlier is 1-(0.99^15) = 14%. Track:

- **p50** for broad regression detection
- **p95** for SLO targets
- **p99** for architectural bottleneck identification
- **p99.9** for latency-sensitive paths (payments, real-time)

Use **HDR Histogram** for precision: O(1) recording (~3-6 ns), constant memory regardless of sample count, configurable significant figures.

## Realistic Test Design

1. **Mine production workload** from APM/access logs. Extract top 5-10 user journeys with relative weights.
2. **Think time:** Use log-normal distribution (Box-Muller transform), not constant pauses. Constant pauses create synchronized bursts.
3. **Data parameterization:** Use Zipfian distributions for hot/cold access patterns. Unique IDs per VU prevent artificial cache warming.
4. **Environment parity:** A DB with 100 rows behaves differently than 100M rows. Use same Terraform/K8s manifests as production.

## PostgreSQL Under Load

- `MaxOpenConns` defaults to 0 (unlimited) -- set explicitly to `(PG max_connections / app_instances) - buffer`
- `MaxIdleConns` defaults to 2 -- too low. Increase to 25-50% of MaxOpenConns. GoCardless reduced DB load 30-60% by going from 2 to 6.
- Monitor `db.Stats().WaitCount` and `WaitDuration` for pool queueing
- Under write-heavy load, watch autovacuum and dead tuple accumulation

## Soak Testing (4-72 hours at 60-80% capacity)

Catches what load tests cannot: goroutine leaks, connection leaks, memory growth, latency drift.

**Go:** Monitor `runtime.NumGoroutine()` via Prometheus. `time.After` in select loops leaks timers. Use `goleak` in unit tests.

**Node.js/TypeScript:** `emitter.on()` without `removeListener()`, closures capturing large objects, unfinished streams.

**Pass/fail:** Memory growth < 10% over duration, no latency drift > 15%, error rate < 0.1%, goroutine count stable.

## Capacity Planning

Define a **Service Capacity Unit (SCU):** load-test a single pod to find max RPS meeting your SLO. Required pods = `(Target RPS x Safety Factor) / RPS_per_pod`. Google SRE N+2 rule: provision so peak traffic works while 2 largest instances are unavailable.

**Verify auto-scaling works:** HPA scale-up latency is 30s to 4 minutes end-to-end. Run k6 ramp-up while watching `kubectl get hpa --watch`. Test scale-down too. CPU-based HPA misses I/O-bound workloads -- use custom metrics (RPS, queue depth) via KEDA.

## Chaos Under Load

Most incidents occur at peak load AND component failure. Use **toxiproxy** (Shopify, Go) to inject network faults programmatically from `go test`. Test: what happens when 10,000 users hit checkout while the payment service has 500ms added latency?

## CI Integration

Run k6 with threshold-based pass/fail in CI. Weekly soak tests as scheduled jobs with 4-6 hour timeouts. Use Infracost to estimate infrastructure cost changes per PR.
