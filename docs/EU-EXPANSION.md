# EU Expansion Seam

> Status: **design seam, not yet deployed.** This document specifies how the
> R1 hosted footprint extends to the EU when demand or a data-residency
> requirement arrives. Nothing here is live today; the current demo footprint
> is single-region (`us-central1`). This is the honest "later" plan, written
> now so the seam is a deliberate boundary rather than a rewrite.

## The core principle: the runtime is region-agnostic; only the hosted control surface is regional

R1 has two halves, and only one of them has a geography:

1. **The OSS runtime** (`r1` binary, MIT). It runs on the developer's own
   machine or CI. It holds no regional state. It talks to whatever hosted
   control surface it is pointed at. A German developer and a US developer run
   the *identical* binary; the only difference is the URL it calls home to.

2. **The hosted control surface** (`r1-coord-api`, `r1-admin`, and — in the
   real product — the RelayGate inference path). This is what has a region: it
   terminates sessions, mints/verifies operator JWTs, holds the active-session
   registry, and (in production) is the anchoring + audit boundary. This is the
   piece that a data-residency regime cares about.

Because the split is already this clean, "go to the EU" is **stand up a second
copy of the control surface in an EU region and pin EU users to it** — not a
re-architecture.

## What exists today (the thing being extended)

The demo footprint deployed for the convergence program:

| Surface | Service | Region | State |
| --- | --- | --- | --- |
| Coordination API | `r1-demo-coord-api` | `us-central1` | Stateless — in-memory TTL active-session registry only; reads no database |
| Admin / governance | `r1-demo-admin` | `us-central1` | Stateless — renders live coord-api data over HTTP at request time |
| Inference | RelayGate (NA) | `us-central1` | The hosted model path; not part of the demo footprint |

Key fact that makes EU cheap: **the coord-api and admin surfaces are
stateless.** The active-session registry lives in process memory (a restart
just waits one heartbeat interval). There is no cross-region database
replication to design for the session/governance surfaces. Where a surface
*does* hold state (in the real product: the anchored audit record, license
keys, billing), that state — not the compute — is what the EU boundary must
localize.

## The EU seam, step by step

### 1. Deploy an EU coordination API

Deploy a second Cloud Run service in an EU region (e.g. `europe-west1`),
identical image, identical scale-to-zero shape:

```bash
gcloud run deploy r1-demo-coord-api-eu \
  --project=resolute-parity-484218-g1 --region=europe-west1 \
  --image=us-central1-docker.pkg.dev/resolute-parity-484218-g1/honest-demo/r1-demo-coord-api:latest \
  --min-instances=0 --cpu=1 --memory=512Mi --port=8080 --allow-unauthenticated \
  --set-env-vars=R1_ENV=demo,R1_REGION=eu \
  --set-secrets=AUTH_JWT_SECRET=honest-demo-r1-auth-jwt-secret:latest
```

The image is region-agnostic — the same artifact that serves NA serves the EU.
Only the deploy region and the surface's home URL change. (Artifact Registry
`honest-demo` is multi-region-readable; an EU-local AR mirror is a later
cost/latency optimization, not a correctness requirement.)

### 2. Pin EU users to the EU surface

Pinning happens at the runtime's configuration boundary, not inside the binary:

- **Onboarding (`honest.yaml`)** carries the coord-api URL. An EU tenant is
  onboarded with `coord_api_url: https://api.eu.r1.run` (or the EU `*.run.app`
  URL) instead of the NA one. The binary reads it; nothing is compiled in.
- **Operator identity.** JWTs are issued per-region by the region's coord-api
  and its RelayOne OIDC binding. An EU operator authenticates against the EU
  coord-api; the EU admin verifies EU-issued tokens. The shared HS256 secret is
  a *demo* simplification — in production each region holds its own signing key
  (see the shared-state caveat below).
- **Routing.** For a single public hostname (`api.r1.run`) with geo-routing,
  put a latency/geo load-balancer in front and let it steer EU clients to the
  EU backend. For the demo, distinct per-region URLs are simpler and equally
  honest.

### 3. Keep the audit story regional

In production the session receipt is anchored and becomes the compliance
artifact (see `docs/DEMO-SCRIPT.md` and the `Anchorer` seam). The EU deployment
anchors against an EU-resident record store so the audit trail for EU sessions
never leaves the region. The anchoring *mechanism* (trusted-timestamp default
via the Go `honest-crypto` port) is identical; only the record's residency
changes. This is the one place where "EU" is more than a redeploy — it is a
data-residency decision about where the canonical audit record lives.

## Shared-state caveat (must be stated)

The demo deliberately shares one secret (`honest-demo-r1-auth-jwt-secret`)
across surfaces and one Cloud SQL instance (`honest-demo-sql`) across products —
shared blast radius, acceptable for a demo, **not** for a real EU
data-residency posture. A production EU deployment localizes:

- **Signing keys** — each region holds its own JWT signing key (KMS-backed), so
  an NA key compromise does not forge EU operator tokens.
- **The canonical audit record store** — EU session receipts anchor to an
  EU-resident store.
- **Any billing / license / identity state** — localized or replicated under an
  explicit residency contract, not implicitly shared.

Compute (coord-api, admin) can stay a single portable image; **state** is what
the EU boundary localizes.

## Why this is a seam and not a promise

Everything above is a redeploy of an existing, stateless, region-agnostic image
plus a per-region key and record-store decision — no new service to write, no
runtime change, no protocol change. That is the whole point of keeping the
runtime region-agnostic and the control surface thin: the EU is a configuration
and infrastructure step, taken when a real EU requirement lands, not a
re-architecture. Until then, this document is the boundary.
