# amplitude

> Amplitude HTTP V2 API for event ingestion — `POST /2/httpapi`. Batch up to ~10 events per request; always include `insert_id` for dedup.

<!-- keywords: amplitude, analytics, event tracking, http v2, amplitude api -->

**Official docs:** https://amplitude.com/docs/apis/analytics/http-v2  |  **Verified:** 2026-04-14 via web search.

## Endpoint + auth

- US: `POST https://api2.amplitude.com/2/httpapi`
- EU: `POST https://api.eu.amplitude.com/2/httpapi`
- Content-Type: `application/json`
- Auth: API key in the request body (`api_key` field). No Authorization header.

## Request shape

```json
{
  "api_key": "YOUR_API_KEY",
  "events": [{
    "user_id": "u_42",
    "event_type": "Order Placed",
    "insert_id": "order_123_try_1",
    "event_properties": { "total": 19.99, "currency": "USD" },
    "user_properties": { "plan": "pro" },
    "time": 1713100000000,
    "device_id": "device-uuid",
    "platform": "web",
    "app_version": "1.4.2"
  }]
}
```

Required: exactly one of `user_id` or `device_id`, plus `event_type`.

Response:
- `200` — events accepted
- `400` — malformed payload (check `error`)
- `413` — too large (split)
- `429` — rate limited (`Retry-After` header)

## Rate limits

- 1000 events/second and 100 batches/second per project.
- Keep each batch ≤ 10 events, ≤ 1 MB, ≤ 2000 events per request.
- 430 daily events per user per device before throttling (tuneable upon request).

## Dedup with `insert_id`

MANDATORY for retriable code paths. Generate a UUID at event creation; retry with same ID is a no-op server-side. Without this, network retries double-count.

## User properties

Set via `user_properties` on events or via the Identify API (`POST /identify`). `user_properties` support operators:

```json
{
  "user_properties": {
    "$set": { "plan": "pro" },
    "$setOnce": { "first_seen_at": "2026-01-01" },
    "$add": { "orders_count": 1 },
    "$append": { "favorite_categories": "electronics" }
  }
}
```

## Group analytics (accounts / orgs)

```json
{
  "event_type": "Deploy Completed",
  "user_id": "u_42",
  "groups": { "company": "acme-corp" },
  "group_properties": { "company": { "tier": "enterprise" } }
}
```

Requires Group Analytics enabled on your plan.

## SDK alternative

Node SDK `@amplitude/analytics-node` handles batching, retries, and EU routing. Prefer SDK for volume; HTTP direct for serverless where keeping an SDK instance alive is awkward.

```ts
import { createInstance } from "@amplitude/analytics-node";
const amp = createInstance();
amp.init(API_KEY).promise;
amp.track({ user_id, event_type: "Order Placed", event_properties: { total } });
await amp.flush().promise;   // flush before Lambda exit
```

## Batch API (for > 10 events)

`POST /batch` has the same shape but allows up to 1000 events per request and is throttled to 1000 events/sec instead of the HTTP V2 limit. Use for offline backfills, not real-time.

## Common gotchas

- **Dropping events silently**: if the payload has an invalid field type, Amplitude returns 200 but the event is silently dropped. Validate field types before send; check the response's `events_ingested` count when using `/batch`.
- **Time is in MILLISECONDS (epoch)** — common bug is sending seconds.
- **Reserved event names**: `Amplitude` prefix is reserved; don't use.
- **`user_id` max 128 chars**; longer IDs get truncated silently.

## Key reference URLs

- HTTP V2 API: https://amplitude.com/docs/apis/analytics/http-v2
- Batch API: https://amplitude.com/docs/apis/analytics/batch-event-upload
- Identify API: https://amplitude.com/docs/apis/analytics/identify
- SDKs: https://amplitude.com/docs/sdks
