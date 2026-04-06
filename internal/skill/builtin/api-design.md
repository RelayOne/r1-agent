# api-design

> REST API design, versioning, pagination, rate limiting, and backward compatibility

<!-- keywords: api, rest, endpoint, pagination, versioning, rate limit, webhook, graphql, grpc, openapi -->

## Critical Rules

1. **Never break existing clients.** Additive changes only: new fields, new endpoints. Removing fields or changing types is a breaking change.

2. **Paginate all list endpoints.** No endpoint should return unbounded results. Use cursor-based pagination (not offset) for large datasets.

3. **Version your API from day one.** `/v1/resource` in the URL path. Header-based versioning is harder for clients to debug.

4. **Idempotency keys for mutations.** POST/PUT with `Idempotency-Key` header. Store and check before processing. Retries must not double-create.

5. **Return consistent error format.** Every error must have `code`, `message`, and optional `details`. Never return raw stack traces.

## Design Patterns

### Response Envelope
```json
{
  "data": { ... },
  "meta": { "request_id": "abc", "timestamp": "..." },
  "errors": null
}
```

### Cursor Pagination
```json
{
  "data": [...],
  "pagination": {
    "next_cursor": "eyJpZCI6MTIzfQ==",
    "has_more": true,
    "limit": 25
  }
}
```
- Encode cursor as opaque base64 (not raw ID)
- Support `limit` parameter with max (e.g., 100)
- Never expose internal IDs in cursors

### Rate Limiting Headers
```
X-RateLimit-Limit: 100
X-RateLimit-Remaining: 42
X-RateLimit-Reset: 1620000000
Retry-After: 30
```
- Use token bucket or sliding window
- Per-API-key, not per-IP (IPs can be shared)
- Return 429 with `Retry-After` header

## Backward Compatibility Checklist

- [ ] New fields have default values
- [ ] Removed fields still accepted (ignored) for 2 versions
- [ ] Changed field types use new field name
- [ ] New required fields are added as optional first
- [ ] Enum values only added, never removed
- [ ] Response shape changes are additive only
- [ ] Webhooks include version in payload

## Common Gotchas

- **BigInt in JSON:** JavaScript loses precision above 2^53. Use string for IDs.
- **Empty array vs null:** `"items": []` and `"items": null` mean different things. Be consistent.
- **UTC timestamps:** Always ISO 8601 with timezone: `2024-01-15T09:30:00Z`. Never local time.
- **Trailing slashes:** `/users` and `/users/` should behave identically. Pick one and redirect.
- **Boolean query params:** `?active=true` vs `?active=1` vs `?active`. Define one format.
- **Bulk operations:** Don't make clients loop. Provide batch endpoints: `POST /users/bulk`.
- **Webhook reliability:** Webhooks fail. Provide retry with exponential backoff and a manual retry button in the dashboard.
