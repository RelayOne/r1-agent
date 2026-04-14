# paddle

> Paddle (Merchant of Record for SaaS). Handles VAT/sales tax for you — you invoice one entity (Paddle), customers billed in their currency. Paddle Billing API (current) replaces the older Classic API.

<!-- keywords: paddle, paddle billing, merchant of record, saas billing, vat, subscriptions -->

**Official docs:** https://developer.paddle.com  |  **Verified:** 2026-04-14.

## Environments + auth

- Sandbox: `https://sandbox-api.paddle.com`
- Live: `https://api.paddle.com`
- Auth: `Authorization: Bearer <API_KEY>` (separate API keys per env)
- API version: `Paddle-Version: 1` header

## Core objects

- **Product**: the thing you sell.
- **Price**: a pricing tier for a product (one-time or recurring).
- **Customer**: buyer record (stores email, address for tax).
- **Transaction**: an invoice for one or more prices.
- **Subscription**: a recurring billing relationship.

## Checkout (overlay)

Use Paddle.js for the checkout widget:

```html
<script src="https://cdn.paddle.com/paddle/v2/paddle.js"></script>
```

```js
Paddle.Environment.set("sandbox");      // or "production"
Paddle.Initialize({ token: "pk_..." });
Paddle.Checkout.open({
  items: [{ priceId: "pri_abc", quantity: 1 }],
  customer: { email: "user@example.com" },
  successUrl: "https://app/success",
});
```

## Server-side transaction (recommended for SaaS)

```ts
// Create transaction
const resp = await fetch("https://api.paddle.com/transactions", {
  method: "POST",
  headers: { Authorization: `Bearer ${API_KEY}`, "Content-Type": "application/json", "Paddle-Version": "1" },
  body: JSON.stringify({
    items: [{ price_id: "pri_abc", quantity: 1 }],
    customer_id: "ctm_xyz",
    collection_mode: "manual",     // or "automatic"
  }),
});
// Returned checkout URL → redirect buyer
```

## Subscriptions

Subscriptions are created from a completed checkout. You manage them via:

```
GET /subscriptions/{id}
PATCH /subscriptions/{id} { "scheduled_change": { "action": "cancel", "effective_at": "next_billing_period" } }
POST /subscriptions/{id}/pause
POST /subscriptions/{id}/resume
```

## Webhooks (essential)

Events: `transaction.completed`, `subscription.created`, `subscription.updated`, `subscription.canceled`, `subscription.past_due`, `customer.updated`, etc.

Verify HMAC signature:

```ts
import crypto from "crypto";
const [ts, h1] = req.headers["paddle-signature"].split(";").reduce((acc, p) => {
  const [k, v] = p.split("="); return { ...acc, [k]: v };
}, {});
const signed = `${ts.split("=")[1]}:${rawBody}`;
const expected = crypto.createHmac("sha256", WEBHOOK_SECRET).update(signed).digest("hex");
// compare expected to h1 in constant time
```

## Merchant of Record implications

- Paddle owns the buyer relationship for tax/compliance. You invoice Paddle; Paddle invoices buyer.
- Paddle takes ~5% + 50¢ per transaction (vs ~3% for Stripe). Tradeoff: you skip 50+ VAT/sales-tax filings.
- For B2B SaaS selling globally, usually worth it. For high-volume low-margin, Stripe is cheaper.

## Common gotchas

- **Classic API (Paddle v1) is different** — has `vendor_id` + `vendor_auth_code`. The current "Paddle Billing" is the one you want for new builds.
- **Webhook signing secret lives at `notification_settings`** — separate per notification destination.
- **Currency presentation is auto-localized** — buyer sees their currency, you always receive in your payout currency.

## Key reference URLs

- API overview: https://developer.paddle.com/api-reference/overview
- Checkout: https://developer.paddle.com/build/checkout/build-overview
- Subscriptions: https://developer.paddle.com/build/subscriptions
- Webhooks signature verification: https://developer.paddle.com/webhooks/signature-verification
