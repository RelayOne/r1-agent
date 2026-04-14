# posthog

> PostHog product analytics + feature flags + session replay + experiments. Open-source, self-hostable. `/i/v0/e` for events, `/flags` for feature flag eval.

<!-- keywords: posthog, product analytics, feature flags, feature flag, session replay, experiment, ab test -->

**Official docs:** https://posthog.com/docs  |  **Verified:** 2026-04-14 via web search.

## Install + init (Node)

```bash
pnpm add posthog-node
```

```ts
import { PostHog } from "posthog-node";
const ph = new PostHog(
  process.env.POSTHOG_API_KEY!,     // project API key (safe to expose client-side too)
  {
    host: "https://us.i.posthog.com",   // or "https://eu.i.posthog.com" or self-hosted URL
    flushAt: 20,                         // batch size before flush
    flushInterval: 10000,                // ms
  }
);
```

## Capture events

```ts
ph.capture({
  distinctId: userId,
  event: "order_placed",
  properties: { total: 1999, items: 3, $insert_id: orderId /* dedup */ },
});
```

The project API key is POST-only and safe to include in client builds. Rate limits on `/i/v0/e` and `/flags` are generous (no hard limit for these public endpoints).

## User properties

```ts
ph.identify({ distinctId: userId, properties: { email, plan: "pro" } });
ph.alias({ distinctId: userId, alias: anonId });   // stitch anon → known
```

## Feature flags

### Server-side evaluation (fast, cached; requires personal API key)

```ts
const ph = new PostHog(API_KEY, { host, personalApiKey: PERSONAL_API_KEY });
await ph.reloadFeatureFlags();     // pulls flag definitions once; cached locally

const variant = await ph.getFeatureFlag("new-checkout", userId, {
  personProperties: { plan: "pro" },
  groupProperties: { company: { tier: "enterprise" } },
});
if (variant === "v2") renderNewCheckout();
```

Local evaluation avoids a network round-trip per flag check. Rate limit: 600/min on `/api/feature_flag/local_evaluation`.

### Remote evaluation (one API call per user per check)

```ts
const variant = await ph.getFeatureFlag("new-checkout", userId);
// Simpler, works without personal API key, but adds latency
```

### Client-side (JavaScript)

```ts
import posthog from "posthog-js";
posthog.init(API_KEY, { api_host: HOST });
posthog.onFeatureFlags(() => {
  if (posthog.isFeatureEnabled("new-checkout")) { /* ... */ }
});
```

## Session replay

Enabled per-project; client SDK records automatically when initialized. Configure masking for PII:

```ts
posthog.init(API_KEY, {
  session_recording: {
    maskAllInputs: true,                // mask <input> contents
    maskInputOptions: { password: true, email: false },
  },
});
```

## Cohorts / experiments

Experiments are feature flags with metrics attached. Define in the dashboard; `getFeatureFlag` returns the variant assigned, and the corresponding capture events are auto-tagged with `$feature/flag_name: variant_key`.

Include `$feature/feature_flag_name: variant_key` in event properties when using server-side SDKs to properly tag server events to experiments.

## Direct HTTP

```
POST https://us.i.posthog.com/i/v0/e
{
  "api_key": "phc_...",
  "event": "order_placed",
  "distinct_id": "u_42",
  "properties": { "total": 1999 },
  "timestamp": "2026-04-14T12:00:00Z"
}
```

## Self-hosted

Same API surface, different host. Docker Compose image at posthog/posthog for internal deployments.

## Common gotchas

- **`flushAt` + `flushInterval` matter in serverless**: Lambda dies before flush → events lost. Call `await ph.shutdown()` at the end of every handler.
- **Personal API key vs project key**: project key (`phc_...`) for ingestion; personal API key (`phx_...`) for local flag eval + admin ops.
- **Cohorts don't evaluate locally** unless you opt in (`personProperties` passed explicitly).

## Key reference URLs

- API overview: https://posthog.com/docs/api
- Node SDK: https://posthog.com/docs/libraries/node
- Feature flags: https://posthog.com/docs/feature-flags
- Feature flag API: https://posthog.com/docs/api/flags
- Session replay: https://posthog.com/docs/session-replay
