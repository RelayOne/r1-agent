# auth0

> Auth0 identity platform — Universal Login, social providers, Management API v2. Machine-to-machine clients for server-side management calls; Authentication API for user flows.

<!-- keywords: auth0, identity, oauth, oidc, universal login, social login, sso, rbac, management api, authentication -->

**Official docs:** https://auth0.com/docs  |  **Verified:** 2026-04-14 via web search.

## Two APIs, different purposes

- **Authentication API** (`https://{tenant}.auth0.com/`): user-facing flows — login, signup, password reset, MFA, social login, token exchange.
- **Management API v2** (`https://{tenant}.auth0.com/api/v2/`): administrative — create/update users, roles, rules, connections. Requires a Machine-to-Machine token with appropriate scopes.

## Getting a Management API token (M2M)

```js
const resp = await fetch(`https://${AUTH0_DOMAIN}/oauth/token`, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({
    client_id: M2M_CLIENT_ID,
    client_secret: M2M_CLIENT_SECRET,
    audience: `https://${AUTH0_DOMAIN}/api/v2/`,
    grant_type: "client_credentials",
  }),
});
const { access_token, expires_in } = await resp.json();
// Default lifetime: 86400s (24h). Cache and refresh before expiry.
```

Tokens are JWTs. Cannot be revoked once issued — keep lifetime short and scope narrow (request only the permissions the call actually needs, e.g. `read:users`, `update:users`).

## User login (PKCE flow for SPAs / mobile)

Prefer Universal Login + the SDK (`@auth0/nextjs-auth0`, `@auth0/auth0-react`, `@auth0/auth0-spa-js`). Direct Authentication API calls for custom-UI cases only:

```js
// Start: redirect browser to
`https://${AUTH0_DOMAIN}/authorize?response_type=code&client_id=${CLIENT_ID}&redirect_uri=${REDIRECT}&scope=openid%20profile%20email&state=xyz&code_challenge=...&code_challenge_method=S256`

// Callback → exchange code for tokens:
fetch(`https://${AUTH0_DOMAIN}/oauth/token`, {
  method: "POST",
  body: JSON.stringify({
    grant_type: "authorization_code",
    client_id: CLIENT_ID,
    code_verifier: "...",          // PKCE verifier
    code: AUTH_CODE,
    redirect_uri: REDIRECT,
  }),
});
// Returns: access_token (JWT for your API audience), id_token (user claims), refresh_token
```

## Validating access tokens server-side

Use the tenant's JWKS endpoint: `https://{tenant}.auth0.com/.well-known/jwks.json`. Verify with RS256 against the `kid` in the JWT header. Check `iss`, `aud` (must match your API identifier), `exp`.

Libraries: `jose` (Node), `pyjwt` (Python), `github.com/auth0/go-jwt-middleware` (Go).

## Common flows via Management API

```js
// Create user
await fetch(`https://${AUTH0_DOMAIN}/api/v2/users`, {
  method: "POST",
  headers: { Authorization: `Bearer ${mgmtToken}`, "Content-Type": "application/json" },
  body: JSON.stringify({
    email: "a@b.com",
    password: "generated-strong-password",
    connection: "Username-Password-Authentication",
    email_verified: false,
  }),
});

// Assign role
await fetch(`https://${AUTH0_DOMAIN}/api/v2/users/${userId}/roles`, {
  method: "POST",
  headers: { Authorization: `Bearer ${mgmtToken}` },
  body: JSON.stringify({ roles: ["rol_abc"] }),
});
```

## Actions / Rules (custom logic in login pipeline)

Modern: **Actions** (Node.js, versioned, bindable to triggers like `post-login`). Legacy: Rules (deprecated for new tenants). Write in the dashboard or via Management API.

## Common gotchas

- **Audience mismatch**: the `aud` claim in access tokens must match your API identifier in Auth0 Dashboard → APIs. If you request `audience: ...auth0.com/userinfo`, you get an opaque token, not a JWT.
- **Callback URL must be whitelisted** in the application settings or Auth0 rejects.
- **Refresh token rotation** (recommended): enabled per-application; each use invalidates the previous. Reduces blast radius on token leaks.

## Key reference URLs

- Management API: https://auth0.com/docs/api/management/v2
- Management API tokens: https://auth0.com/docs/secure/tokens/access-tokens/management-api-access-tokens
- Access token validation: https://auth0.com/docs/secure/tokens/access-tokens/validate-access-tokens
- Universal Login: https://auth0.com/docs/authenticate/login/auth0-universal-login
- Next.js SDK: https://github.com/auth0/nextjs-auth0
