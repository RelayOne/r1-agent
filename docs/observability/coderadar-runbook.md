# CodeRadar Operator Runbook — R1 Dogfood (B3)

How to operate the CodeRadar dogfood pipeline once it's live in prod.

## Configuration knobs

- `CODERADAR_DSN` (secret) — DSN format
  `<scheme>://<api_key>@<host>[/v1[/errors]]`. Bound per env via
  `r1-{dev,staging,prod}-shared-CODERADAR_DSN` in Secret Manager and
  injected by `services/cloudbuild-deploy.yaml`.
- `CODERADAR_SAMPLE_RATE` (env, 0.0–1.0) — overrides the default rate
  for all events. Per-event prod overrides for `provider.call_completed`
  and `tool.call_completed` are hardcoded in
  `internal/coderadar/sampler.go` (10% in prod) and not env-tunable —
  changing them is a deploy.
- `R1_ENV` (env) — `local` | `dev` | `staging` | `prod`. `local`
  short-circuits the queue (no network, no error).

## Disabling the subscriber in an emergency

Unset the DSN and roll the affected service:

```bash
gcloud run services update r1-coord-api-prod \
  --region=us-central1 \
  --remove-secrets=CODERADAR_DSN
```

The wrapper's `Enabled()` returns false for an empty DSN; the
subscriber's `handle` short-circuits before queuing. The hub bus and
all gate / transform hooks continue to run.

## Rotating the DSN

```bash
echo -n "<new-dsn>" | gcloud secrets versions add r1-prod-shared-CODERADAR_DSN --data-file=-
# Roll the service to pick up the new secret version:
gcloud run services update r1-coord-api-prod --region=us-central1
```

Per-service rolls are independent; rotate one, smoke-check with
`make smoke-coderadar ENV=prod` before the next.

## Checking subscriber health

The `r1 ops coderadar-stats` CLI (T16) surfaces live counters:

```
$ r1 ops coderadar-stats
emitted_total           = 12483
dropped_queue_total     =     2
dropped_sample_total    =  4910
dropped_redact_total    =     0
emit_errors_total       =     0
queue_depth             =     7
queue_capacity          =  4096
```

A persistently non-zero `emit_errors_total` plus a recent
`last_emit_error` indicates the circuit may be open — see the
`coderadar_circuit_open` field on the next `service_health_check`.

## Querying events

The CodeRadar admin UI's events explorer accepts the canonical event
name in the `Message` filter, the service tag in `service_name`, and
the correlation prefix in `correlation_id`. Example: find every event
for a mission:

```
service_name = r1-server
correlation_id ~ "s:<missionID>"
```

(R1 uses the mission ID as the fallback session-ID when no
ctx-borne session ID is set — see
`internal/hub/builtin/coderadar_subscriber.go::attachCorrelationFromEvent`.)

## Smoke test

```bash
make smoke-coderadar ENV=dev
```

Materializes the env-scoped DSN, builds + runs the
`coderadar_smoke`-tagged test that POSTs a live `service_started`
event. Pass criterion: 2xx within 5s.

## Alerts → runbook links

| Alert | Runbook | First action |
|-------|---------|--------------|
| `provider_p99_slow`     | `docs/runbooks/provider_p99_slow.md`     | Check provider status page, then fallback chain via `model.fallback` events. |
| `antitrunc_burst`       | `docs/runbooks/antitrunc_burst.md`       | Pull recent `antitrunc.fired` events, group by `pattern_matched`. |
| `mission_abort_rate`    | `docs/runbooks/mission_abort_rate.md`    | Pull recent `mission.aborted` events, group by `abort_reason`. |
| `service_health_stale`  | `docs/runbooks/service_health_stale.md`  | Check Cloud Run revision health; the binary may be wedged or down. |

The four runbook stubs are tracked separately under `docs/runbooks/`;
their incident-response checklists are out of scope for this spec.

## Secret Manager pre-flight (manual ops step)

If `CODERADAR_DSN` is already in Secret Manager per the B3 SOW, verify
each env-scoped name exists:

```bash
for ENV in dev staging prod; do
  gcloud secrets versions list r1-$${ENV}-shared-CODERADAR_DSN || \
    echo "MISSING: r1-$${ENV}-shared-CODERADAR_DSN"
done
```

Create the missing names per env BEFORE the first deploy:

```bash
gcloud secrets create r1-staging-shared-CODERADAR_DSN \
  --replication-policy=automatic
echo -n "<dsn>" | gcloud secrets versions add r1-staging-shared-CODERADAR_DSN --data-file=-
```
