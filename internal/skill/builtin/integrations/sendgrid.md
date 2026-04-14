# sendgrid

> SendGrid v3 transactional email. Use the `@sendgrid/mail` SDK; templates via Dynamic Templates; bounce/open tracking via Event Webhook.

<!-- keywords: sendgrid, email, transactional email, smtp, mail send, dynamic templates, inbound parse, event webhook -->

**Official docs:** https://www.twilio.com/docs/sendgrid  |  **Verified:** 2026-04-14 via web search.

## Base URL + auth

- Global REST base: `https://api.sendgrid.com`
- EU regional base: `https://api.eu.sendgrid.com`
- Auth: `Authorization: Bearer SG.<api-key>`
- SDKs: `@sendgrid/mail` (Node), `sendgrid` (Python), `sendgrid-ruby`, etc.

## Send mail

```js
import sgMail from "@sendgrid/mail";
sgMail.setApiKey(process.env.SENDGRID_API_KEY);

await sgMail.send({
  to: "user@example.com",
  from: { email: "noreply@yourapp.com", name: "Your App" },
  subject: "Welcome",
  text: "Plain text fallback",
  html: "<p>Rich HTML body</p>",
  categories: ["welcome"],    // for analytics filtering
  customArgs: { user_id: "u_42" },  // echoed back on webhook events
});
```

Direct POST equivalent: `POST https://api.sendgrid.com/v3/mail/send` with a `personalizations` / `from` / `subject` / `content` JSON body.

## Dynamic Templates (preferred over inline HTML)

Create the template in the dashboard; reference by ID:

```js
await sgMail.send({
  to: "user@example.com",
  from: "noreply@yourapp.com",
  templateId: "d-abc123...",
  dynamicTemplateData: { first_name: "Alex", verify_url: "https://..." },
});
```

Templates compile Handlebars-style `{{first_name}}` substitutions server-side. Don't construct email HTML in app code — it drifts from test/prod.

## Event Webhook (delivery + engagement)

Configure the webhook URL in Mail Settings → Event Webhook. SendGrid POSTs JSON arrays of events to your endpoint:

```json
[{
  "email":"user@example.com",
  "event":"delivered",
  "sg_message_id":"...",
  "timestamp":1713100000,
  "user_id":"u_42"
}]
```

Events: `processed`, `dropped`, `delivered`, `deferred`, `bounce`, `open`, `click`, `spamreport`, `unsubscribe`, `group_unsubscribe`, `group_resubscribe`.

**Verify the signature** (HMAC-SHA256 with your verification key) before trusting webhook payloads; docs link below. Track `bounce` + `spamreport` + `unsubscribe` to maintain list hygiene — hard-bounced or unsub'd addresses should not be retried.

## Sender identity

- Single Sender Verification: prove you own one From address. Fine for low volume.
- Domain Authentication (SPF/DKIM/DMARC): required for production; set up CNAME records the dashboard specifies. Deliverability is ~30-50% worse without it.

## Rate limits + errors

- 401 unauthorized → API key wrong or missing mail-send permission.
- 403 on sandbox mode accidentally enabled.
- 413 payload too large → split recipients.
- 429 rate limited → retry with exponential backoff.

## Key reference URLs

- Mail Send: https://www.twilio.com/docs/sendgrid/api-reference/mail-send/mail-send
- Authentication: https://www.twilio.com/docs/sendgrid/for-developers/sending-email/authentication
- Event Webhook: https://www.twilio.com/docs/sendgrid/for-developers/tracking-events/event
- Dynamic Templates: https://www.twilio.com/docs/sendgrid/ui/sending-email/how-to-send-an-email-with-dynamic-templates
