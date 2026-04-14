# postmark

> Postmark: transactional email known for fast delivery + clean bounce handling. Separate "Transactional" and "Broadcast" streams (prevents your app alerts from being throttled by newsletter complaints). Template API with Mustache-style vars.

<!-- keywords: postmark, email, transactional email, postmark api, postmark template -->

**Official docs:** https://postmarkapp.com/developer  |  **Verified:** 2026-04-14 via web search.

## Base URL + auth

- REST base: `https://api.postmarkapp.com/`
- Auth: `X-Postmark-Server-Token: <token>` header per server (each server has its own token).
- For account-level operations: `X-Postmark-Account-Token`.

## Send email

```
POST /email
X-Postmark-Server-Token: ...
Content-Type: application/json

{
  "From": "noreply@yourdomain.com",
  "To": "user@example.com",
  "Subject": "Welcome",
  "HtmlBody": "<p>...</p>",
  "TextBody": "...",
  "MessageStream": "outbound",          // or a custom broadcast stream
  "Tag": "welcome",
  "Metadata": { "user_id": "42" },      // echoed on webhook events
  "TrackOpens": true,
  "TrackLinks": "HtmlOnly"               // "None" | "HtmlOnly" | "TextOnly" | "HtmlAndText"
}
```

Node SDK:

```ts
import { ServerClient } from "postmark";
const client = new ServerClient(process.env.POSTMARK_TOKEN!);
await client.sendEmail({ From, To, Subject, HtmlBody, TextBody });
```

## Send with template

```
POST /email/withTemplate
{
  "From": "...",
  "To": "...",
  "TemplateId": 12345,                 // or TemplateAlias: "welcome-email"
  "TemplateModel": { "first_name": "Alex", "verify_url": "https://..." },
  "MessageStream": "outbound"
}
```

Batch templates via `POST /email/batchWithTemplates`.

## Bounces

Bounce API: `GET /bounces` with filters (`type`, `inactive`, `from`, `tag`). `GET /bounces/{id}` for detail. `PUT /bounces/{id}/activate` to re-enable a soft-bounced address after fixing.

Bounce types: `HardBounce`, `SoftBounce`, `Transient`, `Unsubscribe`, `SubscriptionChange`, `AutoResponder`, `Blocked`, `SpamComplaint`, `ManuallyDeactivated`, `Unknown`, `Undeliverable`.

## Webhooks

Subscribe in server settings. Events: `Delivery`, `Bounce`, `SpamComplaint`, `Open`, `Click`, `SubscriptionChange`, `Inbound`.

```json
{
  "RecordType": "Bounce",
  "Type": "HardBounce",
  "MessageID": "...",
  "Email": "user@example.com",
  "Details": "...",
  "Metadata": { "user_id": "42" }
}
```

Postmark does NOT sign webhooks by default. Best practice: require HTTPS + a shared secret in URL query string (`?secret=...`) and rotate.

## Testing

Sandbox bounce inbox: send to `hardbounce@bounce-testing.postmarkapp.com` to generate a fake hard bounce for your test flows. Similar addresses for soft/spam/etc.

## Common gotchas

- **Stream mismatch**: mixing transactional and broadcast streams hurts deliverability. Keep them separate.
- **429 rate limits**: Postmark's hard limit is 300 requests/second per server; `Retry-After` header present.
- **From address must be a verified sender** on the server — DKIM + Return-Path DNS setup at dashboard.
- **Template aliases are case-sensitive**; missing template returns 422.

## Key reference URLs

- Email API: https://postmarkapp.com/developer/api/email-api
- Templates: https://postmarkapp.com/developer/api/templates-api
- Bounces: https://postmarkapp.com/developer/api/bounce-api
- Webhooks: https://postmarkapp.com/developer/webhooks/bounce-webhook
- Sandbox: https://postmarkapp.com/developer/user-guide/sandbox-mode
