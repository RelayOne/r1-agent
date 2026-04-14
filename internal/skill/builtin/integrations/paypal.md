# paypal

> PayPal Orders v2 + Subscriptions. OAuth2 client-credentials for server auth. Sandbox vs Live have separate credentials. Webhooks with HMAC-SHA256 + CERT verification.

<!-- keywords: paypal, payment, checkout, paypal subscription, braintree, venmo -->

**Official docs:** https://developer.paypal.com/docs/api  |  **Verified:** 2026-04-14 (Orders v2 stable since 2020).

## Environments + auth

- Sandbox: `https://api-m.sandbox.paypal.com`
- Live: `https://api-m.paypal.com`
- Client ID + secret per environment (never mix).

Get access token:

```
POST /v1/oauth2/token
Authorization: Basic <base64(CLIENT_ID:SECRET)>
Content-Type: application/x-www-form-urlencoded

grant_type=client_credentials
```

Response: `{access_token, expires_in}` (usually 9 hours). Cache + refresh before expiry.

## Create order (Orders v2)

```
POST /v2/checkout/orders
Authorization: Bearer <access_token>

{
  "intent": "CAPTURE",
  "purchase_units": [{
    "amount": { "currency_code": "USD", "value": "19.99" },
    "reference_id": "order_42"
  }],
  "application_context": {
    "return_url": "https://app/success",
    "cancel_url": "https://app/cancel"
  }
}
```

Response includes `links[]` — the `approve` URL is where you redirect the buyer.

## Capture order (after buyer approves)

```
POST /v2/checkout/orders/{order_id}/capture
Authorization: Bearer <access_token>
```

Response: `{ status: "COMPLETED", purchase_units[0].payments.captures[0].id }`. That capture ID is what you track internally.

## Subscriptions

```
// 1. Create plan (once, via dashboard or API)
POST /v1/billing/plans

// 2. Create subscription
POST /v1/billing/subscriptions
{ "plan_id": "P-...", "subscriber": { "email_address": "..." }, "application_context": {...} }

// 3. Redirect to approve URL, then webhook tells you when active
```

## Webhooks

Subscribe at `/v1/notifications/webhooks`. Events: `CHECKOUT.ORDER.APPROVED`, `PAYMENT.CAPTURE.COMPLETED`, `BILLING.SUBSCRIPTION.ACTIVATED`, etc.

Verify signature:

```
POST /v1/notifications/verify-webhook-signature
{
  "auth_algo": "<from Paypal-Auth-Algo header>",
  "cert_url": "<from Paypal-Cert-Url>",
  "transmission_id": "<from Paypal-Transmission-Id>",
  "transmission_sig": "<from Paypal-Transmission-Sig>",
  "transmission_time": "<from Paypal-Transmission-Time>",
  "webhook_id": "<your webhook ID>",
  "webhook_event": <raw body>
}
// Response verification_status: "SUCCESS" or "FAILURE"
```

Do NOT skip verification — PayPal's endpoint is the canonical way.

## Common gotchas

- **Sandbox accounts**: generate test buyer + seller accounts in developer dashboard; can't use real accounts in sandbox.
- **Currency rules**: amount.value must match currency precision (USD 2 decimals, JPY 0 decimals). Invalid precision → 400.
- **Refund via capture ID**, not order ID: `POST /v2/payments/captures/{capture_id}/refund`.
- **Braintree vs PayPal REST**: Braintree (owned by PayPal) is a separate SDK/product; don't confuse.

## Key reference URLs

- Orders v2: https://developer.paypal.com/docs/api/orders/v2/
- Subscriptions: https://developer.paypal.com/docs/api/subscriptions/v1/
- Webhooks: https://developer.paypal.com/api/rest/webhooks/
- Verify signature: https://developer.paypal.com/api/rest/webhooks/rest/#link-verifywebhooksignature
