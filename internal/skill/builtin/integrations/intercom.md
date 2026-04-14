# intercom

> Intercom: customer messaging + support. Messenger widget, conversations, tickets, help center, product tours. REST API for user/conversation sync; Messenger JS SDK for in-app chat.

<!-- keywords: intercom, customer support, live chat, messenger, help desk -->

**Official docs:** https://developers.intercom.com  |  **Verified:** 2026-04-14 (API v2.11).

## Auth

- **Access Token** (apps + personal): `Authorization: Bearer <token>`
- OAuth2 for public apps in the App Store.
- Base URL: `https://api.intercom.io/`
- Version: `Intercom-Version: 2.11` header (pin a version — API evolves).

## Messenger (JS SDK)

```html
<script>
  window.intercomSettings = {
    app_id: "APP_ID",
    user_id: "42",
    email: "user@example.com",
    name: "Alex",
    created_at: 1700000000,
    user_hash: "<HMAC-SHA256>",   // identity verification
  };
</script>
<script>/* standard loader snippet */</script>
```

### Identity verification (MUST)

Without `user_hash`, anyone with your App ID can impersonate users. Compute server-side:

```ts
const hash = crypto.createHmac("sha256", IDENTITY_SECRET).update(userId).digest("hex");
```

Enable "Identity Verification" in Intercom settings to enforce.

## REST: create/update contact (user or lead)

```ts
// Upsert by external_id
await fetch("https://api.intercom.io/contacts", {
  method: "POST",
  headers: { Authorization: `Bearer ${TOKEN}`, "Content-Type": "application/json", "Intercom-Version": "2.11" },
  body: JSON.stringify({
    role: "user",
    external_id: "42",
    email: "user@example.com",
    name: "Alex",
    custom_attributes: { plan: "pro" },
  }),
});
```

Search contacts:

```
POST /contacts/search    { query: { field: "email", operator: "=", value: "a@b.com" } }
```

## Conversations

```
GET  /conversations/{id}
POST /conversations/{id}/reply     { body: "Thanks", message_type: "comment", type: "admin", admin_id }
POST /conversations/{id}/parts     { body, message_type: "note" }       # internal note
POST /conversations/{id}/close
```

## Tickets (support ticket API)

```
POST /tickets         { ticket_type_id, contacts, ticket_attributes: {...} }
PUT  /tickets/{id}    { state: "submitted"|"in_progress"|"resolved" }
```

## Webhooks

App → Developer Hub → Webhooks. Topics: `conversation.user.created`, `conversation.admin.replied`, `contact.created`, `ticket.state.updated`, etc.

Verify HMAC-SHA1 of raw body with your app's client secret:

```ts
const sig = req.headers["x-hub-signature"].replace("sha1=", "");
const expected = crypto.createHmac("sha1", CLIENT_SECRET).update(rawBody).digest("hex");
```

## Custom attributes + events

Custom attributes live on Contact / Company. Track events:

```
POST /events   { event_name: "order_placed", created_at, user_id, metadata: { total: 99 } }
```

Events show in user profile timeline + drive campaign triggers.

## Common gotchas

- **Identity verification**: enable it. Without, anyone can open chat as any logged-in user of your app.
- **Rate limits**: 10,000 req/min default; per-endpoint limits tighter. Watch `X-RateLimit-*` headers.
- **API versioning via header** — without `Intercom-Version` you're auto-moved to latest stable, which can break your code.
- **`external_id` vs `id`**: `id` is Intercom's; `external_id` is your system's. Prefer `external_id` for idempotent upserts.
- **Archived contacts don't show in Messenger** — archive instead of delete when deactivating users, so history stays.

## Key reference URLs

- REST API: https://developers.intercom.com/docs/references/rest-api/
- Identity verification: https://developers.intercom.com/docs/installing-intercom/web/identity-verification/
- Webhooks: https://developers.intercom.com/docs/references/webhooks/
- Versioning: https://developers.intercom.com/docs/build-an-integration/learn-more/rest-apis/api-versioning/
