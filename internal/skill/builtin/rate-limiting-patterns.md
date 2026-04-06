# rate-limiting-patterns

> Rate limiting algorithms, Redis Lua atomicity, distributed limiting, cost-based metering, and per-endpoint design

<!-- keywords: rate limit, token bucket, sliding window, leaky bucket, throttle, 429, retry-after, lua, redis, abuse, ddos, circuit breaker, load shedding -->

## Critical Rules

1. **Token bucket is the default algorithm.** It permits controlled bursts up to bucket capacity while enforcing sustained throughput. O(1) space, O(1) time. Used by Stripe, AWS, and Go's `golang.org/x/time/rate`. Tune two parameters: capacity (burst size) and refill rate.

2. **Rate limiting requires atomic Lua scripts in Redis.** MULTI/EXEC cannot express conditional logic. WATCH/MULTI aborts under concurrency. Lua scripts via EVAL execute atomically with full branching in a single round trip.

3. **Rate limiting, load shedding, and circuit breakers serve different purposes.** Rate limiting controls per-client throughput. Load shedding protects the system from aggregate overload. Circuit breakers prevent cascading failures on outbound calls. Layer all three from outside in.

4. **IPv6 renders per-address rate limiting useless.** A single user receives a /64 prefix (18.4 quintillion addresses). Rate limit by /64 prefix minimum. Track /64, /56, and /48 simultaneously with progressively higher thresholds.

5. **Return 429 with Retry-After header.** Use RFC 9457 Problem Details (`application/problem+json`) for response bodies. Never cache 429 responses (RFC 6585).

## Algorithm Selection

| Algorithm | Redis Type | Memory | Accuracy | Burst | Best For |
|-----------|-----------|--------|----------|-------|----------|
| Token bucket | HASH (2 fields) | O(1) | Exact | Controlled | General-purpose APIs |
| Sliding window log | SORTED SET | O(n) | Exact | None | Payment/auth endpoints |
| Fixed window counter | STRING | O(1) | Approximate | 2x at boundary | Simple internal throttling |
| Leaky bucket | HASH | O(1) | Exact | None | Traffic shaping |
| Sliding window counter | STRING x2 | O(1) | Near-exact | Smoothed | Best accuracy/memory ratio |

## Token Bucket Lua Script

```lua
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local refill_rate = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])
local bucket = redis.call('HMGET', key, 'tokens', 'last_refill')
local tokens = tonumber(bucket[1]) or capacity
local last_refill = tonumber(bucket[2]) or now
tokens = math.min(capacity, tokens + (now - last_refill) * refill_rate)
if tokens < requested then
    redis.call('HMSET', key, 'tokens', tokens, 'last_refill', now)
    redis.call('EXPIRE', key, math.ceil(capacity / refill_rate) + 1)
    return {0, math.floor(tokens * 100) / 100, math.ceil((requested - tokens) / refill_rate)}
end
tokens = tokens - requested
redis.call('HMSET', key, 'tokens', tokens, 'last_refill', now)
redis.call('EXPIRE', key, math.ceil(capacity / refill_rate) + 1)
return {1, math.floor(tokens * 100) / 100, 0}
```

For multi-key algorithms in Redis Cluster, use hash tags (`{ratelimit:user123:swc}:5`) to ensure all keys land on the same slot.

## Per-Endpoint Limit Design

- **Auth endpoints**: 5-10 req/min. Strictest limits. Apply per-IP, per-account, per-device simultaneously.
- **Read endpoints**: 1,000+ req/min. Generous. Support ETags; exclude 304s from counting.
- **Write endpoints**: ~100 req/min. POST/PUT/DELETE modify state and trigger downstream processes.
- **Search endpoints**: Use complexity weighting (basic filter: 2 points, aggregation: 5, wildcard: 10).
- **GraphQL**: Point-based cost. GitHub: 5,000 points/hour. Cost = product of connection sizes at each nesting level.
- **Tiered limits**: Free 100/min, basic 1,000, premium 10,000. Apply per-user and per-org simultaneously.

## Distributed Rate Limiting Architectures

**Centralized Redis**: Simplest. All instances share Redis. 1-5ms latency per check. Fail open if Redis is down (Stripe's practice).

**Local with periodic sync**: Each server maintains local counters, syncs to Redis every 100ms-1s. Accepts eventual consistency (~5% over-admission). Not for security-critical limits.

**Poisson statistical limiter**: For fixed-shard systems. Global limit / N shards, use 95th percentile of Poisson distribution per shard. Zero coordination, zero failure modes.

## Cost-Based Rate Limiting

Traditional request-count limiting fails for AI/compute APIs where one request can cost 100x another. Assign cost weights per operation and budget by cost, not count. Read actual token consumption from LLM responses and apply retroactively. Set per-tenant compute budgets with soft caps (alerts) and hard caps (suspend). Fire usage alerts at 75%, 90%, 100%.

## Graduated Abuse Response

Five escalation levels: monitoring -> stricter limits -> challenge-response (CAPTCHA) -> temporary block (exponential: 15min, 1h, 24h) -> permanent ban (requires human review).

Abuse scoring signals: request rate vs baseline, failed auth ratio, sequential access patterns, geographic anomalies, IP reputation, TLS fingerprint. Scores decay over time (halve every 24h without violations).

## IETF Standard Rate Limit Headers (draft-10+)

```http
RateLimit-Policy: "burst";q=100;w=60, "daily";q=1000;w=86400
RateLimit: "burst";r=57;t=43
```

Parameters: `q` (quota), `w` (window seconds), `r` (remaining), `t` (seconds until reset). Reset uses delay-seconds, not Unix timestamps.
