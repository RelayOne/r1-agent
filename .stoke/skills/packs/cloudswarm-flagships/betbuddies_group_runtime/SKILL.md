---
name: betbuddies_group_runtime
description: Deterministic runtime contract for the CloudSwarm BetBuddies private pool flagship flow.
---

# betbuddies_group_runtime

Use this capability when a CloudSwarm operator has already chosen the
BetBuddies flagship and needs the exact R1-side runtime contract:
required credentials, rule-lock expectations, settlement approval
gates, and the summary shape that the flow should return to the
workspace.

## Acceptance criteria

- Requires a non-empty `event_title`.
- Requires at least one invitee in `invitees`.
- Requires a positive `stake_amount_cents`.
- Restricts `ledger_backend` to `google_sheets`.
- Returns the five-step hero-skill chain:
  `gmail_draft` → `payment_link` → `google_sheets_write` →
  `google_sheets_read` → `stripe_charge`.
- Emits approval guidance for invite sends, post-lock rule edits, and
  every Stripe settlement action.
