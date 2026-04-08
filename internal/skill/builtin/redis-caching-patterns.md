# redis-caching-patterns

> Multi-layer caching with Redis/Valkey, stampede prevention, hot key mitigation, memory optimization, and invalidation strategies

<!-- keywords: redis, valkey, cache, caching, singleflight, stampede, ttl, lru, lfu, invalidation, bloom filter, cache aside, write through, hot key, eviction -->

## Critical Rules

1. **Cache-aside is the default pattern.** Check cache, query DB on miss, populate cache. On write, update DB then delete the cache key (not update). Uber uses this at 150M+ reads/sec with 99.9% hit rate.

2. **Every TTL must have jitter.** `effective_ttl = base_ttl + random(0, base_ttl * 0.2)`. Without jitter, bulk loads or deployments cause thousands of keys to expire simultaneously, hammering the database.

3. **Every key costs ~100 bytes before your data.** A key `user:12345` (10 bytes) with a 23-byte value consumes ~99 bytes total in Redis. The ~3x overhead ratio means 10M keys at 1GB useful data requires 3GB Redis memory.

4. **Use allkeys-lfu eviction.** LFU (Redis 4.0+) prevents rarely-accessed items from evicting consistently popular ones. LRU is simpler but lets a burst of cold keys evict hot data. Monitor hit rate: target >95%.

5. **Pipelining is the real throughput multiplier.** A single connection sending 1,000 commands individually at 1ms RTT takes ~1 second. Pipelined: ~10-20ms. Cross-datacenter: 50-100x improvement. Always prefer pipelining over adding connections.

## Cache Stampede Prevention (Layer These)

**Singleflight (per-process):** Go's `singleflight.Group` deduplicates concurrent calls. When multiple goroutines request the same key, only one executes the fetch; others block and share the result. Node.js equivalent: store the in-flight Promise in a Map so concurrent callers await the same promise.

**Distributed locking (cross-process):** `SET lock:{key} {unique_id} NX EX {timeout}`. Only one process recomputes; others poll. Release must be atomic via Lua script (check ownership before delete). Single-instance locks suffice; Redlock is overkill for stampede prevention.

**XFetch probabilistic early revalidation:** `shouldRecompute = (currentTime - delta * beta * ln(random())) >= expiry`. Achieves O(1) expected stampede size with no coordination, no latency impact. Store computation duration (`delta`) alongside each cached value. Used by Cloudflare.

| Technique | Scope | Stampede Size | Latency Impact |
|-----------|-------|--------------|----------------|
| Singleflight | Single process | 1 per process | Waiters blocked |
| Distributed lock | Cross-process | 1 globally | Waiters poll |
| XFetch | Any | O(1) expected | None |

## Multi-Layer Cache Coherence (L1 -> L2 -> L3)

L1 (in-process, ~10ns): ristretto or go-redis/cache with TinyLFU. L2 (Redis, ~0.5-2ms). L3 (PostgreSQL, ~5-50ms).

**Redis Pub/Sub as invalidation backplane:** When any instance updates DB and invalidates Redis, it publishes on a dedicated channel. All instances subscribe and evict corresponding L1 entries. Pub/Sub is fire-and-forget; combine with short L1 TTLs (30-60s) as safety net. For stronger durability, use Redis Streams (persistent, replayable).

**TOCTOU race:** Instance A reads stale from L2, invalidation broadcast arrives (no-op since not cached locally), Instance A writes stale to L1. Bounded only by L1 TTL. For data requiring strong consistency, skip L1 and read from Redis directly.

## Hot Key Detection and Mitigation

When one key absorbs majority traffic, it bottlenecks a single Redis shard. Detection: `redis-cli --hotkeys` (requires LFU), `OBJECT FREQ <key>`, or client-side frequency tracking with Count-Min Sketch.

- **Auto-promote to L1**: When a key is detected hot, cache in-process with 5-30s TTL. Eliminates Redis round-trips entirely.
- **Key splitting**: Distribute across N replicas (`product:iphone:1` through `product:iphone:N`), randomly selected on read. Each maps to a different hash slot.
- **Server-assisted client-side caching**: Redis 6+ `CLIENT TRACKING` pushes invalidation messages. Broadcasting mode (`PREFIX user:`) eliminates server-side memory cost. Redisson reports 45x faster reads.

## Negative Caching Stops Cache Penetration

Cache sentinel values (`"__NULL__"`) with short TTL (30-60s) when DB returns empty. For adversarial workloads (random invalid IDs), use Bloom filters: 1M items at 1% false positive rate costs only ~1.2MB. Stack defenses: rate limiting -> input validation -> Bloom filter -> negative caching -> circuit breaker.

## Cache Versioning for Rolling Deployments

Version prefix in keys (`v2:user:123`). During deploys, use dual-read: try new key first, fall back to old key with transformation. Protobuf reduces version bump frequency (forward-compatible by design). After all instances are on new version, old keys expire via TTL.

## Redis Memory Optimization

- **Hash ziplist/listpack**: Hashes under 128 entries with values under 64 bytes use contiguous memory (~15-25 bytes/field vs ~80-100). The hash bucketing pattern (`HSET users:${floor(id/1000)} ${id} ${data}`) exploits this for 5x memory savings.
- **Embstr encoding**: Strings <= 44 bytes use a single 64-byte allocation. Longer strings need two allocations.
- **Skip caching for sub-0.1ms computations**: If Redis GET takes 0.2ms but computation takes 0.1ms, caching is slower.
- **Monitor fragmentation**: `mem_fragmentation_ratio` above 1.5 warrants `activedefrag`. Track `evicted_keys` growth.

## Valkey: The Redis Fork

Valkey forked from Redis 7.2.4 (March 2024) after the BSL license change. Backed by AWS, Google, Oracle. Valkey 8.0 benchmarks at 1.19M RPS (230% over 7.2). Valkey 9.0 adds atomic slot migration, hash field expiration, and 2,000-node cluster support. All existing Redis clients (go-redis, ioredis) work unchanged. AWS ElastiCache for Valkey is ~20% cheaper than Redis. Key gap: Active-Active CRDT replication remains Redis Enterprise-only.

## Common Gotchas

- **Redis is single-threaded per shard.** More connections do not increase throughput. go-redis defaults to 10 * GOMAXPROCS, which is almost always sufficient.
- **Pub/Sub messages are not persisted.** Instances down during publish miss the invalidation. Always pair with TTL.
- **MONITOR reduces throughput by ~50%.** Use only briefly for debugging. Prefer `redis-cli --hotkeys` or client-side tracking.
- **Adding a TTL costs ~32 extra bytes per key** (extra dictEntry in expires dictionary).
- **Consistent hashing with hash tags**: `{user:1001}.profile` and `{user:1001}.settings` force related keys to the same Redis Cluster slot, enabling multi-key operations and Lua scripts.
