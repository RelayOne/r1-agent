# r1-browser Cloud Run Service — Operations

Spec: [`specs/browser-remote-sandbox.md`](../../specs/browser-remote-sandbox.md) (C6, §T3 + §T10 + §T14a).
Status: Authored 2026-05-12. First deploy is operator-driven.

## 1. Container image

The `r1-browser` image is built from `Dockerfile.r1-browser` at the repo root. Two stages:

1. **builder**: `golang:1.25-bookworm` compiles `services/r1-browser/` to a static `/out/r1-browser` binary.
2. **runtime**: `debian:bookworm-slim` + `chromium` + `dumb-init` + the static binary.

### Why debian-slim, not distroless

Chromium needs glibc + a wide shared-lib surface (`libnss3`, `libxss1`, `libatk-bridge`, `fonts-liberation`, …). A distroless variant requires walking those libs in via `ldd` at build time, which the spec calls out as a future optimization. Today we ship `debian-slim` so the container is debuggable in production and the image build is reproducible.

If image size becomes a problem (current footprint ~300 MiB), the migration path is:

1. Switch to `ghcr.io/browserless/chrome:v2.x` as the runtime base (their Docker image already does the lib-walk).
2. Or layer the static binary on `gcr.io/distroless/cc-debian12` after manually copying Chromium + its libs.

Decision recorded; the current image is sufficient for the v1 deploy.

## 2. Cloud Run config

See `services/cloudbuild-r1-browser.yaml` for the canonical settings. The standing rules + the per-service tuning are:

- `region=us-central1`
- `min-instances=1` (one warm idle instance to absorb cold starts)
- `max-instances=50`
- `concurrency=1` (the load-bearing isolation primitive — one Chromium per container)
- `memory=2Gi`, `cpu=1`
- `timeout=300s`
- `no-cpu-throttling` (Chromium is CPU-bursty)
- `ingress=internal-and-cloud-load-balancing` (no public DNS; reached over the internal VPC)
- `no-allow-unauthenticated` (Cloud Run IAM gates traffic via the two-SA pattern below)

## 3. IAM — the two-SA pattern

Spec §T10:

- **`r1-browser-runtime@<project>.iam.gserviceaccount.com`** — the service account the container runs as. Grant NOTHING beyond `roles/run.invoker` on itself (defense in depth — the runtime SA should not even be able to read its own Cloud Logging).
- **`r1-browser-invoker@<project>.iam.gserviceaccount.com`** — the SA backend services (r1-coord-api) authenticate as when calling `r1-browser`. Grant `roles/run.invoker` on the `r1-browser-<env>` Cloud Run service to this SA. Bind it to the r1-coord-api Cloud Run instance so the metadata server can mint ID tokens audience-bound to `https://browser.r1.run`.

Copy-pasteable `gcloud` commands (run once per env):

```bash
# Per-env: dev / staging / prod
ENV=staging
PROJECT_ID=resolute-parity-484218-g1

gcloud iam service-accounts create r1-browser-runtime \
  --project=$PROJECT_ID \
  --display-name="r1-browser Cloud Run runtime"

gcloud iam service-accounts create r1-browser-invoker \
  --project=$PROJECT_ID \
  --display-name="r1-browser invoker (caller-side identity)"

# Grant the invoker run.invoker on the service:
gcloud run services add-iam-policy-binding r1-browser-$ENV \
  --project=$PROJECT_ID \
  --region=us-central1 \
  --member="serviceAccount:r1-browser-invoker@$PROJECT_ID.iam.gserviceaccount.com" \
  --role=roles/run.invoker

# Bind the invoker SA to r1-coord-api so the metadata server mints ID tokens for it:
gcloud run services update r1-coord-api-$ENV \
  --project=$PROJECT_ID \
  --region=us-central1 \
  --service-account=r1-browser-invoker@$PROJECT_ID.iam.gserviceaccount.com
```

## 4. Deploy

The first deploy is operator-driven — this commit authors the image + cloudbuild spec but does NOT push to GCP. To deploy:

```bash
# 1. Confirm SAs exist (step 3 above).
# 2. Trigger Cloud Build manually with the staging substitution:
gcloud builds submit \
  --config=services/cloudbuild-r1-browser.yaml \
  --substitutions=_ENV=staging \
  --region=us-central1 \
  .

# 3. Smoke-check from a Cloud Shell or VM in the VPC:
URL=$(gcloud run services describe r1-browser-staging --region=us-central1 --format='value(status.url)')
curl -s "$URL/livez"            # expect {"ok":true,...}

# 4. Promote to prod ONLY after a soak week.
gcloud builds submit \
  --config=services/cloudbuild-r1-browser.yaml \
  --substitutions=_ENV=prod \
  --region=us-central1 \
  .
```

## 5. CDP debugging — don't attach DevTools

The container is single-tenant and ephemeral. Attaching a live DevTools session against a running Chromium would (a) break the tenant-isolation invariant by giving an operator simultaneous visibility into a customer session, and (b) bypass the bearer-auth gate.

The supported debugging path: replay the failing event sequence in a dev environment. The bus emits `browser.session_opened`, `browser.navigate`, `browser.error`, `browser.session_closed` — pipe the captured events from the production session into a local r1-browser container (via the `inhouse_live` integration test harness) and reproduce the failure there.

## 6. Network-policy defense-in-depth

Spec §T5b (optional): when in-page `Network.setRequestInterception` is the only egress check, a future Chromium version could (in theory) bypass it. The defense-in-depth answer is to run a squid or tinyproxy inside the container at `localhost:3128`, configure Chromium with `HTTP_PROXY=localhost:3128`, and put the same allow/deny rules in the proxy config.

Today's v1 ships WITHOUT this layer — the CDP interception path passed every conformance test. If T13b's nightly live test ever surfaces a bypass, this is the next checkbox.

## 7. Observability

Every browser session emits to the existing event-log + CodeRadar pipeline (`internal/coderadar/`). Standard attributes per the spec: `tenant_id`, `mission_id`, `provider`, `session_id`, `endpoint_host`, `duration_ms`. See `docs/integrations/remote-browser.md` § Troubleshooting for the diagnostic mapping.

Cloud Run vCPU-seconds are tagged `Category="browser-inhouse"` in the existing cost ingester — surfaces in the per-tenant rollups alongside `browser-remote` (Browserless) units.
