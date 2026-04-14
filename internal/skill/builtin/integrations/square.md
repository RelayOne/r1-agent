# square

> Square: payments, POS, online store, subscriptions. OAuth2 client-credentials or per-merchant OAuth. Production vs Sandbox have separate base URLs.

<!-- keywords: square, square payments, pos, square api, terminal -->

**Official docs:** https://developer.squareup.com/docs  |  **Verified:** 2026-04-14 (stable Payments API).

## Environments

- Sandbox: `https://connect.squareupsandbox.com/v2/`
- Production: `https://connect.squareup.com/v2/`
- Auth: `Authorization: Bearer <ACCESS_TOKEN>` (per-merchant for multi-tenant apps; static token for single-merchant builds).
- SDKs: `square` (Node), `squareup` (Python), Java, Ruby, PHP, .NET.

## Payment flow (Web Payments SDK → Create Payment)

### 1. Client: collect card with Web Payments SDK

```html
<script src="https://sandbox.web.squarecdn.com/v1/square.js"></script>
```

```js
const payments = Square.payments(APP_ID, LOCATION_ID);
const card = await payments.card();
await card.attach("#card-container");
document.getElementById("pay").onclick = async () => {
  const { token } = await card.tokenize();      // nonce for server to charge
  await fetch("/charge", { method: "POST", body: JSON.stringify({ token }) });
};
```

### 2. Server: create payment

```ts
import { SquareClient, SquareEnvironment } from "square";
const client = new SquareClient({
  token: process.env.SQUARE_ACCESS_TOKEN!,
  environment: SquareEnvironment.Sandbox,
});

const resp = await client.payments.create({
  sourceId: nonce,                 // token from client
  idempotencyKey: crypto.randomUUID(),
  amountMoney: { amount: 1999n, currency: "USD" },   // amount in MINOR units (cents)
  locationId: LOCATION_ID,
  referenceId: "order_42",
});
// resp.payment.status: "COMPLETED" on success
```

## Idempotency

Required on every mutating call. Generate a UUID per user action. Retry with same key is safe.

## Refund

```ts
await client.refunds.refundPayment({
  idempotencyKey: crypto.randomUUID(),
  paymentId,
  amountMoney: { amount: 500n, currency: "USD" },   // partial refund; omit to refund full
});
```

## Subscriptions

```ts
// 1. Create catalog item (once)
await client.catalog.object.upsert({ ... });

// 2. Create subscription plan
await client.subscriptions.create({
  idempotencyKey,
  locationId,
  planVariationId,
  customerId,
});
```

## Webhooks

Configure in Developer Dashboard → Webhooks. Events: `payment.created`, `payment.updated`, `refund.created`, `order.created`, `invoice.payment_made`, etc.

Verify signature:

```ts
import crypto from "crypto";
const sig = req.headers["x-square-hmacsha256-signature"];
const body = rawBody;
const url = notificationUrl;     // the full URL Square posted to
const expected = crypto.createHmac("sha256", SIG_KEY).update(url + body).digest("base64");
if (expected !== sig) return 401;
```

## Common gotchas

- **`amountMoney.amount` is BIGINT in minor units** — USD cents as BigInt, not a decimal float.
- **Sandbox test cards**: card nonce `cnon:card-nonce-ok` simulates approval; specific nonces simulate declines. See sandbox payments docs.
- **Location ID required** on most endpoints — merchants can have multiple locations.
- **OAuth tokens expire**: Square rotates; implement refresh token flow for multi-merchant apps.

## Key reference URLs

- Payments API: https://developer.squareup.com/reference/square/payments-api
- Web Payments SDK: https://developer.squareup.com/docs/web-payments/overview
- Webhooks: https://developer.squareup.com/docs/webhooks/overview
- Webhook signature: https://developer.squareup.com/docs/webhooks/step3validate
