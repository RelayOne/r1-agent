# sentry

> Sentry error monitoring + performance tracing. Initialize once via DSN; Next.js has a first-class SDK (`@sentry/nextjs`) that auto-instruments React components, server actions, API routes, and edge middleware.

<!-- keywords: sentry, error monitoring, error tracking, performance tracing, dsn, sourcemaps, sentry-nextjs, sentry-react, observability -->

**Official docs:** https://docs.sentry.io  |  **Verified:** 2026-04-14 via web search.

## Install (Next.js)

```bash
pnpm add @sentry/nextjs
npx @sentry/wizard@latest -i nextjs
```

The wizard creates `sentry.client.config.ts`, `sentry.server.config.ts`, `sentry.edge.config.ts` plus `next.config.js` source-map upload plugin wiring.

```ts
// sentry.client.config.ts
import * as Sentry from "@sentry/nextjs";

Sentry.init({
  dsn: process.env.NEXT_PUBLIC_SENTRY_DSN,
  tracesSampleRate: 0.1,        // 10% of transactions, tune for scale
  replaysSessionSampleRate: 0.01,
  replaysOnErrorSampleRate: 1.0,
  integrations: [
    Sentry.replayIntegration({ maskAllText: true, blockAllMedia: true }),
  ],
  environment: process.env.NEXT_PUBLIC_ENV,
  release: process.env.NEXT_PUBLIC_RELEASE,
});
```

`NEXT_PUBLIC_SENTRY_DSN` is safe to expose (it only allows write, not read). Server configs use the same DSN without the NEXT_PUBLIC prefix.

## Manual capture

```ts
try { risky(); }
catch (err) { Sentry.captureException(err, { tags: { feature: "checkout" }, extra: { orderId } }); }

Sentry.captureMessage("Unexpected empty cart on checkout");

Sentry.addBreadcrumb({
  category: "auth",
  message: "user signed in",
  data: { userId },
});
```

## Performance tracing

Next.js routes and API calls are auto-instrumented when the SDK is initialized. For custom spans:

```ts
await Sentry.startSpan({ name: "process-order", op: "task" }, async (span) => {
  span.setAttribute("order.id", id);
  await work();
});
```

Backend-to-frontend distributed tracing works out of the box when both ends run `@sentry/*` with the same release.

## User context

```ts
Sentry.setUser({ id: userId, email: userEmail });
// After logout:
Sentry.setUser(null);
```

Never store secrets in `setUser` — the object is attached to every event.

## Source maps (stack traces without this = useless)

`next.config.js` with `withSentryConfig` uploads sourcemaps at build time. Required env: `SENTRY_AUTH_TOKEN`, `SENTRY_ORG`, `SENTRY_PROJECT`. Without these, production stacks show minified code.

## Session Replay

Replays are tied to events (`replaysOnErrorSampleRate: 1.0` captures a replay for every error). Huge diagnostic lever — worth the bandwidth cost on production unless you handle PII that can't be scrubbed.

## Scrubbing PII

Built-in scrubbers handle common PII (credit cards, emails in some paths). For explicit control:

```ts
beforeSend(event, hint) {
  if (event.request?.headers?.authorization) delete event.request.headers.authorization;
  return event;
},
```

Never log raw auth headers, tokens, or request bodies containing passwords. Sentry's default scrubbers are not sufficient — add explicit deletion of known-sensitive keys.

## Common gotchas

- **Missing release**: events won't group correctly; source maps won't match.
- **tracesSampleRate too high** on high-traffic apps: Sentry bill explodes. Start at 0.05-0.1, observe, adjust.
- **Not filtering 4xx errors**: 404s from bots flood your issue feed. Filter in `beforeSend`.
- **Uncaught errors in server actions** on older SDKs: upgrade `@sentry/nextjs` to latest.

## Key reference URLs

- Next.js guide: https://docs.sentry.io/platforms/javascript/guides/nextjs/
- Manual setup: https://docs.sentry.io/platforms/javascript/guides/nextjs/manual-setup/
- Tracing: https://docs.sentry.io/platforms/javascript/guides/nextjs/tracing/
- Session Replay: https://docs.sentry.io/platforms/javascript/guides/nextjs/session-replay/
- DSN: https://docs.sentry.io/concepts/key-terms/dsn-explainer/
