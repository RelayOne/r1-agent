# mailgun

> Mailgun HTTP API for transactional email. HTTP Basic auth with `api` username + API key. Regional endpoints (US/EU). Webhooks for bounces/opens/clicks with HMAC signature verification.

<!-- keywords: mailgun, email, transactional email, mailgun api, mailgun webhook -->

**Official docs:** https://documentation.mailgun.com  |  **Verified:** 2026-04-14 via web search.

## Base URL + auth

- US: `https://api.mailgun.net/v3/`
- EU: `https://api.eu.mailgun.net/v3/`
- Auth: HTTP Basic with username `api` and password `<YOUR_API_KEY>`.

## Send email

```
POST /v3/{DOMAIN}/messages
Authorization: Basic <base64("api:KEY")>
Content-Type: application/x-www-form-urlencoded

from=Excited User <mailgun@YOUR_DOMAIN>
to=user@example.com
subject=Hello
text=Testing
html=<p>Testing</p>
o:tracking=yes
o:tag=welcome
v:user_id=42                 # custom var → echoed on webhooks
```

Node SDK (`mailgun.js`):

```ts
import formData from "form-data";
import Mailgun from "mailgun.js";
const mg = new Mailgun(formData).client({ username: "api", key: process.env.MAILGUN_API_KEY! });
await mg.messages.create(DOMAIN, {
  from: "noreply@yourdomain.com",
  to: ["user@example.com"],
  subject: "Welcome",
  text: "...",
  html: "<p>...</p>",
  "v:user_id": "42",
});
```

## Templates

Store Handlebars templates in dashboard; send with `template: "welcome"` + `h:X-Mailgun-Variables: {"name":"Alex"}` header (stringified JSON).

## Webhooks (events)

Configure URLs in dashboard → Webhooks. Events: `delivered`, `opened`, `clicked`, `unsubscribed`, `complained`, `permanent_fail`, `temporary_fail`.

Verify signature (HMAC-SHA256):

```ts
import crypto from "crypto";
const sig = req.body.signature;              // { token, timestamp, signature }
const expected = crypto
  .createHmac("sha256", MAILGUN_WEBHOOK_SIGNING_KEY)
  .update(sig.timestamp + sig.token)
  .digest("hex");
if (expected !== sig.signature) return 401;
```

Up to 3 URLs per event type. Domain-level OR account-level configuration.

## Common gotchas

- **Regional mismatch**: EU domain must use `api.eu.mailgun.net`; wrong base returns 401 with misleading auth error.
- **Sandbox domain allows only whitelisted recipients** — verify real sending domain before launch.
- **DKIM/SPF required for deliverability** — set DNS records the dashboard specifies.
- **Webhook signing key differs from API key** — separate secret.

## Key reference URLs

- Send email: https://documentation.mailgun.com/docs/mailgun/api-reference/send/mailgun/messages
- Webhooks: https://documentation.mailgun.com/docs/mailgun/user-manual/webhooks/webhooks
- Tracking: https://mailgun-docs.redoc.ly/docs/mailgun/user-manual/tracking-messages/
