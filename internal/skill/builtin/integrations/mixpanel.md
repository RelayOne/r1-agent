# mixpanel

> Mixpanel product analytics: server-side `track()` for events, `people.set()` for user traits, `import()` for events older than 5 days.

<!-- keywords: mixpanel, product analytics, event tracking, funnel, retention, cohort -->

**Official docs:** https://docs.mixpanel.com  |  **Verified:** 2026-04-14 via web search.

## Install + init (Node)

```bash
pnpm add mixpanel
```

```ts
import Mixpanel from "mixpanel";
const mp = Mixpanel.init(process.env.MIXPANEL_PROJECT_TOKEN!, {
  host: "api.mixpanel.com",   // use "api-eu.mixpanel.com" for EU residency
});
```

## Track events

```ts
mp.track("Order Placed", {
  distinct_id: userId,     // REQUIRED — the user's stable identifier
  $insert_id: orderId,     // dedup key; same insert_id discarded on retry
  total: 1999,
  currency: "USD",
  items: 3,
});
```

The server-side `/track` endpoint only accepts events with timestamps within the last **5 days**. Older events → use `mp.import(...)` with the `/import` endpoint.

## User properties

```ts
mp.people.set(userId, {
  $email: "a@b.com",
  $name: "Alex",
  plan: "pro",
  signup_date: new Date().toISOString(),
});
mp.people.set_once(userId, { first_order_at: nowIso });  // only set if not already set
mp.people.increment(userId, "lifetime_orders", 1);
```

Properties prefixed with `$` are reserved (Mixpanel auto-fills some, you can set `$email`, `$name`, `$avatar`).

## Alias (stitch anonymous → known user)

```ts
mp.alias(newUserId, anonymousDistinctId);
// Subsequent track() with distinct_id: newUserId reunites history with pre-signup events.
```

Call once, right after user signup — calling twice or in the wrong direction silently fails.

## Batch import (historical backfill)

```ts
mp.import_batch([
  { event: "Order Placed", properties: { distinct_id, time: pastSeconds, ... } },
], { project_id: PROJECT_ID });   // requires project_id + API secret
```

Used for backfilling historical events > 5 days old. Different auth (API secret, not project token).

## Direct HTTP (no SDK)

```
POST https://api.mixpanel.com/track
Content-Type: application/json

[{"event":"Order Placed","properties":{"distinct_id":"u_42","token":"PROJECT_TOKEN","$insert_id":"evt_123","total":1999}}]
```

Response: `1` on success, `0` on validation failure (check body JSON for error detail).

## Common gotchas

- **`distinct_id` must be stable** — changing it between events fragments user timelines.
- **`$insert_id` is critical** — without it, network retries double-count events. Use a UUID per logical event.
- **EU data residency**: must init with `host: "api-eu.mixpanel.com"` AND the project must be created as EU; you can't migrate an existing US project.
- **SDK lib is client-side first**: the Node library is reliable but has fewer features than the JS SDK. For server-side volume, the HTTP API is fine + cheaper to maintain.

## Key reference URLs

- Track events: https://developer.mixpanel.com/reference/track-event
- Node SDK: https://docs.mixpanel.com/docs/tracking-methods/sdks/nodejs
- Server-side best practices: https://docs.mixpanel.com/docs/tracking-best-practices/server-side-best-practices
- Import API: https://developer.mixpanel.com/reference/import-events
