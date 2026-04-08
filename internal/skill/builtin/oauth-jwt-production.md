# oauth-jwt-production

> OAuth 2.1 token flows, JWT sizing and rotation, PKCE enforcement, Phantom Token pattern, and passkey adoption for production multi-tenant SaaS

<!-- keywords: oauth, oidc, jwt, token, refresh, pkce, passkey, fido, mfa, session, bff, workload identity, spiffe, zanzibar, authorization -->

## Critical Rules

1. **PKCE is mandatory for all clients.** RFC 9700 (January 2025) requires authorization servers to reject requests missing a `code_challenge`. Without PKCE, authorization codes are capturable via Referer headers, browser history, or malicious extensions.

2. **Redirect URI validation demands exact string matching only.** OAuth 2.1 mandates this. Wildcard or pattern-based redirect URIs have caused real breaches (Azure multi-tenant apps, Keycloak CVE-2023-6927).

3. **JWTs are stateless so they remain valid until expiration regardless of revocation.** Short-lived access tokens (5-15 minutes per RFC 9700) limit the damage window. For critical operations, add token introspection (RFC 7662) as a real-time check.

4. **Set a hard JWT size budget of 2KB.** Include only `sub`, `iss`, `aud`, `exp`, `iat`, `jti`, `scope`, `tenant_id`. Look up rich authorization data server-side. JWTs around 3.6KB have caused connection failures (Hasura users).

5. **Never use HS256 in multi-party scenarios.** The same key signs and verifies, meaning any validating service can forge tokens. Use ES256 (ECDSA P-256) as default: 4x smaller signatures than RS256.

## Token Refresh Race Conditions

When two browser tabs detect an expired access token simultaneously, both use the same refresh token. With rotation enabled, the first succeeds and invalidates the old token. The second triggers theft detection and revokes the entire token family.

Fix: **Refresh token grace period** (30 seconds) during which a recently-rotated token can be reused. Client-side: implement a mutex so only one tab refreshes; others queue and receive the new token. Proactively refresh at 80% of access token TTL.

## The Phantom Token Pattern

Issue opaque tokens to clients (instantly revocable, reveal no PII). At the API gateway, introspect the opaque token to receive a JWT, forward to internal microservices for stateless validation. The gateway caches the JWT keyed by opaque token hash, eliminating repeated introspection. Curity publishes NGINX and Kong plugins.

## Key Rotation

Publish the new public key in JWKS alongside the old key. Activate for signing. Remove old key only after `retirement_time + max_token_lifespan + safety_buffer`. Every JWT must carry a `kid` header. Cache JWKS responses with jitter on refresh intervals. Use singleflight/mutex to prevent JWKS stampedes. Maintain last-known-good JWKS in persistent storage for resilience.

## Where Tokens Live

- **Web SPAs**: BFF (Backend-for-Frontend) pattern. BFF handles OAuth as a confidential client, stores tokens server-side, issues HttpOnly/Secure/SameSite=Strict cookies. Tokens never reach JavaScript.
- **Mobile apps**: Authorization headers with tokens in platform-native secure storage (iOS Keychain, Android Keystore). Consider DPoP (RFC 9449) to bind tokens to the device.
- **Never localStorage**: Any XSS exfiltrates all tokens.

## Session Best Practices

- Always regenerate session ID after authentication (prevents session fixation).
- Redis is optimal for sessions: sub-millisecond reads, native TTL via EXPIRE, 3-10x faster than PostgreSQL at 10K concurrent users.
- Use `__Host-` cookie prefix for shared-domain multi-tenant deployments (forbids Domain attribute, scopes to exact host).
- Concurrent session limits: default 3-5 active, evict oldest on new login.

## MFA: Passkeys Are Production-Ready

Google reports 800M+ accounts using passkeys with 2.5B+ sign-ins. Amazon has 175M+ customers (6x faster than passwords). Domain-bound signatures prevent phishing proxies. Synced passkeys achieve AAL2 under NIST SP 800-63-4 (July 2025).

TOTP: Rate-limit to 3-5 attempts per 30-second window. Enforce one-time use per window. Tolerate +/-1 time step for clock drift. SMS MFA: classified as "restricted authenticator" by NIST. SIM swapping succeeds 80% of attempts. Use only as transitional fallback.

MFA fatigue: solved by number matching (user enters 2-digit number from login screen into authenticator app). Now mandatory for Microsoft Entra, Okta, and Duo.

## Authorization Models

Start with **RBAC** (tenant-scoped roles: Owner, Admin, Editor, Viewer). When sharing individual resources is needed, add **ReBAC** (Zanzibar-style graph relationships). SpiceDB: 5ms p95, used by OpenAI. OpenFGA: CNCF Incubating, developer-friendly.

The **new enemy problem**: when permissions change, stale cached permissions could still grant access. Zanzibar solves with zookies (consistency tokens encoding a global timestamp). Any authorization system without a consistency model is vulnerable.

## Service-to-Service Auth

Use workload identity (GCP Workload Identity Federation, AWS EKS Pod Identity, Azure AD Workload Identity). Eliminates long-lived credentials. mTLS via service mesh adds ~3% latency at p99. For vendor-neutral identity, use SPIFFE/SPIRE (CNCF graduated).

## Common Gotchas

- **Using ID tokens as access tokens**: Skips audience validation. ID tokens are for the client; access tokens are for the resource server.
- **Not validating `iss` and `aud` claims**: Enables cross-tenant token confusion.
- **Implicit flow**: Deprecated. Tokens in URL fragments leak via browser history and Referer headers.
- **Algorithm confusion attack**: Changing `alg` from RS256 to HS256 and signing with the public key. Always whitelist allowed algorithms.
- **JWT in URL params**: Tokens leak via Referer header, server logs, browser history.
