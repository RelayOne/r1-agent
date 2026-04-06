# payment-integration

> Payment processing, subscription billing, webhook handling, and PCI compliance

<!-- keywords: payment, stripe, billing, subscription, checkout, invoice, refund -->

## Stripe Integration Patterns

1. **Use PaymentIntents, not Charges.** PaymentIntents handle SCA (Strong Customer Authentication) and 3D Secure automatically. The Charges API is legacy.
2. **Create PaymentIntent server-side, confirm client-side.** Never pass full card numbers to your server.
3. **Store the Stripe customer ID.** Create a `stripe_customer_id` column on your users table. One customer per user, created at signup or first purchase.
4. **Use Stripe's idempotency keys.** Pass `Idempotency-Key` header on every mutating API call. Use your internal order ID or a UUID. Retries become safe.
```go
params := &stripe.PaymentIntentParams{
    Amount:   stripe.Int64(2000), // $20.00 in cents
    Currency: stripe.String("usd"),
    Customer: stripe.String(user.StripeCustomerID),
}
params.SetIdempotencyKey(order.ID)
pi, err := paymentintent.New(params)
```
5. **Never store card details.** Use Stripe Elements or Checkout Sessions. Your server only sees tokens and PaymentMethod IDs.

## Subscription Lifecycle Management

Key states: `trialing` -> `active` -> `past_due` -> `canceled` / `unpaid`

1. **Handle trial end.** Send reminder emails 3 days and 1 day before trial ends. Create the subscription with `trial_end` timestamp.
2. **Grace period for failed payments.** Configure Stripe Smart Retries. Allow 3-7 days of `past_due` before canceling. Notify user on each failure.
3. **Prorate on plan changes.** Use `proration_behavior: "create_prorations"` for upgrades. For downgrades, apply at period end.
4. **Cancel at period end, not immediately.** `cancel_at_period_end: true` lets users keep access until they've paid through.
5. **Track subscription status locally.** Mirror `status`, `current_period_end`, and `cancel_at_period_end` in your database. Use webhooks to keep it in sync.

## Webhook Handling

1. **Verify signatures.** Always validate `Stripe-Signature` header against your webhook secret. Reject unsigned requests.
```go
event, err := webhook.ConstructEvent(body, sig, webhookSecret)
if err != nil {
    return http.StatusBadRequest // signature verification failed
}
```
2. **Idempotent processing.** Store `event.ID` in a processed-events table. Check before processing. Stripe retries webhooks up to 3 days.
3. **Return 200 quickly.** Process the event asynchronously. Stripe times out after 20 seconds and will retry, causing duplicate processing.
4. **Handle these events at minimum:**
   - `checkout.session.completed` -- fulfill the order
   - `invoice.payment_succeeded` -- extend subscription access
   - `invoice.payment_failed` -- notify user, start grace period
   - `customer.subscription.updated` -- sync status changes
   - `customer.subscription.deleted` -- revoke access

## PCI Compliance

- **Use Stripe Elements or Checkout.** Card data never touches your server. This keeps you at PCI SAQ-A (simplest level).
- **Never log card numbers, CVVs, or full card data.** Not in logs, not in error messages, not in analytics.
- **HTTPS everywhere.** TLS 1.2+ required for any page that loads payment forms.
- **Access control.** Restrict Stripe dashboard access. Use restricted API keys with minimal permissions per service.

## Pricing Model Implementation

### Tiered Pricing
```
Tier 1: 0-100 units    @ $0.10/unit
Tier 2: 101-1000 units @ $0.08/unit
Tier 3: 1001+ units    @ $0.05/unit
```
Use Stripe's `tiered` pricing mode on the Price object. Calculate locally for display, but let Stripe be the source of truth for billing.

### Usage-Based Billing
Report usage via Stripe's Usage Records API. Aggregate in your system and push daily or hourly.
```go
usageRecord, _ := usagerecord.New(&stripe.UsageRecordParams{
    SubscriptionItem: stripe.String(subItemID),
    Quantity:         stripe.Int64(150),
    Timestamp:        stripe.Int64(time.Now().Unix()),
    Action:           stripe.String("increment"),
})
```

### Per-Seat Pricing
Update subscription quantity when seats are added or removed. Use `proration_behavior` to handle mid-cycle changes.

## Invoice Generation

1. **Let Stripe generate invoices.** For subscriptions, invoices are created automatically. For one-off charges, create invoice items then finalize.
2. **Add metadata.** Include your internal order ID, customer reference, and tax ID on the invoice.
3. **Auto-collection vs manual.** Use `auto_advance: true` for automatic charging. Use `false` when you need manual approval before charging.
4. **Tax handling.** Use Stripe Tax for automatic calculation or set `tax_rates` on line items. Store tax IDs via `customer_tax_ids`.

## Refund and Dispute Handling

1. **Refund to original payment method.** Use `refund.New()` with the PaymentIntent or Charge ID. Partial refunds are supported.
2. **Track refund reason.** Store internally: `customer_request`, `duplicate`, `fraudulent`, `product_not_delivered`.
3. **Disputes (chargebacks).** Respond within 7 days with evidence: receipt, delivery confirmation, terms of service, customer communication logs.
4. **Prevention:** Use Stripe Radar for fraud detection. Require AVS and CVC checks. Send receipts immediately.

## Currency and Tax

1. **Store amounts in smallest currency unit.** Cents for USD, yen for JPY (zero-decimal). Use `int64`, never floats.
2. **Specify currency explicitly.** Never assume USD. Store currency code alongside every amount.
3. **Tax varies by jurisdiction.** US: sales tax varies by state/city. EU: VAT varies by country and product type. Use a tax calculation service.
4. **Multi-currency pricing.** Create separate Stripe Prices per currency, or use Stripe's automatic currency conversion. Display prices in the customer's local currency.
5. **Rounding.** Always round at the line-item level, not the total. Rounding the total introduces penny discrepancies.
