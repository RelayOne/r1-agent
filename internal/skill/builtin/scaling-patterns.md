# scaling-patterns

> Decision framework and implementation patterns for scaling applications from hundreds to millions of users.

<!-- keywords: scaling, horizontal scaling, sharding, caching, load balancing, microservices -->

## Horizontal vs Vertical Scaling

1. **Vertical scaling** (bigger machine): simpler operations, no distributed coordination, hard ceiling around 128 cores / 2 TB RAM.
2. **Horizontal scaling** (more machines): near-unlimited ceiling, requires stateless services, adds network complexity.
3. Start vertical until you hit CPU, memory, or I/O ceiling. Horizontal scaling introduces distributed systems problems -- don't pay that cost prematurely.
4. Stateless services scale horizontally by default. Move session data to Redis or JWTs first.
5. Database is almost always the first bottleneck. Scale reads with replicas, writes with sharding.

## Database Sharding Strategies

1. **Range-based**: shard by date range or ID range. Simple but creates hotspots on the latest shard.
2. **Hash-based**: `shard = hash(tenant_id) % N`. Even distribution but resharding requires data migration.
3. **Directory-based**: lookup table maps keys to shards. Flexible but the directory is a single point of failure.
4. Shard by tenant/org ID in multi-tenant SaaS -- queries rarely cross tenant boundaries.
5. Avoid cross-shard joins. Denormalize or use an async aggregation layer.
6. Plan for resharding from day one: use consistent hashing (virtual nodes) to minimize data movement.
7. Consider Vitess, Citus, or PlanetScale before building custom sharding logic.

## Caching Layers

1. **L1 -- Application cache**: in-process LRU (Guava, groupcache, `sync.Map`). Microsecond access, limited to instance memory.
2. **L2 -- Distributed cache**: Redis or Memcached. Sub-millisecond over network, shared across instances.
3. **L3 -- CDN**: CloudFront, Cloudflare. Cache static assets and API responses at the edge.
4. Cache invalidation strategies:
   - **TTL**: simple, tolerates staleness window. Good default.
   - **Write-through**: update cache on write. Consistent but adds write latency.
   - **Write-behind**: queue cache updates asynchronously. Fast writes, eventual consistency.
   - **Event-driven**: publish change events, subscribers invalidate. Best for microservices.
5. Cache stampede prevention: use distributed locks or `singleflight` for expensive computations.
6. Monitor cache hit ratio -- below 90% indicates sizing or key design problems.

## Connection Pooling

1. Database: pool size = `(core_count * 2) + spindle_count` (PGBouncer rule of thumb).
2. Use PgBouncer or ProxySQL in front of PostgreSQL/MySQL for connection multiplexing.
3. HTTP clients: reuse connections with keep-alive. Set `MaxIdleConnsPerHost` proportional to target concurrency.
4. Redis: pool size matches expected concurrent command count. Pipeline commands to reduce round trips.
5. Monitor active vs idle connections. Leaked connections manifest as pool exhaustion under load.

## Message Queues for Async Processing

1. Use queues for any work that does not need a synchronous response: email, PDF generation, webhooks, analytics.
2. **SQS/Redis Streams**: simple task queues, at-least-once delivery.
3. **Kafka**: ordered event streaming, replay capability, high throughput (millions/sec).
4. **NATS**: lightweight pub/sub with JetStream for persistence.
5. Ensure idempotency: consumers must handle duplicate messages safely (use idempotency keys).
6. Dead-letter queues catch poison messages after N retries -- monitor and alert on DLQ depth.
7. Back-pressure: producers should slow down or shed load when queue depth exceeds threshold.

## Rate Limiting Implementation

1. **Token bucket**: allows burst up to bucket capacity, refills at steady rate. Best for API rate limiting.
2. **Sliding window**: count requests in a rolling time window. More accurate than fixed windows.
3. Implement at the API gateway level (Kong, Envoy) for global limits, and in-app for per-user limits.
4. Return `429 Too Many Requests` with `Retry-After` header.
5. Use Redis `INCR` + `EXPIRE` for distributed rate limiting across instances.
6. Differentiate limits by tier: free (100/hr), pro (10,000/hr), enterprise (custom).

## Circuit Breaker Pattern

1. States: **Closed** (normal) -> **Open** (failing, reject fast) -> **Half-Open** (test recovery).
2. Trip the breaker after N consecutive failures or error rate > threshold in a window.
3. Open state returns fallback immediately -- cached data, default value, or graceful degradation.
4. Half-open lets a single probe request through. Success closes the circuit; failure reopens.
5. Use per-dependency breakers: a failing payment service should not break user lookup.
6. Libraries: `gobreaker` (Go), `resilience4j` (Java), `opossum` (Node.js).

## Read Replicas and CQRS

1. Route reads to replicas, writes to primary. Use middleware or a query router.
2. Replication lag (typically 10-100ms) means reads may be stale. Acceptable for dashboards, not for "read-after-write" flows.
3. For read-after-write consistency: route the user's own reads to primary for a short window after writes.
4. **CQRS** (Command Query Responsibility Segregation): separate write model (normalized, transactional) from read model (denormalized, fast).
5. The read model is rebuilt from events or change streams. Use materialized views or dedicated read stores (Elasticsearch, DynamoDB).
6. CQRS adds operational complexity. Adopt only when read and write patterns have fundamentally different scaling requirements.
7. Event sourcing pairs naturally with CQRS but is not required -- you can use CQRS with a traditional database.
