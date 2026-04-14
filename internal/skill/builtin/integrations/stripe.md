# stripe

> Stripe payments: Payment Intents, Checkout, subscriptions, webhooks. Use the server-side SDK; never handle raw card data client-side except via Stripe.js / Elements.

<!-- keywords: stripe, payment, billing, checkout, subscription, webhook, invoice, charge, refund, paymentintent, customer, saas billing -->

**Official docs:** https://docs.stripe.com  |  **Verified:** 2026-04-14 via web search.

## Base URL + auth

- REST base: `https://api.stripe.com/v1/`
- Auth: `Authorization: Bearer sk_live_...` (secret key) server-side. **Never ship a secret key in frontend code.** Frontend uses publishable key `pk_live_...` via Stripe.js.
- SDKs: `stripe` (Node), `stripe` (Python), `stripe-go`, `stripe-ruby`, etc. Prefer official SDKs — they handle retries, idempotency, and signature verification.

## Core flow: one-time payment (Payment Intents)

Recommended for all custom checkout flows. Create one PaymentIntent per order.

```js
// Server
const pi = await stripe.paymentIntents.create({
  amount: 1999,         // cents
  currency: 'usd',
  automatic_payment_methods: { enabled: true },
  metadata: { order_id: '...' },
});
return { clientSecret: pi.client_secret };
```

```js
// Client (Stripe.js)
const { error } = await stripe.confirmPayment({
  elements,
  clientSecret,
  confirmParams: { return_url: 'https://app/return' },
});
```

PaymentIntent lifecycle: `requires_payment_method` → `requires_confirmation` → `processing` → `succeeded` (or `canceled`, `requires_action`).

## Stripe Checkout (hosted, faster to ship)

```js
const session = await stripe.checkout.sessions.create({
  mode: 'payment',
  line_items: [{ price: 'price_123', quantity: 1 }],
  success_url: 'https://app/success?session_id={CHECKOUT_SESSION_ID}',
  cancel_url: 'https://app/cancel',
});
// Redirect browser to session.url
```

Use `mode: 'subscription'` for recurring billing with a Price in recurring interval.

## Webhooks (authoritative state, required)

Stripe's webhook events are the source of truth for payment status — do NOT rely on the client confirmPayment return. Subscribe to at minimum:

- `payment_intent.succeeded` — order paid, fulfill
- `payment_intent.payment_failed` — notify customer
- `checkout.session.completed` — checkout finalized
- `invoice.paid` / `invoice.payment_failed` — subscription billing
- `customer.subscription.updated` / `.deleted` — subscription lifecycle

Signature verification is mandatory:

```js
const sig = req.headers['stripe-signature'];
const event = stripe.webhooks.constructEvent(rawBody, sig, STRIPE_WEBHOOK_SECRET);
```

`rawBody` must be the unparsed request body. Express: use `express.raw({ type: 'application/json' })` on the webhook route only. The signed timestamp mitigates replay attacks (rejected if older than `tolerance` seconds, default 300).

## Idempotency

Every mutating API call should carry an `Idempotency-Key` header (UUID per user action). Stripe returns the cached response if the same key retries within 24 hours. Prevents double-charges on network retries.

## Common gotchas

- **Test mode vs live mode keys** — the gate sees `sk_test_...` and `sk_live_...` as different ecosystems. Webhook secrets are separate per mode.
- **Apple Pay / Google Pay domain verification** — required before those methods show in Payment Element.
- **SCA (Strong Customer Authentication)** — `requires_action` status means 3DS challenge; frontend must handle via `confirmPayment` flow automatically.
- **Subscription proration** — `proration_behavior: 'create_prorations'` for mid-cycle plan changes.

## Key reference URLs

- Payment Intents: https://docs.stripe.com/api/payment_intents
- Webhooks: https://docs.stripe.com/webhooks
- Signature verification: https://docs.stripe.com/webhooks/signature
- Subscriptions: https://docs.stripe.com/billing/subscriptions/overview
- Idempotency: https://docs.stripe.com/api/idempotent_requests
