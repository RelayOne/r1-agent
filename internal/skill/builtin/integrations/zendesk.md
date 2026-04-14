# zendesk

> Zendesk Support: the OG help-desk. Tickets, agents, macros, help center. REST API v2; Sunshine Conversations for omnichannel messaging.

<!-- keywords: zendesk, zendesk support, ticketing, help desk, customer service, sunshine conversations -->

**Official docs:** https://developer.zendesk.com  |  **Verified:** 2026-04-14.

## Base URL + auth

- `https://{subdomain}.zendesk.com/api/v2/`
- Auth: email + API token → Basic auth: `email/token:<TOKEN>` → base64. Or OAuth2 for apps.

```
Authorization: Basic <base64(user@example.com/token:API_TOKEN)>
```

## Tickets (the main resource)

```ts
// Create
await fetch(`https://${SUB}.zendesk.com/api/v2/tickets.json`, {
  method: "POST",
  headers: { Authorization, "Content-Type": "application/json" },
  body: JSON.stringify({
    ticket: {
      subject: "Login broken",
      comment: { body: "Getting 500s on /login", public: false },
      requester: { name: "Alex", email: "alex@example.com" },
      priority: "urgent",
      tags: ["prod", "auth"],
    },
  }),
});
```

Common endpoints:

```
GET    /tickets/{id}
PUT    /tickets/{id}                     { ticket: { status: "solved" } }
POST   /tickets/{id}/comments            via PUT /tickets/{id} with comment
GET    /search.json?query=type:ticket status:open
POST   /tickets/create_many              bulk (up to 100)
```

## Users

```
POST /users              { user: { name, email, role: "end-user"|"agent"|"admin" } }
PUT  /users/create_or_update
GET  /users/search?query=email:a@b.com
```

## Search API (Lucene-style)

```
/search.json?query=type:ticket status:open priority:urgent created>2025-01-01
```

Powerful; paginates via cursor-based `page[after]`/`page[size]`.

## Webhooks + Triggers

**Webhooks** (newer): configure a webhook endpoint, then use a **Trigger** to fire it based on conditions (e.g., "ticket created AND priority=urgent").

```
POST /webhooks
{ webhook: { name, endpoint: "https://...", http_method: "POST",
  request_format: "json", status: "active",
  authentication: { type: "bearer_token", data: { token: "..." } } } }
```

Trigger references the webhook via the `notify_webhook` action.

Verify signature: HMAC-SHA256 of raw body, header `X-Zendesk-Webhook-Signature`, compare base64:

```ts
const expected = crypto.createHmac("sha256", SIGNING_SECRET).update(timestamp + rawBody).digest("base64");
```

## Sunshine Conversations (omnichannel messaging)

Separate API for unified SMS / WhatsApp / Facebook / web messaging. Different base: `https://api.smooch.io/v2/apps/{appId}/`.

## App framework (ZAF)

Build side-panel apps that run inside the Zendesk agent interface. Built in JS using `zendesk_app_framework` SDK. Publish via Marketplace or private install.

## Common gotchas

- **Rate limits**: 700 req/min on "Enterprise" plan for API v2; lower on smaller plans. `Retry-After` on 429. Use bulk endpoints (`create_many`, `update_many`) for large sync jobs.
- **API token vs password**: `/token` suffix on email tells Zendesk to use API token; without it, it tries password auth and fails.
- **Triggers + Webhooks vs legacy HTTP targets**: HTTP Targets are deprecated; migrate to Webhooks.
- **Search has 1000-result ceiling** — even with pagination. For full exports, use incremental export API (`/incremental/tickets`).
- **Custom fields are integer IDs**, not names — fetch from `/ticket_fields` once + cache.

## Key reference URLs

- REST API v2: https://developer.zendesk.com/api-reference/ticketing/introduction/
- Webhooks: https://developer.zendesk.com/documentation/webhooks/
- Incremental export: https://developer.zendesk.com/api-reference/ticketing/ticket-management/incremental_exports/
- Triggers: https://support.zendesk.com/hc/en-us/articles/4408822278810
