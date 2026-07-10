# R1 Demo Script

> The reference walkthrough for the R1 honest-demo artifact. It has two parts:
> **Part A** is the *live hosted governance surface* — two Cloud Run services
> you can `curl` right now, with the exact verified happy-path output. **Part B**
> is the *playable local POC* — the `honest.yaml` + Claude Code hooks flow that
> shows the whole honest-stack thesis in a tool developers already use.
>
> Everything shown as command output below was captured live on
> 2026-07-10 against the deployed services and a fresh build of this branch.
> Synthetic data only. Where a step genuinely requires a model-provider API key,
> it is marked **BLOCKED(provider-key)** — an accepted external dependency; the
> governance and verification surfaces are demonstrated without one.

## What the demo proves

R1 refuses to lie about completion, and it makes that refusal *auditable*. A
session becomes a signed, anchored, independently-verifiable record:

1. **SessionStart** loads the agent's identity and its seeded context (IKL) —
   tiered role → domain → task text prepended to the system prompt.
2. **PreToolUse** enforces a signed action spec before a tool runs (reject
   before execute) and records the decision to the ledger.
3. **SessionEnd** extracts candidate learnings.
4. The **session receipt is anchored** (trusted-timestamp default) and anyone
   can verify it — without trusting R1.

Meanwhile the **hosted coordination + governance surface** shows the live fleet:
who is running, under what operator identity, with real auth enforcement (not a
wide-open dashboard).

---

## Part A — the live hosted governance surface

Two scale-to-zero Cloud Run services (project `resolute-parity-484218-g1`,
region `us-central1`, `--min-instances=0`):

| Surface | URL |
|---|---|
| Coordination API (`r1-demo-coord-api`) | `https://r1-demo-coord-api-927350204262.us-central1.run.app` |
| Admin / governance (`r1-demo-admin`) | `https://r1-demo-admin-927350204262.us-central1.run.app` |

Both are **stateless** (in-memory TTL active-session registry; no database), so
there is no per-request DB cost and a cold start is a fast Go binary.

### A.1 — Public health + version (no auth)

```console
$ curl https://r1-demo-coord-api-927350204262.us-central1.run.app/livez
{"ok":true,"service":"r1-coord-api","env":"demo","version":"9103efd5","uptime_sec":21}

$ curl https://r1-demo-coord-api-927350204262.us-central1.run.app/v1/version
{"env":"demo","service":"r1-coord-api","version":"9103efd5"}
```

### A.2 — The governance gate is real: no token, no fleet data

`GET /v1/sessions` is the operator fleet view. Unauthenticated → **401 by
design.** This is the honest posture: the surface exists and enforces, rather
than leaking or faking.

```console
$ curl https://r1-demo-coord-api-927350204262.us-central1.run.app/v1/sessions
{"ok":false,"error":"missing or malformed Authorization header"}          # HTTP 401
```

### A.3 — The full operator happy-path (with an operator JWT)

