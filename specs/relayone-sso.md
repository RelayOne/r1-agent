<!-- STATUS: ready -->
<!-- CREATED: 2026-05-11 -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 36 -->

# RelayOne SSO + JWT Auth — Go Port of `@relayone/auth-core`

## 1. Overview

This spec brings the R1 daemon to feature parity with the TypeScript `@relayone/auth-core` package (`/home/eric/repos/auth-core`) for two of its primitives: `JwtService` (HS256 + RS256 sign / verify / refresh) and `RelayOneSsoClient` (OIDC authorization-code-with-PKCE flow against the RelayOne control-plane at `https://auth.relayone.io`). It also wires those primitives into R1's existing HTTP surface so that the daemon can act as an OIDC Relying Party that accepts SSO logins, issues R1-internal JWTs, isolates sessions per tenant, and signs out via RP-Initiated Logout.

The constraint that drives every design decision in this spec is **cross-language token interop**. A JWT minted by the TypeScript service (in any RelayOne app — RelayGate, Stoke, ParentProof, etc.) must validate in Go, and vice versa, for both HS256 and RS256. That means: matching `iss` / `aud` / `kid` semantics exactly, matching the `token_use: "access" | "refresh"` discriminator, matching the `tenant_id` / `roles` / `session_id` custom claim names, and matching key material formats (HS256 secret as raw UTF-8 bytes; RS256 PKCS8 private + SPKI public PEM). The TypeScript implementation uses `jose@5.9.6`; the Go side will use `github.com/lestrrat-go/jwx/v2 v2.1.6` (sister project to `jose`, JOSE-spec-complete, actively maintained as of 2026-Q2). For the OIDC client, we use `github.com/coreos/go-oidc/v3 v3.14.x` plus `golang.org/x/oauth2 v0.30.x` — `go-oidc` handles discovery + JWKS cache + ID-token verify; `oauth2` handles the PKCE-aware authorization-code dance via `oauth2.S256ChallengeOption` and `oauth2.VerifierOption` (PKCE landed in `x/oauth2 v0.20+`).

The package lands under `internal/auth/` in `r1-agent` and is wired by `cmd/r1-server/main.go`. It exposes four HTTP routes (`/auth/sso/start`, `/auth/sso/callback`, `/auth/refresh`, `/auth/logout`), introduces a `TenantID` field on `internal/server/sessionhub.Session`, and adds an `auth.mode: anonymous | sso` config switch so existing local-CLI flows (no auth, loopback-only) keep working. Per-tenant isolation extends to the ledger (every node gets a `tenant_id`), the in-memory bus (every event gets a `tenant_id`), and the WAL/event-log (every row gets a `tenant_id` column with an index).

## 2. Why a Go port (not gRPC to the TS service)

The TS `auth-core` package is consumed by Node-side RelayOne apps. R1 is a Go binary that ships standalone — to a dev's laptop, to a CI runner, to a customer-managed Kubernetes cluster — and cannot depend on a Node sidecar at runtime. A Go port of the two primitives we need (`JwtService` + `RelayOneSsoClient`) is ~600 LOC and gives us:

- **Zero added runtime deps** for self-hosted users. The R1 binary already speaks HTTPS; it just needs a JWT lib and an OIDC lib, both of which compile in.
- **Cross-language interop** at the JWT layer. A user who logs into RelayGate (TS) and receives an access token can pass that token to R1 (Go) and have it validate, because both sides target the same JOSE primitives. Same in reverse: R1-issued tokens can be introspected by Node services via the existing `relayone-introspect-client.ts`.
- **Symmetry with the existing `internal/ledger/redact_sign.go`** ed25519 patterns — R1 already manages on-disk PEM keys for the redaction signer; the JWT keystore reuses that file-mode discipline (0600 priv, 0644 pub).

## 3. Stack & versions

- **Go 1.25.5** (matches `go.mod`).
- **`github.com/lestrrat-go/jwx/v2 v2.1.6`** — JOSE-spec-complete; supports HS256/RS256/EdDSA; `jwk.Set` + `jwk.Cache` for remote JWKS; matches the surface area of TS `jose@5.9.6`. We pin v2 (not v3 alpha) — v2 has been stable since 2023-Q3 and is what most Go OIDC stacks consume today.
- **`github.com/coreos/go-oidc/v3 v3.14.0`** — OIDC discovery, ID-token verify against remote JWKS, claims extraction. Maintained by CoreOS/Red Hat, used in production by Dex and Kubernetes. v3 added Go 1.21+ context-aware JWKS refresh.
- **`golang.org/x/oauth2 v0.30.0`** — authorization-code flow with PKCE (`oauth2.S256ChallengeOption(verifier)` + `oauth2.VerifierOption(verifier)`, added in v0.20). Token refresh, RP-Initiated Logout (manual `end_session_endpoint` GET — neither library wraps this; we implement directly).
- **`golang.org/x/crypto/hkdf`** — HKDF-SHA256 for per-tenant key derivation (no third-party dep needed).
- **Test mocks:** `net/http/httptest` (standard library) for a fake IdP serving `/.well-known/openid-configuration`, `/.well-known/jwks.json`, `/oauth/authorize`, `/oauth/token`, `/oauth/userinfo`, `/oauth/end_session`.

## 4. Contract mirror — TS to Go field-by-field

The Go port mirrors `auth-core/src/jwt-service.ts` (lines 1–208) and `auth-core/src/relayone-sso-client.ts` (lines 1–89) function-for-function. Names are Go-idiomatic (PascalCase exported; receiver methods on a single struct), but the underlying claim names + wire formats are identical:

