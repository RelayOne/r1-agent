# analytics-tracking

> Analytics event design, attribution models, UTM handling, and privacy-compliant tracking

<!-- keywords: analytics, tracking, attribution, referral, affiliate, utm, events -->

## Event Taxonomy Design

1. **Use noun-verb naming.** `page_viewed`, `button_clicked`, `order_completed`. Never `click` alone -- too ambiguous.
2. **Namespace by domain.** `checkout.started`, `checkout.payment_entered`, `checkout.completed`. Enables filtering and ownership.
3. **Standard properties on every event:** `timestamp`, `user_id` (or anonymous ID), `session_id`, `device_type`, `page_url`.
4. **Event-specific properties are typed.** `order_completed` must have `order_id: string`, `total_cents: int`, `currency: string`. Never shove amounts into strings.
5. **Track negative paths.** `checkout.abandoned`, `signup.failed`, `search.no_results` are more valuable than success events for optimization.

## Attribution Models

### First-Touch
Assigns 100% credit to the first interaction. Best for measuring awareness channels.
```
user lands via Google Ad (day 1) -> reads blog (day 5) -> converts (day 10)
Credit: Google Ad = 100%
```

### Last-Touch
Assigns 100% credit to the final interaction before conversion. Best for measuring closing channels.

### Multi-Touch (Recommended)
Distribute credit across all touchpoints. Common approaches:
- **Linear:** Equal credit to each touchpoint
- **Time-decay:** Recent interactions get more weight. Use half-life of 7 days.
- **U-shaped:** 40% first, 40% last, 20% split among middle

Store all touchpoints per user -- you can recompute attribution models later.

## UTM Parameter Handling

1. **Capture on landing.** Parse `utm_source`, `utm_medium`, `utm_campaign`, `utm_term`, `utm_content` from URL on first page load.
2. **Persist in session storage.** UTM params vanish after navigation. Store them immediately.
3. **Attach to signup/conversion events.** Every conversion event should carry the UTM context that brought the user.
4. **Normalize values.** Lowercase all UTM values before storage: `Google` and `google` must not split your reports.
```javascript
const UTM_PARAMS = ['utm_source', 'utm_medium', 'utm_campaign', 'utm_term', 'utm_content'];
function captureUTM() {
  const params = new URLSearchParams(window.location.search);
  const utm = {};
  UTM_PARAMS.forEach(key => {
    const val = params.get(key);
    if (val) utm[key] = val.toLowerCase().trim();
  });
  if (Object.keys(utm).length) sessionStorage.setItem('utm', JSON.stringify(utm));
}
```

## Affiliate and Referral Tracking

1. **Unique referral codes per affiliate.** `?ref=partner123`. Map code to affiliate in your DB.
2. **Cookie the referral.** 30-day cookie for the referral code. First-touch wins (don't overwrite).
3. **Record referral at conversion.** Store `referrer_id` on the order/signup. This is your source of truth for payouts.
4. **Idempotent attribution.** One conversion = one payout. Deduplicate by order ID.

## Conversion Funnel Tracking

Track each step as a discrete event with a shared `funnel_id`:
- `funnel.step_viewed { funnel: "onboarding", step: 1, step_name: "create_account" }`
- `funnel.step_completed { funnel: "onboarding", step: 1, step_name: "create_account" }`
- Compute drop-off: `(step_N_completed / step_N_viewed) * 100`

## A/B Testing Instrumentation

1. **Assign variant at first exposure.** Hash `user_id + experiment_name` for deterministic assignment.
2. **Track exposure event.** `experiment.exposed { experiment: "pricing_v2", variant: "control" }`. Without exposure tracking, your results include users who never saw the change.
3. **Attach variant to downstream events.** Every conversion event during the experiment must carry the variant.
4. **Never reassign mid-experiment.** Sticky assignment prevents statistical contamination.

## Privacy-Compliant Analytics

1. **Consent before tracking.** No analytics cookies or events until the user opts in (GDPR/ePrivacy).
2. **Anonymous IDs by default.** Generate a random UUID. Only link to PII after authentication and consent.
3. **IP anonymization.** Truncate the last octet for IPv4, last 80 bits for IPv6 before storage.
4. **Data retention policy.** Auto-delete raw events after 13 months (GA4 default). Aggregate reports can live longer.
5. **Respect DNT and GPC headers.** `Sec-GPC: 1` or `DNT: 1` means suppress non-essential tracking.

## Server-Side vs Client-Side Tracking

| Concern | Client-Side | Server-Side |
|---------|------------|-------------|
| Ad blockers | Blocked ~30% | Not affected |
| PII control | Harder (runs in browser) | Full control |
| Latency | Adds page weight | Zero user impact |
| Accuracy | Subject to bot traffic | Filtered at source |
| Implementation | Drop-in snippet | Requires backend work |

**Recommendation:** Use server-side for revenue and conversion events (accuracy matters). Use client-side for UI interaction events (scroll depth, clicks). Hybrid approach gets you both coverage and accuracy.
