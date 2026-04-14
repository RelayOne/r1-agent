# lemon-squeezy

> Lemon Squeezy: Merchant of Record for digital goods + SaaS. Simpler than Paddle for indie devs; strong affiliate tooling. JSON:API-style REST.

<!-- keywords: lemon squeezy, lemonsqueezy, merchant of record, saas billing, indie payments -->

**Official docs:** https://docs.lemonsqueezy.com  |  **Verified:** 2026-04-14.

## Base URL + auth

- REST base: `https://api.lemonsqueezy.com/v1/`
- Auth: `Authorization: Bearer <API_KEY>`
- Accept: `application/vnd.api+json` (JSON:API convention)

## Core objects

- **Store**: your LS account.
- **Product** → **Variant** → **Price**: product has variants (e.g. Monthly, Annual), variants have prices.
- **Customer**: buyer.
- **Order**: one-time purchase.
- **Subscription**: recurring billing.
- **Checkout**: a hosted URL for one-time or subscription purchase.

## Create checkout (hosted)

```ts
const resp = await fetch("https://api.lemonsqueezy.com/v1/checkouts", {
  method: "POST",
  headers: {
    Authorization: `Bearer ${API_KEY}`,
    "Content-Type": "application/vnd.api+json",
    Accept: "application/vnd.api+json",
  },
  body: JSON.stringify({
    data: {
      type: "checkouts",
      attributes: {
        checkout_data: {
          email: "user@example.com",
          custom: { user_id: "42" },   // echoed on webhook
        },
        product_options: {
          redirect_url: "https://app/success",
          receipt_link_url: "https://app/receipt",
        },
      },
      relationships: {
        store: { data: { type: "stores", id: STORE_ID } },
        variant: { data: { type: "variants", id: VARIANT_ID } },
      },
    },
  }),
});
const { data } = await resp.json();
// data.attributes.url → redirect buyer here
```

## List / fetch subscriptions

```
GET /v1/subscriptions?filter[store_id]=123
GET /v1/subscriptions/{id}
```

## Cancel / pause / update subscription

```
DELETE /v1/subscriptions/{id}              // cancel at period end
PATCH /v1/subscriptions/{id}               // change variant (plan swap)
POST /v1/subscriptions/{id}/resume
```

## Webhooks

Events: `order_created`, `subscription_created`, `subscription_updated`, `subscription_cancelled`, `subscription_payment_success`, `subscription_payment_failed`, `license_key_created`, etc.

Verify HMAC-SHA256:

```ts
import crypto from "crypto";
const sig = req.headers["x-signature"];
const expected = crypto.createHmac("sha256", WEBHOOK_SECRET).update(rawBody).digest("hex");
if (!crypto.timingSafeEqual(Buffer.from(sig), Buffer.from(expected))) return 401;
```

Always verify — anyone can POST to your webhook endpoint.

## License keys (digital goods)

```
POST /v1/license-keys/activate?license_key=KEY&instance_name=name
POST /v1/license-keys/deactivate?license_key=KEY&instance_id=ID
GET  /v1/license-keys/validate?license_key=KEY
```

For distributing offline software that needs activation.

## Common gotchas

- **JSON:API format** is verbose; stick to the SDK (`@lemonsqueezy/lemonsqueezy.js`) which abstracts it.
- **Custom fields stay on the order, not the subscription** — plan accordingly when tying subs back to internal users.
- **Test mode toggle per store**: sandbox is baked into the production account, switched via a store-level toggle. Enable before pointing test traffic.

## Key reference URLs

- API getting started: https://docs.lemonsqueezy.com/api/getting-started
- Checkouts: https://docs.lemonsqueezy.com/api/checkouts
- Webhooks: https://docs.lemonsqueezy.com/help/webhooks
- Node SDK: https://github.com/lmsqueezy/lemonsqueezy.js
