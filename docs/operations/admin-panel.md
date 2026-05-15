# Admin Panel — Operator Runbook

`admin.r1.run` is R1's hosted admin surface. During the 2026-05-15
audit, the live public surface was the separate `r1-admin` Cloud Run
service, not a sub-path on `r1-server`, and much of the page content
was still placeholder scaffold. Treat this document as the target
operator runbook plus current wiring notes, not proof that the hosted
admin surface is fully complete.

Spec: [`specs/admin-panel.md`](../../specs/admin-panel.md).

## Sign-in flow

1. User navigates to `https://admin.r1.run/admin`.
2. The intended design is bearer JWT or `__Host-r1_at` cookie auth
   via the A4 SSO flow. The current hosted `r1-admin` service still
   has partial auth wiring, so verify the live behavior before relying
   on this section as an exact production contract.
3. Missing/expired token → HTTP 302 to
   `/auth/sso/start?next=/admin/<path>`. The SSO start handler
   bounces through the IdP and lands the user back on the originally
   requested path after sign-in.
4. Valid JWT without `role=admin` → HTTP 403 with body
   `{"error":"forbidden","reason":"admin_role_required"}` (JSON path)
   or plain text `"403 forbidden: admin role required"` (HTML path).
5. Valid admin → handler runs. An `AdminViewed` ledger node is
   emitted asynchronously (path, user, tenant, redacted /24 remote
   addr, timestamp).

## Granting / revoking the admin role

The admin role lives in the A4 RelayOne SSO IdP's roles claim. There
is no R1-side role assignment. To grant:

1. Sign in to the RelayOne IdP console as a directory administrator.
2. Locate the user. Add `admin` to their `roles` claim.
3. The user's next refreshed access token (re-login or refresh) will
   carry the role.

To revoke: remove `admin` from the user's roles. Existing access
tokens carry the previous claim until they expire (15-minute default
TTL on the access cookie); for immediate revocation, additionally
invalidate the refresh cookie via the A4 logout endpoint.

The middleware compares roles with constant-time equality so a
specific role can't be inferred from response timing.

## Audit trail

Every admin page view emits an `AdminViewed` ledger node
asynchronously (`internal/ledger/nodes/admin_viewed.go`). Fields:

| Field | Value |
|---|---|
| `Path` | e.g. `/admin/sessions` |
| `User` | SSO `sub` claim (email) |
| `Tenant` | `tenant_id` claim |
| `Timestamp` | server clock, UTC |
| `RemoteAddr` | redacted to /24 (IPv4) or /48 (IPv6) |

Failed authentications (401/302) do NOT emit `AdminViewed` — we
don't log unauthorized scans against the ledger. To browse the audit
trail, sign in as admin and visit `/admin/audit` filtered by
`?type=admin_viewed`.

## Cloud Run domain mapping

`admin.r1.run` is a Cloud Run domain mapping on the hosted
`r1-admin` service. Current provisioning shape:

```bash
gcloud beta run domain-mappings create \
  --service=r1-admin-prod \
  --domain=admin.r1.run \
  --region=us-central1 \
  --project=relayone-488319
```

The mapping is idempotent — re-running is safe. Verify with:

```bash
gcloud beta run domain-mappings describe admin.r1.run \
  --region=us-central1 \
  --project=relayone-488319
```

## Cloudflare DNS

The Cloudflare CNAME for `admin.r1.run` must be **DNS-only** (proxy
OFF — Cloud Run terminates TLS itself). This record was already
present during the 2026-05-15 audit:

```
admin.r1.run.   CNAME   ghs.googlehosted.com.
```

If the proxy is ON, Cloudflare attempts to terminate TLS with its own
cert and the Cloud Run-issued cert is bypassed; clients see a
certificate-name mismatch.

The `r1.run` zone's ownership is already verified in the Google Site
Verification console.

## Phase-2 button reference

Phase 1 is read-only. The templates render Phase-2 mutation surfaces
as `<button disabled aria-disabled="true" title="Phase 2 — Q3 2026">`
so testers can see where future mutations will land:

| Page | Disabled buttons (Phase 2) |
|---|---|
| `/admin/tenants` | "Onboard Tenant" |
| `/admin/tenants/<slug>` | "Suspend Tenant", "Revoke Admin", "Edit Quota" |
| `/admin/sessions/<id>` | "Terminate Session", "Force Drain" |
| `/admin/billing` | "Adjust Cap" |

## Troubleshooting

**`503 admin auth not configured`** — the middleware was wired
without a JWT verifier. Confirm `A4_SSO_JWKS_URL` is set and
non-empty in the `r1-server` environment, and that
`auth.NewJwtService` ran successfully at startup. The probe is
intentionally fail-closed: missing config returns 503 rather than
silently accepting requests.

**`302` loop between `/admin` and `/auth/sso/start`** — the SSO
callback isn't depositing the access cookie correctly. Check the
IdP's redirect URI matches the deployed callback path
(`https://admin.r1.run/auth/sso/callback`), and that the cookie is
marked `Secure; SameSite=Lax`. The `__Host-` prefix requires
`Secure; Path=/` with no `Domain=` attribute; if any of those is
violated, the browser silently drops the cookie.

**`403 admin role required` for a user who *should* have admin** —
the user's access token hasn't refreshed yet. Have them log out + in
to mint a new token, or hit `/auth/refresh` which rotates the access
cookie from the refresh cookie.

**JWKS unavailable** — the middleware fetches the JWKS document at
startup and refreshes on `kid` miss (cache TTL 24h). If the IdP is
unreachable at request time AND the cache doesn't carry the needed
key, requests return 503. Cache state is process-local; restarting
`r1-admin` clears it.

**Performance degraded with >10k sessions** — the current hosted admin
surface still reads
`sessionhub.List()` in-memory; at large session counts, switch the
list page to pagination-by-cursor via a future Phase-1.5 follow-up.
Track in `plans/admin-perf-followups.md`.

## Dev bypass

For local iteration without spinning up a real IdP, set
`R1_ADMIN_DEV_BYPASS=1` in the dev environment. The middleware
synthesizes a default admin principal (`dev@local` / tenant `dev`)
and skips JWT verification.

**Production deployments MUST leave `R1_ADMIN_DEV_BYPASS` unset.**
The middleware reads the value as exact string `"1"` — anything else
(`true`, `yes`, empty, unset) keeps the bypass disabled. The wiring
in `cmd/r1-server/main.go` additionally refuses to enable the bypass
when `R1_ENV=production`.

## Screenshots

(placeholder — fill in once the templates render against staging
data with realistic session/tenant counts)

## Spec reference

`specs/admin-panel.md` §6 (auth contract), §7 (Cloud Run mapping),
§9 (acceptance criteria), §10 (implementation checklist).
