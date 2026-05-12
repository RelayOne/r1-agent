# PostHog Product Analytics — Operator Runbook

R1 ships first-class PostHog integration via `internal/analytics/` and the bus
subscriber registered in `internal/hub/builtin/analytics_subscriber.go`. This
document is the operator-facing runbook: how to provision a project, point
R1 at it, opt tenants out, and verify captures are landing.

The implementation is specified by `specs/posthog-analytics.md` (B1,
BUILD_ORDER 38) and depends on A4 RelayOne SSO (BUILD_ORDER 36) for the
tenant identifier that drives PostHog Group Analytics. Until A4 lands, the
analytics package keeps emitting per-user events; the tenant binding
activates automatically the first time a tenant-bound request flows
through.

## 1. Provisioning a PostHog project

1. Sign in at `https://us.posthog.com` (US region) or `https://eu.posthog.com`
   (EU region). Self-hosted operators point at their own deployment.
2. **Create a new project** for each R1 environment. Recommended layout:
   - `r1-prod` — production captures.
   - `r1-staging` — pre-prod captures, retained 30 days.
   - `r1-dev` — developer captures, retained 7 days, sampled 10%.
3. Open Project Settings → **API Keys** and copy the `phc_*` Project API key.
   This is the value R1 reads via `POSTHOG_API_KEY`. It is NOT the personal
   API key — leave that field empty (R1 does not use server-side feature
   flag evaluation in B1).
4. Enable **Group Analytics** for the `tenant` group type (Project Settings
   → Groups → New group type → `tenant`). Without this, per-tenant
   dashboards remain empty even though captures arrive.

## 2. Wiring R1 to PostHog

Three environment variables drive the client (all read by
`internal/analytics/analytics.go:FromEnv`):

| Var | Default | Notes |
|---|---|---|
| `POSTHOG_API_KEY` | `""` | Required. Empty value yields a no-op client (zero HTTP requests). |
| `POSTHOG_HOST` | `https://us.i.posthog.com` | Override for EU SaaS or self-hosted. |
| `R1_ENV` | `development` | Stamped onto every event as the `environment` property. |
| `ANALYTICS_DISABLED` | unset | Set to `1` to fully suppress captures even when a key is present. |
| `POSTHOG_FLUSH_AT` | `100` | Batch size. |
| `POSTHOG_FLUSH_INTERVAL` | `5s` | Max wait before flushing a partial batch. |
| `POSTHOG_SHUTDOWN_WAIT` | `2s` | Drain budget on graceful shutdown. |

### 2.1 Cloud Run / GCP

In line with `rules_deployment_gcp.md`:

1. Create a Secret Manager entry `POSTHOG_API_KEY` per environment. Version-pin
   it; rotate via a new version, not a config edit.
2. Inject the secret into the services that talk to the bus:
   - `r1-server` — enabled by default.
   - `r1d` (daemon) — enabled by default.
   - `r1` (one-shot CLI build) — disabled by default; opt in by exporting
     `POSTHOG_API_KEY` at run time.
3. Set `POSTHOG_HOST` on staging/dev clusters to the self-hosted dev project
   so production captures stay clean.

### 2.2 Local development

```
export POSTHOG_API_KEY=phc_local_dev_key
export POSTHOG_HOST=https://us.i.posthog.com  # or your self-hosted URL
export R1_ENV=development
r1 serve
```

To prove a clean dev session, set `ANALYTICS_DISABLED=1` and verify that the
`r1 admin metrics | grep analytics` snapshot shows zero captured events.

## 3. Per-tenant opt-out

The `r1.policy.yaml` parser reads the following block:

```yaml
analytics:
  disabled: false
  tenant_optouts:
    - tenant-uuid-1
    - tenant-uuid-2
```

- `disabled: true` short-circuits the client to a no-op regardless of
  `POSTHOG_API_KEY`. Use this for fully-air-gapped customer deployments.
- `tenant_optouts` is consulted on every capture. Matching tenants drop the
  event before it reaches the SDK; allowed tenants continue to emit normally.

The opt-out list is loaded once at startup. Reload via the daemon
`daemon.reload_config` callback wires the new list onto the running client
without dropping in-flight captures.

## 4. Importing dashboards, funnels, cohorts

The following files are checked in alongside the integration so they
survive a redeploy:

