# RelayOne SSO + JWT — Operator Runbook

This document is the operator-facing reference for the
`internal/auth` package shipped under spec
[`specs/relayone-sso.md`](../../specs/relayone-sso.md). It mirrors the
TypeScript `@relayone/auth-core` primitives — `JwtService` and
`RelayOneSsoClient` — and is the canonical reference for wiring the
R1 daemon as an OIDC Relying Party.

If you are a Go engineer and want the package-internal API, see the
`doc.go` of `internal/auth` (`go doc github.com/RelayOne/r1/internal/auth`).

---

## 1. Registering R1 as an OIDC client at RelayOne

R1 is an OIDC Relying Party against the RelayOne control plane
(`https://auth.relayone.io`). To enroll a R1 deployment:

1. Get a RelayOne admin token from the control-plane console.
2. POST to the admin endpoint:

   ```
   POST https://auth.relayone.io/v1/oauth/clients
   Authorization: Bearer <admin-token>
   Content-Type: application/json

   {
     "name": "r1-prod-cluster-east",
     "redirect_uris": ["https://r1.example.com/auth/sso/callback"],
     "grant_types": ["authorization_code", "refresh_token"],
     "response_types": ["code"],
     "scope": "openid email profile"
   }
   ```

3. Capture `client_id` and `client_secret` from the response.
4. The redirect URI MUST exactly match `https://<r1-host>/auth/sso/callback`
   — RelayOne validates redirect URIs strictly per RFC 6749 §3.1.2.2.

For local development, register `http://localhost:3948/auth/sso/callback`
as a second redirect URI on the same client.

## 2. Environment variables

The R1 daemon reads its auth config from environment variables. The
table below is the contract — name, type, default, fallback.

### JwtService (`internal/auth/jwt.go`)

| Variable                    | Required | Type     | Default | Notes                                                            |
|-----------------------------|----------|----------|---------|------------------------------------------------------------------|
| `AUTH_JWT_ISSUER`           | yes      | string   | -       | `iss` claim. e.g. `https://r1.example.com`                       |
| `AUTH_JWT_AUDIENCE`         | yes      | string   | -       | `aud` claim. Usually the daemon's role (e.g. `r1-daemon-prod`)   |
| `AUTH_JWT_SECRET`           | HS256    | string   | -       | >=32 chars. Required when not running RS256.                     |
| `AUTH_JWT_PUBLIC_KEY`       | RS256    | PEM      | -       | SPKI PEM. Presence triggers RS256 mode.                          |
| `AUTH_JWT_PRIVATE_KEY`      | RS256    | PEM      | -       | PKCS8 PEM. Required when `AUTH_JWT_PUBLIC_KEY` is set.           |
| `AUTH_JWT_ACCESS_TTL_SEC`   | no       | int      | 900     | Access-token lifetime (15 min default).                          |
| `AUTH_JWT_REFRESH_TTL_SEC`  | no       | int      | 2592000 | Refresh-token lifetime (30 days default).                        |

### SsoClient (`internal/auth/sso_client.go`)

| Variable                       | Required | Type   | Default                    | Notes                                                                    |
|--------------------------------|----------|--------|----------------------------|--------------------------------------------------------------------------|
| `RELAYONE_SSO_CLIENT_ID`       | yes*     | string | -                          | From step 1.                                                             |
| `RELAYONE_SSO_CLIENT_SECRET`   | yes*     | string | -                          | From step 1.                                                             |
| `RELAYONE_SSO_ISSUER`          | yes*     | URL    | (fallback `RELAYONE_SSO_URL`) | RelayOne issuer base. Include the `/v1` suffix.                       |
| `RELAYONE_SSO_REDIRECT_URI`    | yes*     | URL    | -                          | Must match the registered redirect URI.                                  |

* All four must be set together. Missing any yields degraded mode:
SSO routes return 404 and JWT-only flows (e.g. cmd/r1-admin) continue
to work. This mirrors the TS `relayOneSsoClientFromEnv` contract.

### Daemon-wide mode switch

| Variable          | Type   | Default     | Values                          |
|-------------------|--------|-------------|---------------------------------|
| `R1_AUTH_MODE`    | string | `anonymous` | `anonymous` \| `sso` \| `both`  |

