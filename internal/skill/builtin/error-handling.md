# error-handling

> Error handling, retry strategies, graceful degradation, and observability

<!-- keywords: error, retry, fallback, graceful, degradation, circuit breaker, timeout, backoff, jitter, panic, recover, observability -->

## Critical Rules

1. **Never swallow errors silently.** `_ = fn()` is acceptable only when documented why. Every error path must be logged, returned, or explicitly handled.

2. **Retries must have jitter.** `time.Sleep(attempt * baseDelay)` causes thundering herd. Add random jitter: `delay + rand(0, delay/2)`.

3. **Timeouts must form a hierarchy.** HTTP handler timeout > service call timeout > DB query timeout. If inner > outer, the inner timeout is useless.

4. **Distinguish transient from permanent errors.** Retry network errors. Don't retry validation errors. Don't retry 4xx (except 429). Always retry 503.

5. **Circuit breakers prevent cascade failures.** After N consecutive failures, stop calling. Half-open: try one call after cooldown. Reset on success.

## Retry Patterns

### Exponential Backoff with Jitter
```
delay = min(baseDelay * 2^attempt + random(0, baseDelay), maxDelay)
```
- Base: 100ms, Max: 30s, Attempts: 5
- Never retry without a cap. Infinite retry = infinite resource consumption.

### Retry Budget
- Track retry rate across the service (not per-request)
- If retry rate > 10% of total traffic, stop retrying (you're amplifying an outage)
- Shared retry budget prevents retry storms

## Error Classification

| Class | Retry? | Example |
|-------|--------|---------|
| Transient | Yes | Network timeout, 503, connection reset |
| Rate limit | Yes (with backoff) | 429, quota exceeded |
| Client error | No | 400, 404, validation failure |
| Auth error | No | 401, 403 |
| Server error | Maybe | 500 (may be transient or permanent) |
| Data error | No | Constraint violation, schema mismatch |

## Graceful Degradation

1. **Feature flags for new code paths.** If the new path fails, fall back to the old path.
2. **Default values for missing config.** Don't crash on missing optional config.
3. **Cached responses on upstream failure.** Serve stale data with a warning header.
4. **Partial success is valid.** If 8/10 items process, return the 8 with errors for the 2.
5. **Graceful shutdown:** Stop accepting new work, drain in-flight requests (30s max), then exit.

## Common Gotchas

- **`context.DeadlineExceeded` vs `context.Canceled`.** Deadline = timeout. Canceled = caller gave up. Handle differently.
- **Wrapping errors loses type info.** Use `fmt.Errorf("context: %w", err)` to preserve `errors.Is/As` semantics.
- **Panic in goroutine kills the process.** Every `go func()` needs `defer recover()` if the goroutine can panic.
- **HTTP client without timeout.** `&http.Client{}` has no timeout. Always set `Timeout: 30 * time.Second`.
- **Logging the full error chain.** Log the root cause, not just the wrapper. Use `errors.Unwrap` chain.
- **`os.Exit()` skips defers.** Use `log.Fatal` only in `main()`. In libraries, return errors.
