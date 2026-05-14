# R1 lifecycle email campaigns

Operators author these inside the Customer.io campaign editor. R1
emits the trigger events listed below; the campaign's send conditions
are configured Customer.io-side. Editing copy or schedule is a
marketing-side change in Customer.io. Code changes are only required
when the trigger event itself changes — those are R1 releases tagged
with `spec(B2)` or `spec(B2-*)`.

## Six canonical lifecycle moments

| Day | Trigger event | Working subject | Suppression rule |
|---|---|---|---|
| 0 | `signup` | "Welcome — how to write your first plan." | `unsubscribed` |
| 1 | `signup` ∧ no `activation` 24h | "Start your first session" → platform.r1.run | `activation` arrives → cancel |
| 3 | `signup` ∧ no `first_mission` 72h | "Examples to try" | `first_mission` → cancel |
| 7 | `signup` ∧ no `first_completion` 7d | "Stuck? Drop the operator a line" | `first_completion` → cancel |
| 14 | `mission_completed` count ≥ 1 | "Power user playbook" | one-shot per user |
| 30 | `anti_trunc_fired` count ≥ 1 in last 30d | "How R1's anti-truncation saved you from N false completions" | one-shot/month |
| -- | session in last 30d | Monthly "Your R1 month in review" | active = session in 30d |
| -- | `budget_alert` (80/90/exceeded) | "Account budget at 80%/90%/exceeded" | always send |

## Cohort segments (Customer.io UI)

- **Power users** — `mission_completed` ≥ 10 in trailing 7d.
- **At-risk** — `activation` ≥ 1 ∧ no `session_started` in last 3 days.
- **Churn** — no `session_started` in last 14 days.
- **Cold** — no `session_started` in last 30 days; suppress all
  campaigns until next `SSOLoginSucceeded` re-warms the cohort.

## Authoring a new campaign

1. Confirm the trigger event already exists in R1 — see
   `internal/hub/builtin/lifecycle_subscriber.go` `Register()` for
   the canonical list.
2. If you need a NEW trigger event that R1 doesn't yet emit, open an
   issue tagged `spec(B2-extend)` — adding a hub bus event is a code
   change, not a Customer.io change.
3. Inside Customer.io, build the campaign against the existing
   trigger. Use the suppression-rule column above as the template.
4. A/B test variants live in Customer.io's native A/B feature — do
   not branch in R1 code.
5. After launch, monitor the `lifecycle.queued` / `lifecycle.dropped`
   counters on the R1 metrics endpoint to confirm events are reaching
   Customer.io.

## PII inventory

Only PII that crosses the wire to Customer.io:

- `email` (identify trait) — required for delivery
- `user_id` (Customer.io customer ID) — opaque hash from A4 SSO
- `tenant_id`, `plan_tier`, `signup_date` — non-PII identity metadata

Never sent to Customer.io: raw prompts, file paths, mission titles,
ledger node IDs, IP addresses, tool inputs. The choke point is
`internal/lifecycle/traits.Build` — the reflection test in
`traits_test.go` fails the build on any new field added to the
allowlist.