- `anonymous` — Existing loopback bearer auth only. SSO routes
  return 404. Zero behavioral change from pre-A4.
- `sso` — SSO routes mounted. Non-loopback callers MUST present a
  verified JWT (cookie or `Authorization: Bearer`).
- `both` — SSO routes mounted; the loopback bearer is ALSO accepted
  for migration. Document as transitional.

## 3. HTTP routes

When `R1_AUTH_MODE=sso` (or `both`), four routes are mounted on the
main daemon mux:

| Method | Path                   | Body / Query                         | Returns                                                                  |
|--------|------------------------|--------------------------------------|--------------------------------------------------------------------------|
| GET    | `/auth/sso/start`      | `?next=<path>`                       | 302 to IdP authorize URL; sets `__Host-r1_sso_state` cookie              |
| GET    | `/auth/sso/callback`   | `?code=&state=`                      | 302 to `next`; sets `__Host-r1_at` + `__Host-r1_rt` cookies              |
| POST   | `/auth/refresh`        | cookie `__Host-r1_rt`                | 200 + new `__Host-r1_at` cookie; JSON `{access_token, expires_at}`       |
| POST   | `/auth/logout`         | cookie `__Host-r1_at` (optional)     | 200 `{"logged_out":true}` (or 302 to IdP end_session if available)        |

### Cookie discipline

All three cookies use the `__Host-` prefix per the OWASP guidance —
`Secure`, `HttpOnly`, `SameSite=Lax`, no `Domain`, `Path=/` (state +
access) or `Path=/auth` (refresh). Browsers will silently drop a
`__Host-` cookie that doesn't satisfy all four rules, so a successful
login implicitly verifies that the daemon is reachable via HTTPS.

For local development under plain HTTP, set
`AUTH_COOKIE_INSECURE_FOR_TESTS=1` (read by the `SsoConfig` constructor;
NEVER in production).

## 4. Key rotation

### RS256 (recommended for multi-tenant)

1. Generate a new keypair:

   ```bash
   openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 \
     -outform PEM -out ~/.r1/keys/jwt-priv.pem.new
   openssl rsa -pubout -in ~/.r1/keys/jwt-priv.pem.new \
     -outform PEM -out ~/.r1/keys/jwt-pub.pem.new
   ```

2. Drop the new key into the daemon's keys directory:

   ```bash
   mv ~/.r1/keys/jwt-priv.pem ~/.r1/keys/jwt-priv.pem.old
   mv ~/.r1/keys/jwt-pub.pem  ~/.r1/keys/jwt-pub.pem.old
   mv ~/.r1/keys/jwt-priv.pem.new ~/.r1/keys/jwt-priv.pem
   mv ~/.r1/keys/jwt-pub.pem.new  ~/.r1/keys/jwt-pub.pem
   ```

3. SIGHUP the daemon — the file-source re-reads on reload.
4. Wait one `AUTH_JWT_ACCESS_TTL_SEC` window (default 15 minutes) so
   any in-flight access tokens expire under the old key.
5. Delete the `.old` files.

### HS256 (single-tenant)

Deploy a new `AUTH_JWT_SECRET`. All existing tokens expire after one
access-TTL window. Refresh tokens minted under the old secret will
fail to validate immediately — clients receive a 401 and re-login.

## 5. Per-tenant isolation

R1 supports per-tenant isolation in three layers:

- **Sessions:** `internal/server/sessionhub.Session` has `TenantID`,
  `Subject`, `Roles` fields populated by the SSO callback. Empty
  `TenantID` means "anonymous / shared/legacy" — preserves existing
  CLI workflows.
- **Bus events:** `internal/bus.Event.TenantID` carries the originating
  tenant. Subscribers can filter via `bus.Pattern{TenantID: "..."}`;
  the default empty filter sees everything (system events bypass the
  filter). Events with empty `TenantID` ALWAYS pass to every
  subscriber — that's the "shared/legacy" bucket.
- **Ledger / cloud storage:** Tenant tagging propagates via the
  `tenant_id` field on every emitted event and is consumed by the
  observability layer. See `specs/relayone-sso.md` Phase F items 29-31
  for the migration plan.

