# twilio

> Twilio Programmable Messaging (SMS/MMS/WhatsApp) + Verify (OTP/2FA). Use the official helper libraries; Verify is preferred over DIY OTP.

<!-- keywords: twilio, sms, mms, whatsapp, otp, 2fa, verify, messaging, phone, text message, verification -->

**Official docs:** https://www.twilio.com/docs  |  **Verified:** 2026-04-14 via web search.

## Base URL + auth

- REST base: `https://api.twilio.com/2010-04-01/`
- Auth: HTTP Basic — `AccountSid:AuthToken` (server-side only).
- Verify service base: `https://verify.twilio.com/v2/`
- SDKs: `twilio` (Node), `twilio` (Python), `twilio-ruby`, etc.

## Send SMS (Programmable Messaging)

```js
const twilio = require("twilio")(accountSid, authToken);
await twilio.messages.create({
  from: "+15017122661",          // Twilio number
  to: "+15558675310",            // E.164 format required
  body: "Your code is 123456",
});
```

Support for `MessagingServiceSid` (sender pool) preferred over hard-coded `from` for deliverability. Returns a Message resource with `sid` and `status` (queued → sent → delivered).

## Delivery status webhook

Set `statusCallback: "https://app/webhooks/twilio"` on create, or configure per-number in console. Twilio POSTs to it with:

```
MessageSid=SM...&MessageStatus=delivered&From=...&To=...
```

Validate with the official helper:

```js
const isValid = twilio.validateRequest(
  authToken,
  req.headers["x-twilio-signature"],
  fullUrl,
  req.body
);
```

The signature covers the raw URL + sorted POST body params. Reject on mismatch.

## Verify API (prefer over DIY OTP)

Fully managed OTP with retry, fraud rules, channel fallback.

```js
// Step 1: trigger a verification
await twilio.verify.v2.services(VERIFY_SERVICE_SID)
  .verifications.create({ to: "+15558675310", channel: "sms" });

// Step 2: check the code the user entered
const check = await twilio.verify.v2.services(VERIFY_SERVICE_SID)
  .verificationChecks.create({ to: "+15558675310", code: "123456" });
// check.status === "approved" on success, "pending" or "canceled" otherwise
```

Channels: `sms`, `call`, `email`, `whatsapp`, `sna` (Silent Network Auth). Verify tracks failed attempts per destination — stops retry loops automatically.

## WhatsApp

Same `messages.create` with `from: "whatsapp:+14155552671"` and `to: "whatsapp:+15558675310"`. Requires a WhatsApp-approved template for the initial outbound message.

## Rate limits + errors

- 429 on rate exceed — respect `Retry-After`.
- 21211 "Invalid To phone number" → reject at client; never retry.
- 21610 "Receiver unsubscribed (STOP)" → permanent; remove from list.

## Cost gotchas

- Toll-free and short-code numbers are more expensive per message.
- International messages vary by country; always log `price` + `price_unit` on the Message resource.
- A2P 10DLC registration required in US — do it before launch.

## Key reference URLs

- Messaging: https://www.twilio.com/docs/messaging/api
- Verify: https://www.twilio.com/docs/verify
- Webhook validation: https://www.twilio.com/docs/usage/webhooks/webhooks-security
- Phone-number formats (E.164): https://www.twilio.com/docs/glossary/what-e164