| TS                                              | Go                                                              | Notes                                                                                                          |
|-------------------------------------------------|-----------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------|
| `JwtService.sign(payload, opts)`                | `(*JwtService).Sign(payload map[string]any, opts SignOptions)`  | Returns `(token string, err error)`. Sets `iat`, `exp`, `iss`, `aud`; carries `sub` via `opts.Subject`.        |
| `JwtService.verify<T>(token)`                   | `(*JwtService).Verify(token string)`                            | Returns `(VerifiedToken, error)`. Strict iss/aud check. Algorithms slice = `[s.algorithm]`.                    |
| `JwtService.issuePair(payload, subject)`        | `(*JwtService).IssuePair(payload, subject)`                     | Returns `SignedTokenPair{AccessToken, RefreshToken, AccessExpiresAt, RefreshExpiresAt}`.                       |
| `JwtService.refreshAccess(refresh)`             | `(*JwtService).RefreshAccess(refresh string)`                   | Verifies `token_use=="refresh"`, mints a new access. Does NOT rotate refresh (caller's choice).                |
| `jwtServiceFromEnv(env)`                        | `JwtServiceFromEnv(getenv func(string) string)`                 | Same env var names: `AUTH_JWT_ISSUER`, `_AUDIENCE`, `_SECRET`, `_PUBLIC_KEY`, `_PRIVATE_KEY`, `_*_TTL_SEC`.    |
| `JwtVerificationError`                          | `var ErrJwtVerification = errors.New("jwt_verification_failed")`| Wrap with `fmt.Errorf("%w: %v", ErrJwtVerification, cause)` so callers `errors.Is`.                            |
| `RelayOneSsoClient.getEndpoints()`              | `(*SsoClient).Endpoints(ctx) (Endpoints, error)`                | Pinned: `{issuer}/oauth/authorize`, `/oauth/token`, `/oauth/userinfo`, `/.well-known/jwks.json`.                |
| `OAuthClientBase.buildAuthorizeUrl(input)`      | `(*SsoClient).BuildAuthorizeURL(input AuthorizeInput) (string, error)` | PKCE S256; query: `client_id`, `redirect_uri`, `response_type=code`, `scope`, `state`, `code_challenge`, `code_challenge_method=S256`, optional `nonce`. |
| `OAuthClientBase.exchangeCode(code, verifier)`  | `(*SsoClient).ExchangeCode(ctx, code, verifier) (Tokens, error)`| POST `application/x-www-form-urlencoded` to `/oauth/token`.                                                    |
| `OAuthClientBase.verifyIdToken(idToken, nonce)` | `(*SsoClient).VerifyIDToken(ctx, idToken, nonce) (Claims, error)` | Uses `go-oidc.IDTokenVerifier` against cached JWKS.                                                            |
| `OAuthClientBase.fetchUserInfo(at)`             | `(*SsoClient).FetchUserInfo(ctx, accessToken) (map[string]any, error)` | GET with `Authorization: Bearer <at>`.                                                                         |
| `normalizeRelayOneProfile(claims)`              | `(*SsoClient).NormalizeProfile(claims map[string]any) RelayOneProfile` | Lifts `relayone_user_id`, `relayone_org_id`, `msp_org_id`, `msp_managed_orgs`.                                  |
| `relayOneSsoClientFromEnv(env)`                 | `SsoClientFromEnv(getenv) (*SsoClient, error)`                  | Returns `(nil, nil)` when not configured (degraded mode — matches TS).                                          |

Cross-interop test (item 28 below) asserts these match byte-for-byte by feeding a token issued by the TS test fixture into the Go verifier and a token issued by the Go service into a `jose`-based Node verifier.

## 5. Discovery doc — pinned but cached

Per `auth-core/src/relayone-sso-client.ts` lines 33–44, endpoints are pinned (constructed from `issuer + "/oauth/..."` paths) rather than discovered dynamically. We mirror that: `SsoClient.Endpoints()` returns the pinned struct without a network round-trip. The RelayOne control plane DOES serve `/v1/.well-known/openid-configuration` (verified in `/home/eric/repos/RelayOne/apps/control-plane/src/modules/auth/oidc-provider.service.ts` `getDiscoveryDocument()`) with this exact shape:

```json
{
  "issuer": "https://auth.relayone.io",
  "authorization_endpoint": ".../v1/oauth/authorize",
  "token_endpoint": ".../v1/oauth/token",
  "userinfo_endpoint": ".../v1/oauth/userinfo",
  "jwks_uri": ".../v1/.well-known/jwks.json",
  "introspection_endpoint": ".../v1/oauth/introspect",
  "revocation_endpoint": ".../v1/oauth/revoke",
  "code_challenge_methods_supported": ["S256"],
  "id_token_signing_alg_values_supported": ["EdDSA", "RS256"]
}
```

So we fetch it once on first use (for the `end_session_endpoint` if present, and as a sanity check that pinned endpoints match), cache for 1 hour with a 4-hour stale-while-revalidate window (matches the RelayOne JWKS `Cache-Control` header on `/v1/.well-known/jwks.json`), and fall back to pinned endpoints if discovery fails. The JWKS cache is owned by `go-oidc.KeySet`, which handles refresh + retry internally.

## 6. Implementation checklist

### Phase A — Library deps + keystore

1. [ ] Add to `go.mod`: `github.com/lestrrat-go/jwx/v2 v2.1.6`, `github.com/coreos/go-oidc/v3 v3.14.0`, `golang.org/x/oauth2 v0.30.0`. Run `go mod tidy`. Verify no transitive collision with the existing `github.com/dvsekhvalnov/jose2go v1.5.0` already in `go.sum` (that lib is used elsewhere — leave it alone, do NOT replace).

2. [ ] Create `internal/auth/doc.go` — package comment block describing the package's role, the TS contract it mirrors, the two algorithm modes (HS256 single-tenant, RS256 multi-tenant), and the env-var contract. Link to `auth-core/README.md` env-var table.

3. [ ] Create `internal/auth/keys.go`:
   - Type `KeyMaterial struct { Algorithm string; HMACSecret []byte; RSAPrivate *rsa.PrivateKey; RSAPublic *rsa.PublicKey; KID string }`.
   - `func LoadKeyMaterial(ctx context.Context, src KeySource) (*KeyMaterial, error)` where `KeySource` is an interface with three concrete impls (priority order): `SecretManagerSource` (calls `internal/cloud` if a Secret Manager client is configured; on import failure of GCP libs, returns `ErrUnsupported`), `EnvSource{Getenv func(string) string}`, `FileSource{Dir string}` (defaults to `~/.r1/keys/`, mirrors `internal/ledger/redact_sign.go` LoadOrGenerateSigningKey file-mode discipline: 0600 priv, 0644 pub, 0755 dir).
   - Files read: `jwt-priv.pem` (PKCS8 RSA private), `jwt-pub.pem` (SPKI RSA public), `jwt-secret` (raw bytes, >= 32 chars for HS256).
   - First load with no material on disk in FileSource mode: generate a fresh RSA-2048 keypair, derive a 6-byte SHA-256 fingerprint as `KID`, persist both halves. Match the pattern in `redact_sign.go` `LoadOrGenerateSigningKey` exactly.

4. [ ] Add `internal/auth/keys_tenant.go`:
   - `func DeriveTenantHMACSecret(rootSecret []byte, tenantID string) []byte` — HKDF-SHA256 with `info = "r1-jwt-tenant-v1:" + tenantID`, output 32 bytes. Used in HS256 multi-tenant deployments where a single root secret fan-outs to per-tenant secrets.
   - `func DeriveTenantKID(rootKID string, tenantID string) string` — `kid = rootKID + "-t-" + sha256(tenantID)[:6]`. Must match the TS reference (TS does this same derivation in `auth-core` `session-store.ts` consumer paths — confirm the literal `"r1-jwt-tenant-v1:"` HKDF info string with the TS team before merge; if it differs, change BOTH sides in one PR).

### Phase B — JwtService

5. [ ] Create `internal/auth/jwt.go`:
   - Type `JwtService` with private fields: `issuer, audience string; algorithm jwa.SignatureAlgorithm; signKey, verifyKey jwk.Key; accessTTL, refreshTTL time.Duration`.
   - `func NewJwtService(opts JwtServiceOptions) (*JwtService, error)` validates: HS256 requires `SigningKey` (>= 32 bytes); RS256 requires both `PrivateKeyPEM` and `PublicKeyPEM`. Imports private via `jwk.ParseKey(pem, jwk.WithPEM(true))`; public similarly. Set `kid` header on the key.
   - Defaults: access TTL 15m, refresh TTL 30d (matches TS constants).

6. [ ] Implement `(*JwtService).Sign(payload map[string]any, opts SignOptions) (string, error)`:
   - Build `jwt.Token` via `jwt.New()`. Set standard: `iss`, `aud`, `iat=now`, `exp=now+ttl`, `sub=opts.Subject`, `jti=uuid.NewString()`. Copy custom claims from `payload`.
   - Serialize with `jwt.Sign(token, jwt.WithKey(s.algorithm, s.signKey))`.
   - TTL precedence: `opts.TTL > s.accessTTL > 15m default`.

7. [ ] Implement `(*JwtService).Verify(token string) (VerifiedToken, error)`:
   - Parse with `jwt.Parse([]byte(token), jwt.WithKey(s.algorithm, s.verifyKey), jwt.WithIssuer(s.issuer), jwt.WithAudience(s.audience), jwt.WithValidate(true))`.
   - On any failure (expired, wrong iss, wrong aud, bad sig), wrap as `fmt.Errorf("%w: %v", ErrJwtVerification, err)`.
   - Return `VerifiedToken{Payload map[string]any, Header struct{Alg, Typ, KID string}}`.

8. [ ] Implement `(*JwtService).IssuePair(payload map[string]any, subject string) (SignedTokenPair, error)`:
   - Clone payload twice; set `token_use="access"` on one and `token_use="refresh"` on the other. Use TTL accessTTL / refreshTTL respectively.
   - Return `SignedTokenPair{AccessToken, RefreshToken string; AccessExpiresAt, RefreshExpiresAt time.Time}`.

9. [ ] Implement `(*JwtService).RefreshAccess(refreshToken string) (RefreshResult, error)`:
   - `Verify(refreshToken)`. If `token_use != "refresh"`, return `ErrNotRefreshToken` (matches TS `not_a_refresh_token`).
   - Strip JWT-managed claims (`iss`, `aud`, `iat`, `exp`, `sub`, `jti`, `token_use`); copy the rest; set `token_use="access"`; sign with `accessTTL`.
   - Does NOT rotate refresh (matches TS — caller decides rotation policy via `SessionStore`).

10. [ ] Implement `JwtServiceFromEnv(getenv func(string) string) (*JwtService, error)`:
    - `AUTH_JWT_ISSUER` (required), `AUTH_JWT_AUDIENCE` (required).
    - If `AUTH_JWT_PUBLIC_KEY` present, RS256 mode; require `AUTH_JWT_PRIVATE_KEY`.
    - Else HS256 mode; require `AUTH_JWT_SECRET`.
    - Optional: `AUTH_JWT_ACCESS_TTL_SEC`, `AUTH_JWT_REFRESH_TTL_SEC` (parse with `strconv.Atoi`, treat as seconds).
    - Error messages must contain the exact env var name (matches TS test `expect(...).toThrow(/AUTH_JWT_ISSUER/)`).

### Phase C — SsoClient

11. [ ] Create `internal/auth/sso_client.go`:
    - Type `SsoClient struct { issuer, clientID, clientSecret, redirectURI string; scopes []string; http *http.Client; verifier *oidc.IDTokenVerifier; provider *oidc.Provider; discoveryCache *discoveryDoc; discoveryFetchedAt time.Time }`.
    - `func NewSsoClient(opts SsoClientOptions) (*SsoClient, error)` — defaults `scopes = []string{"openid", "email", "profile"}`. Lazy-inits `provider` on first `Endpoints()` call via `oidc.NewProvider(ctx, issuer)` with a fallback to pinned endpoints if discovery 404s or times out (5s).
    - Pinned endpoints (match TS exactly): `{issuer}/oauth/authorize`, `{issuer}/oauth/token`, `{issuer}/oauth/userinfo`, `{issuer}/.well-known/jwks.json`. Note: RelayOne control-plane prepends `/v1/` to its OAuth paths; the `RELAYONE_SSO_ISSUER` env var SHOULD include the `/v1` suffix (e.g. `https://auth.relayone.io/v1`) so trailing-slash trim + `/oauth/...` concat yields the right URL. Document in `docs/integrations/relayone-sso.md` item 30.

12. [ ] Implement `(*SsoClient).BuildAuthorizeURL(input AuthorizeInput) (string, error)`:
    - PKCE: caller supplies `input.CodeVerifier` (44+ chars base64url; helper `GeneratePKCEPair() (verifier, challenge string)` available). Challenge = `base64url(sha256(verifier))`.
    - Query: `client_id`, `redirect_uri`, `response_type=code`, `scope` (space-joined), `state` (caller-supplied; we don't generate it because state semantics are app-specific), `code_challenge`, `code_challenge_method=S256`, optional `nonce`.
    - Extra params merge from `input.Extra map[string]string` (for `prompt=login` etc.).
    - Returns `endpoints.AuthorizationEndpoint + "?" + url.Values.Encode()`.

13. [ ] Implement `(*SsoClient).ExchangeCode(ctx context.Context, code, codeVerifier string) (Tokens, error)`:
    - Use `golang.org/x/oauth2.Config` with `Endpoint = oauth2.Endpoint{TokenURL: pinned, AuthStyle: oauth2.AuthStyleInParams}` (matches TS `client_id`/`client_secret` in form body, not Basic auth — per `oauth-client-base.ts` line 98–104).
    - Call `cfg.Exchange(ctx, code, oauth2.VerifierOption(codeVerifier))`. Returns `*oauth2.Token` plus optionally `id_token` in `Extra("id_token")`.
    - Map to `Tokens{AccessToken, IDToken, RefreshToken string; ExpiresIn int; TokenType string; Raw map[string]any}`.

14. [ ] Implement `(*SsoClient).VerifyIDToken(ctx context.Context, idToken, expectedNonce string) (map[string]any, error)`:
    - Use `go-oidc.Provider.Verifier(&oidc.Config{ClientID: s.clientID})` to construct an `IDTokenVerifier`.
    - `verifier.Verify(ctx, idToken)`. Extract claims via `idTok.Claims(&claims)`.
    - If `expectedNonce != ""` and `claims["nonce"] != expectedNonce`, return `ErrNonceMismatch` (matches TS `id_token_nonce_mismatch`).

15. [ ] Implement `(*SsoClient).FetchUserInfo(ctx, accessToken string) (map[string]any, error)`:
    - GET `endpoints.UserinfoEndpoint` with header `Authorization: Bearer <at>` and `Accept: application/json`.
    - On non-2xx, return `ErrUserinfoFailed` with the response body suffixed.

16. [ ] Implement `(*SsoClient).NormalizeProfile(claims map[string]any) RelayOneProfile`:
    - Mirror `relayone-sso-client.ts` lines 50–61 exactly: lift `relayone_user_id` (fallback to `sub`), `relayone_org_id`, `msp_org_id` (nullable), `msp_managed_orgs` (filter to string slice).
    - Type `RelayOneProfile struct { Sub, Email, Name string; EmailVerified bool; RelayoneUserID, RelayoneOrgID string; MspOrgID *string; MspManagedOrgs []string; Raw map[string]any }`.

17. [ ] Implement `(*SsoClient).ExchangeCodeForProfile(ctx, code, codeVerifier string) (ProfileExchangeResult, error)`:
    - Calls `ExchangeCode`, then `VerifyIDToken` (rejects with `ErrMissingIDToken` if `id_token` absent), then `FetchUserInfo`, then `NormalizeProfile(merge(idClaims, userinfo))`. Returns `{Profile RelayOneProfile, Tokens Tokens}`. Matches TS `exchangeCodeForProfile`.

18. [ ] Implement `SsoClientFromEnv(getenv func(string) string) (*SsoClient, error)`:
    - Reads `RELAYONE_SSO_CLIENT_ID`, `RELAYONE_SSO_CLIENT_SECRET`, `RELAYONE_SSO_ISSUER` (fallback `RELAYONE_SSO_URL`), `RELAYONE_SSO_REDIRECT_URI`.
    - If ANY is missing, return `(nil, nil)` (degraded mode — matches TS). If all four present, construct + return.

### Phase D — HTTP handlers

19. [ ] Create `internal/auth/state_store.go`:
    - Stores `{state, codeVerifier, nonce, next, createdAt}` for <= 10 minutes against the state cookie.
    - Two backends: `InMemoryStateStore` (sync.Map with TTL sweep; default), `RedisStateStore` (optional, gated by `R1_AUTH_STATE_REDIS_URL`). Interface: `Put(state string, entry StateEntry) error`, `Take(state string) (StateEntry, error)` (Take is one-shot — deletes after read; prevents replay).
    - State entries are bound to the client IP + UA hash (`sha256(ip + ":" + ua)[:8]`); a mismatch on callback returns 400.

20. [ ] Create `internal/auth/sso_handlers.go`:
    - `type SsoHandlers struct { Client *SsoClient; JWT *JwtService; StateStore StateStore; SessionMgr SessionManager; Config SsoConfig }`.
    - `SsoConfig{CookieDomain, RedirectAfterLoginAllowlist []string, LogoutRedirectURL string}`.
    - Constructor `NewSsoHandlers(...)` validates allowlist non-empty.

21. [ ] Implement `(h *SsoHandlers) StartHandler` for `GET /auth/sso/start?next=<path>`:
    - Validate `next` is a relative path beginning with `/` and not `//`. Reject otherwise with 400.
    - Generate `state = randHex(32)`, `verifier = randBase64URL(32)`, `nonce = randHex(16)`.
    - `BuildAuthorizeURL` with those values.
    - Persist `(state, verifier, nonce, next, ipUA-hash)` in `StateStore` (TTL 10m).
    - Set cookie `__Host-r1_sso_state=<state>; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=600`. (`__Host-` prefix requires Secure + no Domain + Path=/.)
    - 302 to the authorize URL.

22. [ ] Implement `(h *SsoHandlers) CallbackHandler` for `GET /auth/sso/callback?code=&state=`:
    - Read state cookie; compare to query `state` via `subtle.ConstantTimeCompare`. Mismatch returns 400 `state_mismatch`.
    - `StateStore.Take(state)` — must succeed (one-shot anti-replay). Returns entry with verifier, nonce, next, ipUA-hash.
    - Recompute ipUA-hash from request; mismatch returns 400 `csrf_binding_mismatch`.
    - `ExchangeCodeForProfile(ctx, code, verifier)` — on `ErrJwtVerification` / `ErrNonceMismatch` / `ErrMissingIDToken`, return 401.
    - Resolve tenant: `tenantID = profile.RelayoneOrgID` (fallback to `profile.MspOrgID` if first is empty; final fallback `"default"`).
    - Mint R1-internal JWT pair: payload `{tenant_id, roles, relayone_user_id, msp_org_id, msp_managed_orgs}`, subject = `profile.Sub`. Roles come from `profile.Raw["roles"]` if present, else `[]string{"member"}`.
    - Create R1 session via `SessionMgr.CreateAuthenticated(profile, tokens)` (Phase E). Bind session ID into JWT as `session_id`.
    - Set cookies: `__Host-r1_at=<access>; ...; Max-Age=900` and `__Host-r1_rt=<refresh>; ...; Path=/auth; Max-Age=2592000`. Refresh cookie path is `/auth` so it's only sent to `/auth/*` endpoints.
    - Clear `__Host-r1_sso_state` (Max-Age=0).
    - 302 to `entry.Next` (validated against allowlist + must start with `/`).

23. [ ] Implement `(h *SsoHandlers) RefreshHandler` for `POST /auth/refresh`:
    - Read `__Host-r1_rt` cookie. Missing returns 401 with `WWW-Authenticate: Bearer realm="r1", error="invalid_request"`.
    - `JWT.RefreshAccess(rt)`. On any error returns 401 with `WWW-Authenticate: Bearer realm="r1", error="invalid_token"`.
    - Set new access cookie (`__Host-r1_at`). Also return access token in the response `Authorization` header (for non-cookie clients e.g. cmd/r1-admin) and in the JSON body `{"access_token": "...", "expires_at": "..."}`.
    - Returns 200.

24. [ ] Implement `(h *SsoHandlers) LogoutHandler` for `POST /auth/logout`:
    - Clear `__Host-r1_at` and `__Host-r1_rt` cookies (Max-Age=0).
    - `SessionMgr.Invalidate(sessionID)` (extracted from the access token; tolerate missing access cookie — logout must always succeed).
    - If discovery doc has `end_session_endpoint`, 302 to that with `id_token_hint=<saved id_token>` and `post_logout_redirect_uri=<Config.LogoutRedirectURL>`. If absent (RelayOne discovery does not currently expose `end_session_endpoint` — see Section 5), return 200 with `{"logged_out": true}` and `Cache-Control: no-store`.

25. [ ] Create `internal/auth/middleware.go`:
    - `func (s *JwtService).RequireBearer(next http.Handler, opts MiddlewareOptions) http.Handler` — extracts token from `Authorization: Bearer <token>` (preferred) or `__Host-r1_at` cookie (fallback). Verifies. On success, attaches `*VerifiedToken` to request context via `WithVerified(r, v)`. On failure, 401 with `WWW-Authenticate: Bearer realm="r1", error="invalid_token"`.
    - Anonymous-fallthrough mode: `MiddlewareOptions.AllowAnonymous bool` — when true, missing tokens proceed (no context attached); only bad tokens reject. Used by routes that work for both authenticated SSO sessions and local-CLI anonymous flows.
    - Helper `VerifiedFromContext(ctx) (*VerifiedToken, bool)`.

### Phase E — Sessionhub integration

26. [ ] Modify `internal/server/sessionhub/session.go`:
    - Add field `TenantID string` to `Session` struct (after `SessionRoot`, before `Workspace`).
    - Add field `Subject string` (the JWT `sub`).
    - Add field `Roles []string`.
    - Constructor `newSession` gains a `tenantID, subject string, roles []string` parameter set (default empty string / nil slice for anonymous sessions).
    - These fields are stable, never mutated after Create. The `runMu`-protected fields are unchanged.

27. [ ] Add `internal/server/sessionhub/sessionhub.go::CreateAuthenticated(...)` alongside the existing `Create`:
    - Signature: `CreateAuthenticated(ctx, profile RelayOneProfile, tokens Tokens, workspaceFactory func(...) any, model string) (*Session, error)`.
    - Derives `tenantID := profile.RelayoneOrgID || profile.MspOrgID || "default"` (same precedence as the callback handler — keep ONE helper function `auth.TenantFromProfile(profile)` and call it from both).
    - Existing anonymous `Create` continues to work; sets `TenantID = ""` and the auth middleware skips tenant-scoped checks for those.

28. [ ] Wire `auth.mode` config switch in `cmd/r1-server/main.go`:
    - Parse `R1_AUTH_MODE` env (values: `"anonymous"` default, `"sso"`, `"both"`).
    - In `"anonymous"`: SSO routes return 404; existing loopback bearer auth (from `internal/server/auth_middleware.go::RequireBearer`) is the only gate. No change from today's behavior.
    - In `"sso"`: SSO routes are wired; non-loopback origins MUST present a valid JWT cookie or `Authorization: Bearer`. Loopback (127.0.0.1) origins continue to accept the bearer token from `~/.r1/daemon.json` for local CLI use.
    - In `"both"`: SSO routes wired; both auth modes accepted on the same routes (try JWT first; fall through to bearer; require at least one).

### Phase F — Per-tenant isolation

29. [ ] Extend `internal/ledger/store.go` (and the embedded ledger schema in `internal/ledger/migrate.go`):
    - Add column `tenant_id TEXT NOT NULL DEFAULT ''` to the `nodes` table. Migrate via a new migration step (the migration framework already exists per `internal/ledger/migrate.go`).
    - Every `Append` / `Insert` call gains a `TenantID` field on the node struct. Default `""` means "shared/legacy" — accessible to all tenants.
    - Add index `CREATE INDEX idx_nodes_tenant ON nodes(tenant_id);`.
    - All query paths (`internal/ledger/index.go`) accept a `tenantID string` arg; `""` means "no filter"; non-empty filters to `WHERE tenant_id = ? OR tenant_id = ''` (always-visible shared nodes).

30. [ ] Extend `internal/bus/event.go` (the in-memory hub event):
    - Add field `TenantID string` to `Event` struct.
    - Subscribers gain an optional `WithTenantFilter(tenantID string)` option that drops events whose `TenantID` is non-empty and != filter. Default (no filter) means "see everything" — preserves current behavior for system subscribers.
    - The `EmitBus` helper in `internal/eventlog` (see `specs/event-log-proper.md` Section 5) copies `TenantID` into the event-log row. Add the column to the eventlog schema as part of this work: `tenant_id TEXT` + `CREATE INDEX idx_events_tenant ON events(tenant_id);`.

31. [ ] Extend storage namespacing for cloud paths:
    - When a session has `TenantID != ""`, every `internal/cloud` GCS write path is prefixed with `tenants/<tenantID>/...`. Implement in `internal/cloud/client.go`'s `Object()` builder — accept an optional `WithTenant(tenantID)` option.
    - Cloud SQL row writes already have a `tenant_id` column from item 29; just ensure the cloud-side schema migration is added to `deploy/sql/migrations/` (or wherever the existing migrations live — confirm in `cloudbuild-deploy-api.yaml` consumer).

### Phase G — Tests

32. [ ] Create `internal/auth/jwt_test.go`:
    - `TestSignVerifyRoundTrip_HS256` — sign with `{"foo":"bar"}` + subject `"user-1"`; verify; assert `payload["foo"]=="bar"`, `payload["sub"]=="user-1"`, `header.Alg=="HS256"`.
    - `TestWrongIssuerRejected` — same key, different issuer on verify side, expect `errors.Is(err, ErrJwtVerification)`.
    - `TestIssuePairAndRefresh` — `IssuePair({"role":"admin"}, "u")`, then `RefreshAccess(pair.RefreshToken)`, then `Verify(newAccess)` and assert `role=="admin"`, `token_use=="access"`.
    - `TestRefuseRefreshFromAccessToken` — `RefreshAccess(pair.AccessToken)` returns `errors.Is(err, ErrNotRefreshToken)`.
    - `TestRS256Roundtrip` — generate RSA-2048 keypair via `rsa.GenerateKey`, PKCS8-encode, sign + verify, assert `header.Alg=="RS256"`.
    - `TestJwtServiceFromEnv_RejectsMissingIssuer` — `JwtServiceFromEnv(empty)` returns error containing `"AUTH_JWT_ISSUER"`.
    - `TestKidRotation` — issue with kid="k1"; create a second service with kid="k2"; verify k1 token in the k2 service via a multi-kid `jwk.Set` and assert it picks the right key.

33. [ ] Create `internal/auth/sso_client_test.go`:
    - `TestEndpointsPinned` — `NewSsoClient({issuer: "https://api.relayone.com/"})`; assert endpoints exact-match the four pinned URLs (no trailing slash on issuer; `/oauth/authorize`, `/oauth/token`, `/oauth/userinfo`, `/.well-known/jwks.json`).
    - `TestNormalizeProfile` — feed the exact claim payload from `auth-core/test/relayone-sso-client.test.ts` lines 24–33 (sub, email, msp_org_id, msp_managed_orgs); assert `MspOrgID == &"ro_org_msp"`, `MspManagedOrgs == ["ro_org_a","ro_org_b"]`, `RelayoneUserID == "ro_user_42"`.
    - `TestSsoClientFromEnv_DegradedWhenUnconfigured` — empty env returns `(nil, nil)` (no error).
    - `TestSsoClientFromEnv_AllFourPresent` — all four env vars yields a valid client.
    - `TestExchangeCodeMissingIDToken` — mock IdP returns `{"access_token":"at"}` without `id_token`; `ExchangeCodeForProfile` returns `ErrMissingIDToken`.

34. [ ] Create `internal/auth/sso_handlers_test.go`:
    - Spin up a mock IdP via `httptest.NewTLSServer` serving `/.well-known/openid-configuration`, `/.well-known/jwks.json` (with a pre-generated RSA key), `/oauth/authorize` (304 redirect with a code param), `/oauth/token`, `/oauth/userinfo`.
    - `TestSsoFullFlow` — GET `/auth/sso/start?next=/dashboard` yields 302 to mock authorize URL with PKCE params; simulate callback hit; assert R1-internal cookies set; assert 302 to `/dashboard`.
    - `TestStateMismatchReturns400` — POST to callback with a state that doesn't match the cookie returns 400.
    - `TestStateReplayRejected` — callback with valid state succeeds; second callback with same state returns 400 (one-shot StateStore.Take).
    - `TestRefreshAfterExpiry_Returns401` — issue a refresh with TTL=1s; sleep 2s; POST `/auth/refresh` returns 401 with `WWW-Authenticate: Bearer realm="r1", error="invalid_token"`.
    - `TestRefreshWithoutCookie_Returns401_NoStackTrace` — POST `/auth/refresh` cold returns 401, body MUST NOT contain "panic" or any stack trace.
    - `TestLogoutWithoutCookie_StillReturns200` — POST `/auth/logout` with no cookies returns 200.
    - `TestCallbackRejectsNonAllowlistedNext` — `next=https://evil.com` returns 400; `next=//evil.com` returns 400; `next=/dashboard` returns 302.

35. [ ] Create `internal/auth/interop_test.go`:
    - Run only if `R1_TEST_AUTH_INTEROP=1` is set (gated to keep CI fast; nightly job sets it).
    - `TestTSToken_VerifiesInGo` — load `testdata/ts-issued-hs256.jwt` (committed; generated by a one-shot script in `auth-core/scripts/emit-test-token.ts` documented in Section 7). Verify in Go with the matching HS256 secret. Assert `payload["token_use"]=="access"`.
    - `TestGoToken_VerifiesInTS_OutOfBand` — Go test emits a token to `testdata/go-emitted-hs256.jwt`; Node-side `auth-core` test consumes it. We can't run Node here, so the assertion is "file exists + parseable as compact JWS", with an `make interop-verify-ts` Make target that runs the Node verifier.
    - `TestRS256_BothDirections` — same flow with RSA keys generated once + committed to `testdata/`.

36. [ ] Create `internal/auth/bench_test.go`:
    - `BenchmarkVerify1000Concurrent` — pre-issue 1000 tokens; 32 goroutines each call `Verify` in a tight loop for 1s; assert p99 latency < 5ms (use `internal/bench` helpers if they exist for percentile harness, else `golang.org/x/perf/benchstat`).
    - `BenchmarkIssue` — `Sign` throughput baseline (no hard assertion, just record).

### Phase H — Wiring + docs

37. [ ] Modify `cmd/r1-server/main.go`:
    - In `run()`, after the existing `requireBearer` setup, branch on `R1_AUTH_MODE`. In `"sso"` or `"both"` mode:
      - Load `KeyMaterial` via `auth.LoadKeyMaterial(ctx, auth.DefaultKeySources(ctx))`.
      - Construct `*JwtService` via `auth.JwtServiceFromEnv(os.Getenv)` (which internally uses the key material if env vars are file references like `file:///etc/r1/jwt-priv.pem`).
      - Construct `*SsoClient` via `auth.SsoClientFromEnv(os.Getenv)`. If nil, log a one-line warning and disable SSO routes (do not crash; this is the degraded-mode contract from `auth-core`).
      - Construct `*SsoHandlers`. Mount at `mux.HandleFunc("/auth/sso/start", h.StartHandler)`, `/auth/sso/callback`, `/auth/refresh`, `/auth/logout`.
      - Wrap the rest of the routes in `jwt.RequireBearer(mux, MiddlewareOptions{AllowAnonymous: mode == "both"})`.
    - In `"anonymous"` mode (default): no change. Existing loopback bearer stays.

38. [ ] Create `docs/integrations/relayone-sso.md`:
    - Section 1: Registering R1 as an OIDC client at RelayOne. Walk through the admin endpoint `POST /v1/oauth/clients` from `oidc-provider.controller.ts`; capture `client_id` + `client_secret`; redirect URI is `https://<r1-host>/auth/sso/callback`.
    - Section 2: Env-var reference table. Every variable, type, example, fallback behavior. Mirror the table at the top of this spec.
    - Section 3: Key rotation runbook. RS256: generate new keypair, write to `~/.r1/keys/jwt-priv.pem.new`, SIGHUP r1-server (reload triggers a re-read of the key material), wait one access-TTL window, delete old key. HS256: rotate by deploying a new secret with the same `kid` — all existing tokens expire after one TTL window.
    - Section 4: Troubleshooting matrix. Mismatched issuer (most common — `RELAYONE_SSO_ISSUER` missing `/v1` suffix); clock skew (jwx allows 60s by default — adjust via `jwt.WithAcceptableSkew`); RP-Initiated Logout 404 (RelayOne discovery doesn't expose `end_session_endpoint` today — local cookie clear is sufficient; document the open ticket if/when RelayOne adds it).
    - Section 5: Per-tenant isolation guarantees + how to verify. Show example ledger query with `tenant_id` filter; show example session list filtered by tenant.

39. [ ] Add `STATUS.md` line: `relayone-sso (BUILD_ORDER=36) — done <DATE>` (filled by /build).

## 7. Acceptance criteria (measurable)

- A token issued by `auth-core@0.1.0` (TS) using HS256 with secret `"a".repeat(32)`, issuer `"https://test.relayone.dev"`, audience `"test-app"`, payload `{"foo":"bar","sub":"user-1"}` MUST validate in Go via `JwtService.Verify` with the same parameters and return `payload["foo"] == "bar"`.
- A token issued by Go `JwtService` MUST be verifiable by `auth-core` TS `JwtService.verify` in a round-trip Node test (gated behind `make interop-verify-ts`). Same for RS256 with a shared SPKI public key.
- `/auth/sso/start` 302's to a URL whose query string contains `code_challenge_method=S256` and a `state` matching the cookie value.
- `/auth/sso/callback` with a mismatched `state` (vs cookie) returns 400 with body containing `"state_mismatch"`.
- `/auth/sso/callback` replayed with the same state (after a successful first use) returns 400 with body containing `"state_consumed"` or `"state_mismatch"`.
- `/auth/refresh` with an expired refresh token returns 401 and `WWW-Authenticate: Bearer realm="r1", error="invalid_token"`.
- `/auth/refresh` with no cookie returns 401 with NO stack trace in body and NO panic in logs.
- `/auth/logout` with no cookies returns 200 (logout is idempotent).
- Benchmark `BenchmarkVerify1000Concurrent` reports p99 < 5 ms on a Linux x86_64 dev box (2024+ hardware). If not met, file a follow-up issue; do not fail the build.
- Per-tenant isolation: a session created with `tenant_id="ten-A"` MUST NOT see ledger nodes inserted with `tenant_id="ten-B"`. Test: `TestPerTenantLedgerIsolation` in `internal/ledger/store_test.go`.
- Anonymous-mode local CLI: with `R1_AUTH_MODE=anonymous`, `curl 127.0.0.1:3948/v1/sessions` with the loopback bearer token still works exactly as it does today (no regression).

## 8. Boundaries / non-goals

- **No Node-side runtime dep.** Go port is standalone. Cross-language interop is verified via committed test vectors, not a runtime gRPC call.
- **No custom JWT lib.** We use `lestrrat-go/jwx/v2`. No bespoke JOSE code.
- **No HS256 multi-tenant via shared secret.** HS256 mode is single-tenant. Multi-tenant deployments MUST run RS256 with per-tenant `kid` derivation (item 4).
- **No password/magic-link auth in this spec.** Those primitives exist in `auth-core/src/password-auth.ts` + `magic-link-auth.ts`; if R1 ever needs them, they get their own spec.
- **No `SessionStore` port.** The TS `SessionStore` is Postgres-backed; R1's session model is in-memory + journaled (per `specs/r1d-server.md`). We bind SSO sessions to the existing sessionhub, not a new Postgres table.
- **No introspection endpoint port.** TS `relayone-introspect-client.ts` calls RelayOne's `/oauth/introspect`; we don't need it for the R1 use case (we trust the access token's signature + claims).
- **No SCIM or SAML.** Out of scope — those live in the RelayOne control plane, not the consumer.

## 9. Rollout

- Phase A–C (lib + JwtService + SsoClient): can land independently behind `R1_AUTH_MODE=anonymous` default. Zero user impact.
- Phase D–E (handlers + sessionhub field): land together; gated by `R1_AUTH_MODE=sso`. Document opt-in.
- Phase F (per-tenant isolation): schema migrations are additive (default `""` empty tenant means legacy/shared). Safe to deploy ahead of any user actually using multi-tenant.
- Phase G (tests): land alongside each phase.
- Phase H (wiring + docs): final step; flip the docs link in `README.md` to point at `docs/integrations/relayone-sso.md`.

## 10. Open questions (resolve before merge)

- Confirm the HKDF `info` string for per-tenant key derivation matches what the TS team is using (or will use). Current proposal: `"r1-jwt-tenant-v1:" + tenantID`. If TS differs, change both sides in the same PR. — Tracked in item 4.
- Confirm with RelayOne whether `end_session_endpoint` will be added to the discovery doc. If yes, item 24's "RP-Initiated Logout" path becomes the default; if no, local cookie clear is the only option. Today's RelayOne discovery (verified) does NOT expose it.
- Decide whether `R1_AUTH_MODE=both` is worth the maintenance burden, or whether we should require operators to pick `anonymous` XOR `sso`. Defaulting to "both" for backward compat but the docs flag it as transitional.

### Critical files for implementation
- `/home/eric/repos/r1-agent/internal/auth/jwt.go` (new)
- `/home/eric/repos/r1-agent/internal/auth/sso_client.go` (new)
- `/home/eric/repos/r1-agent/internal/auth/sso_handlers.go` (new)
- `/home/eric/repos/r1-agent/internal/server/sessionhub/session.go` (modified — TenantID field)
- `/home/eric/repos/r1-agent/cmd/r1-server/main.go` (modified — wire auth routes)
