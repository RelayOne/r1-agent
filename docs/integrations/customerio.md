# Customer.io lifecycle email integration

## Operator runbook

R1 emits a typed bus-event stream to Customer.io for retention and
lifecycle email. Six canonical triggers map to Customer.io tracks:

| Trigger event (Customer.io) | When R1 emits |
|---|---|
| `signup` | First `SSOLoginSucceeded` per user (gated; **wires when A4 RelayOne SSO merges**) |
| `session_started` | Every `EventSessionInit` with a non-empty user_id |
| `activation` | First `EventSessionInit` per user (gated) |
| `mission_started` | Every `EventMissionCreated` |
| `first_mission` | First `EventMissionCreated` per user (gated) |
| `mission_completed` | Every `EventMissionConverged` |
| `first_completion` | First `EventMissionConverged` per user (gated) |
| `anti_trunc_fired` | Every anti-trunc engine block (wires once `EventAntiTruncFired` is on the hub bus) |
| `budget_alert` | Every `EventCostBudget80` / `_90` / `_Exceeded` |

`session_started`, `mission_started`, `mission_completed`,
`anti_trunc_fired`, and `budget_alert` always fire. The four
"first_*" milestones are gated by a SQLite flagstore at
`~/.r1/lifecycle.db` so they fire exactly once per `(tenant, user,
event)` tuple — daemon restart preserves the gate.

## Configuration

### Required env (Cloud Run + local dev)

| Variable | Purpose | Default |
|---|---|---|
| `CUSTOMERIO_SITE_ID` | Customer.io Site ID (basic-auth user) | (none — empty disables) |
| `CUSTOMERIO_API_KEY` | Customer.io API key (basic-auth password) | (none — empty disables) |
| `CUSTOMERIO_REGION` | `us` or `eu` (data residency) | `us` |

When both `CUSTOMERIO_SITE_ID` and `CUSTOMERIO_API_KEY` are empty,
R1 returns a no-op client. No goroutines start, no HTTP traffic
leaves the process.

### Tenant opt-out

Per-tenant opt-out via `r1.policy.yaml`:

```yaml
lifecycle:
  disabled: true             # tenant-global
  # or, future schema:
  # disabled_for_tenants: [tenant-id-1, tenant-id-2]
```

When `policy.LifecycleDisabled(tenantID)` returns true, the
subscriber drops events for that tenant before they reach the SDK.

### User opt-out

Customer.io's native unsubscribe link sets the user's `unsubscribed`
attribute server-side. The SDK honors it automatically — R1 does not
need to maintain a parallel suppression list.

## DSAR / GDPR data deletion

DSAR (data subject access request) deletion is a planned operator
CLI: `r1 admin lifecycle delete --user <id>`. This commit lands the
core library (`Client.Delete(ctx, userID)`) and the `FlagStore.DeleteUser`
hook; the cmd/r1 subcommand binding lands in a follow-up once
`cmd/r1/admin_*` dispatch is wired (see `specs/customerio-lifecycle.md`
§13 BLOCKED list).

Once the CLI is bound, calling `Delete` will:

1. Issue `DELETE /api/v1/customers/{userID}` against Customer.io
   (24h soft-delete window per Customer.io's documented behaviour).
2. Drop every row from `lifecycle.db` matching the user_id so a
   subsequent signup re-fires the `signup` milestone.
3. Write an `internal/audit` entry with op=`lifecycle_delete`,
   actor, tenant, user, timestamp.

## A/B testing

Use Customer.io's native A/B test feature in the campaign editor.
Do NOT roll a custom A/B harness in R1 — the event vocabulary is
fixed by this spec; copy variation belongs in the Customer.io UI.

## Troubleshooting

- **No events arriving in Customer.io**: confirm `CUSTOMERIO_SITE_ID`
  and `CUSTOMERIO_API_KEY` are both set; the no-op fallback emits
  zero events when either is empty. Check the `lifecycle.dropped`
  counter via the metrics endpoint — a non-zero value with no
  `lifecycle.queued` increment means the env is unset.
- **Activation never fires for a user**: query the flagstore:
  `sqlite3 ~/.r1/lifecycle.db "SELECT * FROM lifecycle_first_flags WHERE user_id='alice';"`.
  If a row exists, the activation has already fired once for that
  user — Customer.io stops emitting the milestone trigger.
- **EU data residency**: set `CUSTOMERIO_REGION=eu` to route to
  `track-eu.customer.io`. Unknown region values fall back to `us`
  with a warning log.
- **Anonymous user filter triggers unexpectedly**: confirm the
  upstream bus event carries `user_id` in `Custom`. Sessions without
  a verified SSO identity (A4) carry no user_id — that's the
  intended behaviour, not a bug.
