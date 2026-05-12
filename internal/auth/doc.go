// Package auth — Go port of the @relayone/auth-core TypeScript primitives.
//
// This package gives the R1 daemon two capabilities that the TS
// `@relayone/auth-core` provides to Node-side RelayOne apps:
//
//   - JwtService — HS256 or RS256 sign/verify/refresh. Cross-language
//     interop: a JWT minted by the TS service validates here and a JWT
//     minted here validates there. Source of truth for the contract is
//     /home/eric/repos/auth-core/src/jwt-service.ts.
//
//   - SsoClient — OIDC authorization-code-with-PKCE client targeting the
//     RelayOne control plane (https://auth.relayone.io). Mirrors
//     /home/eric/repos/auth-core/src/relayone-sso-client.ts.
//
// # Algorithms
//
//   - HS256: single-tenant deployments. Shared secret as raw UTF-8 bytes.
//     Multi-tenant fan-out via HKDF-SHA256 in keys_tenant.go.
//   - RS256: multi-tenant deployments. PKCS8 PEM for the signing key,
//     SPKI PEM for the verify key. Per-tenant `kid` derivation supported.
//
// # Env-var contract
//
// JwtServiceFromEnv reads:
//
//	AUTH_JWT_ISSUER            (required)
//	AUTH_JWT_AUDIENCE          (required)
//	AUTH_JWT_SECRET            (HS256 only)
//	AUTH_JWT_PUBLIC_KEY        (presence -> RS256; SPKI PEM)
//	AUTH_JWT_PRIVATE_KEY       (RS256 only; PKCS8 PEM)
//	AUTH_JWT_ACCESS_TTL_SEC    (optional; default 900)
//	AUTH_JWT_REFRESH_TTL_SEC   (optional; default 2592000)
//
// SsoClientFromEnv reads:
//
//	RELAYONE_SSO_CLIENT_ID
//	RELAYONE_SSO_CLIENT_SECRET
//	RELAYONE_SSO_ISSUER        (fallback: RELAYONE_SSO_URL)
//	RELAYONE_SSO_REDIRECT_URI
//
// All four are required to construct a client; missing any returns
// (nil, nil) — degraded mode matching the TS factory.
//
// # File-mode discipline
//
// FileSource key material follows the pattern from
// internal/ledger/redact_sign.go: 0600 for private keys, 0644 for public
// keys, 0755 for the directory. First load with no material generates a
// fresh RSA-2048 keypair and persists both halves.
package auth
