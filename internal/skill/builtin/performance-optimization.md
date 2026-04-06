# performance-optimization

> Performance profiling, memory management, caching, and optimization patterns

<!-- keywords: performance, optimization, profiling, memory, cache, latency, throughput, benchmark, pprof, allocation, gc, pool -->

## Critical Rules

1. **Profile before optimizing.** "I think this is slow" is not a profile. Use `pprof` (Go), Chrome DevTools (JS), or your language's profiler. Optimize the actual bottleneck.

2. **Measure the right thing.** P50 is meaningless for user experience. Measure P95 and P99. One slow request in 100 means 1 in 3 users sees it per session.

3. **Cache invalidation is the hard problem.** If you add a cache, you must define: when does it expire? How is it invalidated? What happens when stale data is served?

4. **Memory allocations dominate Go performance.** Reduce allocations: reuse buffers (`sync.Pool`), pre-allocate slices (`make([]T, 0, cap)`), avoid string→[]byte conversions.

5. **Network is the bottleneck, not CPU.** Most web services are I/O-bound. Optimize network calls (batching, connection pooling, compression) before CPU.

## Go Performance Patterns

### Reduce Allocations
```go
// Bad: allocates on every call
func process(items []Item) []Result {
    var results []Result  // grows via append, multiple allocations
    for _, item := range items {
        results = append(results, transform(item))
    }
    return results
}

// Good: pre-allocate
func process(items []Item) []Result {
    results := make([]Result, 0, len(items))
    for _, item := range items {
        results = append(results, transform(item))
    }
    return results
}
```

### sync.Pool for Hot Paths
```go
var bufPool = sync.Pool{
    New: func() any { return new(bytes.Buffer) },
}

func encode(v any) ([]byte, error) {
    buf := bufPool.Get().(*bytes.Buffer)
    defer func() { buf.Reset(); bufPool.Put(buf) }()
    // use buf...
}
```

### String Builder (not concatenation)
```go
// Bad: O(n²) allocations
s := ""
for _, item := range items { s += item.Name + "," }

// Good: O(n) single allocation
var b strings.Builder
for _, item := range items { b.WriteString(item.Name); b.WriteByte(',') }
```

## Caching Strategy

### Cache Hierarchy
```
L1: In-process (sync.Map, LRU)     ~1μs
L2: Local Redis                      ~1ms
L3: Remote Redis cluster             ~5ms
L4: Database query                   ~50ms
L5: External API call                ~200ms
```

### Cache Patterns
- **Cache-aside:** App checks cache, misses go to DB, app populates cache
- **Write-through:** App writes to cache and DB simultaneously
- **Write-behind:** App writes to cache, async flush to DB (risk: data loss)
- **Read-through:** Cache itself fetches from DB on miss

### TTL Strategy
| Data Type | TTL |
|-----------|-----|
| Static config | 1 hour |
| User profile | 5 minutes |
| Search results | 30 seconds |
| Real-time data | No cache (or 1s) |
| File/asset metadata | 24 hours |

## Common Gotchas

- **Premature optimization.** Don't optimize code that runs once per request. Optimize code that runs once per item in a 10K-item loop.
- **JSON serialization cost.** `encoding/json` uses reflection. For hot paths, use `json-iterator` or code generation (`easyjson`).
- **DNS resolution on every request.** HTTP clients cache DNS by default, but custom resolvers may not. Check your client config.
- **Goroutine per connection.** Fine for 10K connections. At 1M, each goroutine's 4KB stack = 4GB. Use connection multiplexing.
- **GC pauses.** Large heaps with many pointers cause long GC pauses. Use value types, arenas, or `GOGC` tuning.
- **Database connection per request.** Connection setup takes 10-50ms. Pool connections. Reuse prepared statements.
