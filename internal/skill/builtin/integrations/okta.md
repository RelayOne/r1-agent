# okta

> Okta: enterprise identity. OIDC + SAML SSO, SCIM provisioning, MFA, API Access Management. Customer Identity Cloud (CIC, ex-Auth0) is a separate product — this is Workforce Identity Cloud.

<!-- keywords: okta, sso, oidc, saml, scim, enterprise identity, workforce identity -->

**Official docs:** https://developer.okta.com/docs  |  **Verified:** 2026-04-14.

## Tenant URLs

- Org: `https://<your-domain>.okta.com` (or `.oktapreview.com` for sandbox)
- Custom domain: `https://id.yourcompany.com` (after domain setup)

## Auth for API access

- **OAuth2 access token** (preferred) — client credentials grant with private key JWT:
  ```
  POST /oauth2/default/v1/token
  grant_type=client_credentials&scope=okta.users.read&client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer&client_assertion=<JWT>
  ```
- **Legacy SSWS token**: `Authorization: SSWS <api_token>` — simpler, but org-wide admin scope.

## OIDC login (user-facing)

Redirect users to:

```
https://<org>.okta.com/oauth2/default/v1/authorize
  ?client_id=<ID>&response_type=code&scope=openid+profile+email
  &redirect_uri=https://app/callback&state=<state>&code_challenge=<PKCE>&code_challenge_method=S256
```

Exchange `code` at `/oauth2/default/v1/token` → get `access_token` + `id_token`.

Verify `id_token` signature via JWKS: `https://<org>.okta.com/oauth2/default/v1/keys`.

## Users API

```
GET    /api/v1/users/{id}
POST   /api/v1/users?activate=true       { "profile": {...}, "credentials": {...} }
POST   /api/v1/users/{id}/lifecycle/suspend
DELETE /api/v1/users/{id}                (hard delete; suspend+deactivate preferred)
```

## Groups + group rules

```
POST /api/v1/groups      { "profile": { "name": "engineers" } }
PUT  /api/v1/groups/{gid}/users/{uid}
```

Group rules auto-assign users based on profile expressions. Great for role mapping from HR system.

## SCIM (auto-provisioning into downstream apps)

Okta acts as SCIM client, your app is the server. Implement:

```
GET    /scim/v2/Users?filter=userName eq "a@b.com"
POST   /scim/v2/Users
PATCH  /scim/v2/Users/{id}
DELETE /scim/v2/Users/{id}
GET    /scim/v2/Groups
```

Okta syncs users + groups to your app on schedule. Must return RFC 7644–compliant responses.

## Event hooks / webhooks

Event Hooks: Okta POSTs user lifecycle events to your URL. Requires one-time verification (Okta GETs `x-okta-verification-challenge` header, you echo it in JSON body).

Inline Hooks: Okta calls your endpoint DURING a flow (e.g., before issuing a token) and uses your response. Must respond within 3 seconds.

## MFA enrollment + challenge

MFA is usually transparent via Okta-hosted sign-in widget. For custom: Authentication API flow with `factor.enroll` + `factor.verify` calls.

## Common gotchas

- **Rate limits are per endpoint + per org tier**. `/users` is ~600/min on most plans. Check `X-Rate-Limit-Remaining`.
- **SSWS tokens show as the admin who created them** — rotate when admins leave. Use OAuth service apps instead.
- **Authorization Server `default` vs custom**: `default` is free on all plans; custom auth servers need API Access Management SKU.
- **SAML vs OIDC**: for new integrations prefer OIDC unless the downstream app requires SAML. SAML metadata URL: `https://<org>.okta.com/app/{appId}/sso/saml/metadata`.

## Key reference URLs

- Users API: https://developer.okta.com/docs/reference/api/users/
- OIDC: https://developer.okta.com/docs/guides/implement-grant-type/authcode/main/
- SCIM: https://developer.okta.com/docs/concepts/scim/
- Event Hooks: https://developer.okta.com/docs/concepts/event-hooks/
- Rate limits: https://developer.okta.com/docs/reference/rl-global-mgmt/