An operator authenticates (RelayOne OIDC in production; here we mint an HS256
operator token against the deployment's `AUTH_JWT_SECRET`). A daemon reports a
synthetic session; the operator lists the fleet:

```console
# daemon heartbeat — POST /v1/sessions/report (any authenticated principal)
$ curl -X POST .../v1/sessions/report -H "Authorization: Bearer $OP_JWT" \
       -d '{"daemon_id":"demo-daemon-1","session_id":"sess-synthetic-001",
            "workdir":"/synthetic/demo-repo","status":"active","cost_usd":0.0123}'
{"accepted":true,"ok":true}                                               # HTTP 200

# operator fleet view — GET /v1/sessions
$ curl .../v1/sessions -H "Authorization: Bearer $OP_JWT"
{"ok":true,"page":1,"page_size":50,"total":1,"active_ttl_sec":300,
 "sessions":[{"daemon_id":"demo-daemon-1","session_id":"sess-synthetic-001",
   "workdir":"/synthetic/demo-repo","status":"active",
   "last_activity":"2026-07-10T07:11:44Z","cost_usd":0.0123}]}            # HTTP 200

# role enforcement — an authenticated NON-operator token cannot enumerate the fleet
$ curl .../v1/sessions -H "Authorization: Bearer $MEMBER_JWT"
{"ok":false,"error":"operator role required"}                            # HTTP 403
```

Full transcript: [`audit/converge-2026-07-08/D-R1/verify-coord-api.txt`](../audit/converge-2026-07-08/D-R1/verify-coord-api.txt).

### A.4 — The admin dashboard renders the same live data (and never fakes it)

The admin surface forwards the operator's bearer to coord-api and renders real
rows. Unwired panels show an explicit **"Unavailable"** — never a fabricated
number (the de-scaffolding contract).

```console
# no token → 302 to SSO (gated by design, not an outage)
$ curl -sD - -o /dev/null https://r1-demo-admin-927350204262.us-central1.run.app/
HTTP/2 302
location: https://r1-demo-coord-api-927350204262.us-central1.run.app/v1/auth/sso/start

# with an operator token → the dashboard, and /sessions shows the live row
$ curl https://.../sessions -H "Authorization: Bearer $OP_JWT"   # HTTP 200
...renders: demo-daemon-1 · sess-synthetic-001 · /synthetic/demo-repo · active...
```

Full transcript: [`audit/converge-2026-07-08/D-R1/verify-admin.txt`](../audit/converge-2026-07-08/D-R1/verify-admin.txt).

---

## Part B — the playable POC: `honest.yaml` + Claude Code hooks

This is the "play with it in a tool you already use" demo: a developer runs an
agent (Claude Code, or R1's own runtime) against a **synthetic repo/task**, and
R1's hooks turn the session into a signed, anchored, verifiable record. Each
step below maps to a shipped seam in this repo; the seam CLI output shown is
real (fresh build, `r1 --version` → `dev+9103efd5c6e9`).

### B.1 — SessionStart: identity + IKL seeds (`SeedResolver`, §3.1)

The runtime prepends tiered seed context (role → domain → task) to the system
prompt — it **augments, never replaces** what the runtime already builds. The
mechanism is `internal/seed.FileSeedResolver` (prepend site
`internal/engine/native_runner.go`), selected at run time:

```console
$ r1 sow "<task>" --seed-profile EXAMPLE --seeds-path seeds --dump-task-prompts
# writes .stoke/prompt-dump/ with the assembled system prompt (role+domain+task
# from seeds/EXAMPLE/ prepended) and exits WITHOUT calling the LLM.
```

The example profile ships at [`seeds/EXAMPLE/`](../seeds/EXAMPLE/)
(`role.md` T0 → `domain.md` T1 → `task.md` T2; real corpora are gitignored).
Assembly order, missing-tier skip, and "original prompt preserved" are covered
by the package unit tests.

### B.2 — PreToolUse: enforce the signed action spec + record (`ActionAuthorizer`, §3.5)

Before a tool runs, the signed-allowlist authorizer (loaded fail-closed from the
seed profile's `action-spec.json` + pubkey) can **reject before execute**, at
the native tool boundary (`internal/authz`, wired in `native_runner.go` after
the rule + C3 throttle gates). The decision is an anchorable canonical record.
Deny / require-approval / allow paths and a tampered-spec rejection are covered
by the seam tests.

### B.3 — SessionEnd → receipt → per-worker accounting (`§3.2`)

The session's turns/cost/receipts/pass-fail roll up into a real receipt table
straight from the local ledger — no model needed:

```console
$ r1 receipt stats
WORKER                     TASKS  TURNS  COST_USD  RECEIPTS  PASS  FAIL
S1                         2      0      0.0000    0         1     1
compliance-repair-round-1  6      0      0.0000    0         2     4
TOTAL                      8      0      0.0000    0         3     5
```

### B.4 — Anchor + verify the receipt (`Anchorer`, §3.4)

The session receipt is anchored (trusted-timestamp default via the Go
`honest-crypto` port) and verified through the same seam — a canonical record
plus an inclusion proof, checkable by anyone without trusting R1:

```console
$ r1 verify --anchor --record <record.json> --proof <proof.json>
# verifies the record envelope hash + inclusion under the anchor proof;
# exits non-zero on a tampered record.
```

The golden-vector conformance test (`internal/honestcrypto`) pins the canonical
record hash, and a swap-proof test proves a stronger anchor backend verifies
through the identical CLI path.

### B.5 — The one BLOCKED step

Actually *running* the agent end-to-end (the model turns between SessionStart
and SessionEnd) calls a model provider and therefore needs an API key:

> **BLOCKED(provider-key)** — a live agent run that produces new receipts needs
> a provider API key (or the hosted RelayGate inference path). This is an
> accepted external dependency. Every **governance and verification** surface
> above — the hosted fleet view, the receipt table, the anchor verify — is
> demonstrated **without** a key. The POC's model turns are the only part that
> needs one.

---

## Demo caveats (stated, not hidden)

- **Shared blast radius.** The demo shares one JWT secret across the two
  surfaces and one Cloud SQL instance across the whole portfolio. Fine for a
  demo, **not** a production posture — see [`docs/EU-EXPANSION.md`](EU-EXPANSION.md)
  for how a real (e.g. EU) deployment localizes keys and state.
- **Scale-to-zero.** `--min-instances=0`: the first request after idle pays a
  cold start (a few hundred ms for these Go binaries).
- **Synthetic only.** Every session, repo, and identity shown here is synthetic.
- **Stateless surfaces.** The active-session registry is in-memory with a 300s
  TTL; a restart simply waits one heartbeat interval. No DB is attached to the
  hosted footprint, by design.

## Reproduce

```bash
# health + version (public)
curl https://r1-demo-coord-api-927350204262.us-central1.run.app/livez
curl https://r1-demo-coord-api-927350204262.us-central1.run.app/v1/version
# the gate (expect 401)
curl https://r1-demo-coord-api-927350204262.us-central1.run.app/v1/sessions
# local seams (fresh build of this branch)
r1 receipt stats
r1 verify --anchor --record <record.json> --proof <proof.json>
```

Live-verification transcripts and the seam-verb capture are archived under
[`audit/converge-2026-07-08/D-R1/`](../audit/converge-2026-07-08/D-R1/).