### Verifying isolation

```bash
# Filter ledger events by tenant
curl -H "Authorization: Bearer $TOKEN" \
  http://localhost:3948/api/events?tenant_id=ten-A | jq

# List sessions for one tenant
curl -H "Authorization: Bearer $TOKEN" \
  http://localhost:3948/api/sessions?tenant_id=ten-A | jq
```

A session created with `tenant_id=ten-A` cannot see ledger nodes
inserted with `tenant_id=ten-B`. Test coverage:
`TestPerTenantLedgerIsolation` in `internal/ledger/store_test.go`.

## 6. Troubleshooting

### "issuer mismatch" on every token

Most common cause: `RELAYONE_SSO_ISSUER` is missing the `/v1` suffix.
The RelayOne control plane mounts OAuth endpoints under `/v1/oauth/...`,
so the issuer URL must include `/v1`:

```
RELAYONE_SSO_ISSUER=https://auth.relayone.io/v1  # correct
RELAYONE_SSO_ISSUER=https://auth.relayone.io     # WRONG — no /v1
```

### "clock skew" verification failures

Both jwx and go-oidc accept a default 60-second skew. To widen on the
R1 side, set `AUTH_JWT_CLOCK_SKEW_SEC` (future config knob — for now,
patch `internal/auth/jwt.go`'s `jwt.Parse` options).

### RP-Initiated Logout returns 404

The RelayOne discovery doc does not currently expose an
`end_session_endpoint`. R1's logout handler falls back to clearing
local cookies and returning 200 — the user is effectively logged out
on the R1 side. The open RelayOne ticket for adding this endpoint is
tracked in `specs/relayone-sso.md` §10.

### Refresh returns 401 immediately after callback

Check that the refresh cookie's `Path` is `/auth` and that the daemon
is being called at a path starting with `/auth/` — browsers send the
refresh cookie only to `/auth/*`. If the daemon's reverse proxy
rewrites paths, the refresh cookie path must be updated to match the
exposed prefix.

### `__Host-` cookies disappearing

Browsers silently drop `__Host-` cookies that don't satisfy:

- `Secure` attribute set
- No `Domain` attribute
- `Path=/`

If the daemon is fronted by a non-HTTPS reverse proxy, the
`Secure` attribute prevents the cookie from being sent. Either run
the daemon behind TLS (recommended) or use a different prefix.
For tests under plain HTTP, set `CookieInsecureForTests=true` on the
`SsoConfig`.

## 7. Cross-language interop testing

The `internal/auth/interop_test.go` family verifies cross-language
parity with `@relayone/auth-core`:

```bash
# Run the basic Go-to-Go interop coverage (always).
go test ./internal/auth/

# Run the real TS-fixture interop (requires auth-core checked out).
R1_TEST_AUTH_INTEROP=1 go test ./internal/auth/ -run Interop

# Emit a Go-side artifact for the optional Node verification step.
R1_TEST_EMIT_FIXTURE=1 go test ./internal/auth/ -run EmitsValidCompactJWS

# Verify the emitted artifact in the TS side (run from auth-core repo).
cd /home/eric/repos/auth-core && make interop-verify-ts
```

The TS-fixture loader (`authCoreFixtureDir`) honors
`AUTH_CORE_FIXTURE_DIR` for non-default checkouts. Missing fixtures
cause the test to `t.Skip` rather than fail.

## 8. Migration playbook (anonymous → sso)

1. Roll the daemon with `R1_AUTH_MODE=anonymous` (no change).
2. Deploy with `R1_AUTH_MODE=both` and the SSO env vars set. SSO
   routes are now reachable; the loopback bearer also still works
   for `cmd/r1` CLI usage.
3. Migrate web UI to log in via `/auth/sso/start`. Browser sessions
   now carry the `__Host-r1_at` cookie automatically.
4. Once all browser traffic is on SSO, flip to `R1_AUTH_MODE=sso`.
   `cmd/r1` still talks via loopback bearer (the loopback gate
   bypasses the JWT requirement for `127.0.0.1`).
5. Roll back: change `R1_AUTH_MODE=anonymous` and restart.

Steps 2-3 can take days or weeks; the daemon supports either mode
indefinitely.
