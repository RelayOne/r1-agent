# segment

> Twilio Segment CDP: server-side `track` / `identify` / `group` / `page` / `screen` events routed to N destinations. One integration point, fan-out to analytics / email / ad platforms.

<!-- keywords: segment, cdp, customer data platform, track, identify, source, destination, writekey -->

**Official docs:** https://segment.com/docs  |  **Verified:** 2026-04-14 via web search.

## Endpoint + auth

- Track base: `https://api.segment.io/v1/`
- Auth: HTTP Basic — `Authorization: Basic <base64(WRITE_KEY:)>` (write key as username, empty password) OR include as `writeKey` in request body.
- Each **Source** has its own write key. Don't reuse across environments.

## Node SDK

```bash
pnpm add @segment/analytics-node
```

```ts
import { Analytics } from "@segment/analytics-node";
const analytics = new Analytics({ writeKey: process.env.SEGMENT_WRITE_KEY! });

analytics.track({
  userId: "u_42",
  event: "Order Completed",
  properties: { orderId: "123", total: 99, currency: "USD" },
});

analytics.identify({
  userId: "u_42",
  traits: { email: "a@b.com", plan: "pro" },
});

analytics.group({ userId: "u_42", groupId: "acme-corp", traits: { tier: "enterprise" } });

analytics.page({ userId: "u_42", name: "Pricing", properties: { plan: "pro" } });

await analytics.closeAndFlush();    // MUST call before process exit or events drop
```

## Direct HTTP

```
POST https://api.segment.io/v1/track
Content-Type: application/json
Authorization: Basic <base64(writeKey:)>

{
  "userId": "u_42",
  "event": "Order Completed",
  "properties": { "total": 99 },
  "messageId": "evt_abc",           // dedup key; retries idempotent
  "timestamp": "2026-04-14T12:00:00Z"
}
```

Endpoints: `/track`, `/identify`, `/group`, `/page`, `/screen`, `/alias`, `/batch`.

## Batch API

```
POST /v1/batch
{
  "batch": [
    { "type": "track", "userId": "u_42", "event": "...", "properties": {...} },
    { "type": "identify", "userId": "u_42", "traits": {...} }
  ]
}
```

Up to 500KB per request, 32KB per call. Use for high-volume server-side pipelines.

## Spec adherence matters

Segment has a **Semantic Events Spec** (Order Completed, Product Viewed, Signed Up, etc.) that downstream destinations know how to route. Using the exact event names + property names the spec defines means destinations (Amplitude, Mixpanel, Google Ads, etc.) get correctly-formatted events without per-destination mapping.

When in doubt: `https://segment.com/docs/connections/spec/`.

## Aliasing anonymous → identified users

```ts
// Before login: user is tracked with anonymousId (auto-generated UUID, stored in localStorage)
analytics.track({ anonymousId: anonId, event: "..." });

// After login: stitch the histories
analytics.alias({ previousId: anonId, userId: actualUserId });
```

Call `alias` ONCE per user, right after signup/login. Double-aliasing breaks the history.

## Destinations are dashboard-configured

Segment's power is that `track()` fan-outs to N destinations (Amplitude, Mixpanel, Braze, Facebook Ads, etc.) via dashboard config. You don't call each destination's SDK — just call Segment once.

Filter per-destination: dashboard → Source → Destinations → per-destination event filters + property mapping.

## Common gotchas

- **Forgetting `closeAndFlush()` in Lambda**: events collected in memory, Lambda exits, events lost.
- **`messageId` missing on retries**: duplicate events.
- **Exceeding 32KB single-event limit**: truncation or rejection; don't stuff huge blobs in properties.
- **Region mismatch**: EU workspaces use `https://events.eu1.segmentapis.com/v1/`.

## Key reference URLs

- HTTP API source: https://segment.com/docs/connections/sources/catalog/libraries/server/http-api/
- Node SDK: https://segment.com/docs/connections/sources/catalog/libraries/server/node/
- Semantic Events Spec: https://segment.com/docs/connections/spec/
- Batch API: https://segment.com/docs/connections/sources/catalog/libraries/server/http-api/#batch