- `docs/analytics/funnels.md` — three product funnels (activation,
  honesty-value, cost-engagement). Each entry includes a fenced JSON
  block paste-ready into PostHog's `Create Insight → Funnel → Import`.
- `docs/analytics/cohorts.md` — four cohorts (power users, anti-trunc
  beneficiaries, at-risk, cost-sensitive tenants).
- `docs/analytics/dashboards/r1-overview.json` — top-level dashboard JSON
  with four tiles. Import via `Dashboards → New → Import JSON`.

Copy-paste preserves the underlying HogQL or filter expressions; PostHog
recomputes everything off the captured event stream.

## 5. Verifying captures

Three signals confirm the integration is alive:

1. **Process metrics.** Run `r1 admin metrics | grep analytics` and look
   for non-zero values on:
   - `analytics.captured` — events that reached the SDK enqueue path.
   - `analytics.captured_noop` — events the no-op client would have
     captured (useful during dry runs).
   - `analytics.dropped` — events lost to a full queue. Steady-state
     should be zero; sustained drops mean PostHog ingress is unhappy.
   - `analytics.no_match` — events with no taxonomy mapping. Non-zero
     here is a code bug.
2. **PostHog Live Events** view — open the project, then `Activity → Live
   events`. Trigger `session_started` by booting `r1` against the wired
   project; the event should land within ~5 seconds.
3. **Bus replay** — replay an audit log against a recording server (see
   `internal/replay/`) and confirm every `mission.*` event lands.

## 6. Retention and sampling

PostHog SaaS retains raw events for 7 years by default; self-hosted retention
is unlimited. To stay inside the EU GDPR retention envelope, set Project
Settings → Data → Retention to 90 days on EU clusters.

Sampling is a per-project knob; B1 emits 100% of events. Consider sampling
high-volume events (e.g. `cost_event_recorded`) at 10% in production once
volume crosses 1M events/day.

## 7. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Zero events in Live Events but `analytics.captured > 0` | Wrong project key (R1 writes, PostHog routes elsewhere). | Verify `POSTHOG_API_KEY` against Project Settings → API Keys. |
| `analytics.dropped` climbing | PostHog ingress slow or unreachable; queue full. | Check `POSTHOG_HOST` reachability; raise `POSTHOG_QUEUE_DEPTH`. |
| Events arrive but `$groups.tenant` empty | A4 RelayOne SSO not yet wired into the running build. | Confirm `correlation.IDs.TenantID` is set; the analytics package reads it via the shim at `internal/analytics/tenant_id.go`. |
| Funnel step "0 → 1" is 100% but "1 → 2" is 0% | Property name drift; PostHog filters on exact strings. | Run `git log -- docs/analytics/funnels.md` and reconcile with the latest taxonomy in `internal/analytics/taxonomy.go`. |
| `analytics.no_match` non-zero | A bus event was added with no PostHog mapping. | Either add it to `BusToAnalytics`, or accept the silent drop. |

## 8. Privacy and PII

The analytics package enforces a property allowlist (see
`internal/analytics/taxonomy.go` adapters) and a regex-driven PII scrubber
(`internal/analytics/redact.go`). The following NEVER cross the analytics
boundary:

- Raw prompts, model completions, tool outputs.
- User emails, names, full file paths beyond the top-level repo root.
- Bearer tokens or API keys (matched by heuristic).

Ledger-backed properties (any field whose name ends in `_node_id`) are
checked against `ledger.IsRedacted(nodeID)`; if the content tier is wiped,
the value is rewritten to `[REDACTED]` before send.

## 9. Disabling for an entire environment

For air-gapped deployments or compliance-driven kill switches, set BOTH:

```
POSTHOG_API_KEY=""
ANALYTICS_DISABLED=1
```

The first ensures the SDK is never constructed; the second is the belt-and-
braces signal honored by the policy overlay. Either alone is sufficient.

## 10. References

- Spec: `specs/posthog-analytics.md`.
- Implementation: `internal/analytics/`, `internal/hub/builtin/analytics_subscriber.go`.
- Taxonomy: `internal/analytics/taxonomy.go`.
- Funnels: `docs/analytics/funnels.md`.
- Cohorts: `docs/analytics/cohorts.md`.
- PostHog Go SDK: `https://github.com/posthog/posthog-go`.
- PostHog Group Analytics: `https://posthog.com/docs/product-analytics/group-analytics`.
