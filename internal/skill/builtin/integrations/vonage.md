# vonage

> Vonage (formerly Nexmo): SMS, voice, verify (2FA), video, WhatsApp Business API. API key + secret for REST; JWT for Application API (voice/video).

<!-- keywords: vonage, nexmo, sms, voice, verify, 2fa, whatsapp business -->

**Official docs:** https://developer.vonage.com  |  **Verified:** 2026-04-14.

## Auth

- **Messages / SMS / Verify**: API key + secret (basic auth or query params). Dashboard → Getting Started.
- **Application API** (voice, video, newer Messages API): JWT signed with private key. `Authorization: Bearer <JWT>`.
- SDKs: `@vonage/server-sdk` (Node), `vonage` (Python), Java, Ruby, PHP, .NET.

## Send SMS (REST)

```ts
await fetch("https://rest.nexmo.com/sms/json", {
  method: "POST",
  headers: { "Content-Type": "application/x-www-form-urlencoded" },
  body: new URLSearchParams({
    api_key: API_KEY,
    api_secret: API_SECRET,
    from: "MyApp",          // alphanumeric sender id (country-dependent) or E.164 number
    to: "14155551234",
    text: "Your code is 123456",
  }),
});
```

## Send SMS via Messages API (unified SMS/WhatsApp/MMS)

```ts
import { Vonage } from "@vonage/server-sdk";
const vonage = new Vonage({ apiKey, apiSecret, applicationId, privateKey });
await vonage.messages.send({
  message_type: "text",
  channel: "sms",           // or "whatsapp", "mms", "viber_service"
  to: "14155551234",
  from: "MyApp",
  text: "Hello",
});
```

## Verify API (2FA)

```ts
// 1. Request — sends OTP via SMS/voice
const req = await fetch("https://api.nexmo.com/v2/verify", {
  method: "POST",
  headers: { Authorization: `Basic ${Buffer.from(API_KEY+":"+API_SECRET).toString("base64")}`, "Content-Type": "application/json" },
  body: JSON.stringify({
    brand: "MyApp",
    workflow: [{ channel: "sms", to: "14155551234" }],
  }),
});
// returns { request_id }

// 2. Check
await fetch(`https://api.nexmo.com/v2/verify/${requestId}`, {
  method: "POST",
  body: JSON.stringify({ code: "1234" }),
});
```

Verify v2 replaces v1. v2 supports fallback workflows (try SMS → voice → email).

## Voice calls (Application API, JWT)

```ts
await vonage.voice.createOutboundCall({
  to: [{ type: "phone", number: "14155551234" }],
  from: { type: "phone", number: VONAGE_NUMBER },
  ncco: [{ action: "talk", text: "Hello from Vonage" }],
});
```

NCCO = Nexmo Call Control Object, JSON describing call flow.

## Webhooks (inbound SMS, delivery receipts, voice events)

Configured per app/number in Dashboard. Vonage POSTs JSON to your URL.

Verify signature (Messages API v2): HMAC-SHA256 with `VONAGE_API_SIGNATURE_SECRET`.

```ts
const expected = crypto.createHmac("sha256", SIG_SECRET).update(rawBody).digest("hex");
// compare to Authorization bearer JWT payload's `sig` claim
```

## Common gotchas

- **Sender ID rules vary by country** — US/Canada require long codes or toll-free; some EU countries accept alphanumeric.
- **Verify v1 is deprecated** — use `/v2/verify`. Different request shape.
- **Two Messages APIs coexist**: old (`rest.nexmo.com/sms/json`, basic auth) and new (`api.nexmo.com/v1/messages`, JWT, unified multi-channel). New is recommended.
- **JWT must include `application_id` and short TTL** (≤15 min). Use SDK to avoid signing mistakes.

## Key reference URLs

- Messages API: https://developer.vonage.com/en/messages/overview
- Verify v2: https://developer.vonage.com/en/verify/overview
- Voice API: https://developer.vonage.com/en/voice/voice-api/overview
- Webhooks + signing: https://developer.vonage.com/en/getting-started/concepts/webhooks
