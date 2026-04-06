# security-auth

> Authentication, authorization, secrets management, and OWASP defense patterns

<!-- keywords: auth, authentication, authorization, jwt, oauth, token, session, password, bcrypt, csrf, xss, injection, secret, api key, rbac -->

## Critical Rules

1. **Never store plaintext passwords.** Use bcrypt with cost >= 12 (or argon2id). Never MD5, SHA-256 alone, or any unsalted hash.

2. **Never log secrets.** API keys, tokens, passwords, PII must never appear in logs. Redact before logging. Grep your log output.

3. **JWT secrets must be rotatable.** Support `kid` (key ID) header for key rotation. Sign with RS256 (asymmetric) for microservices, HS256 only for monoliths.

4. **SQL injection is still the #1 vulnerability.** Always use parameterized queries. Never string-concatenate user input into SQL. This includes `ORDER BY` clauses.

5. **Rate limit authentication endpoints.** 5 attempts per IP per 15 minutes minimum. Use exponential backoff with jitter. Lock accounts after 10 failures.

## Authentication Patterns

### JWT Best Practices
- Short-lived access tokens (15 min max)
- Longer-lived refresh tokens (7-30 days) stored server-side
- Include `iat`, `exp`, `sub`, `iss` claims minimum
- Validate ALL claims on every request
- Revocation: maintain a blocklist or use short expiry + refresh rotation

### Session Security
- `HttpOnly` + `Secure` + `SameSite=Lax` on session cookies
- Regenerate session ID on privilege change (login, role change)
- Bind session to IP/User-Agent for sensitive operations
- Server-side session storage (Redis/DB), never client-only

### API Key Management
- Hash API keys before storage (SHA-256 is fine for random keys)
- Prefix keys for identification: `sk_live_`, `pk_test_`
- Scope keys to minimum required permissions
- Support key rotation without downtime

## OWASP Top 10 Defense

- **Injection:** Parameterized queries, ORM, input validation at boundary
- **Broken Auth:** MFA, account lockout, session timeout
- **Sensitive Data:** Encrypt at rest (AES-256-GCM), in transit (TLS 1.2+)
- **XXE:** Disable external entity processing in XML parsers
- **Broken Access Control:** Default deny, check on every request, not just UI
- **Security Misconfiguration:** No default credentials, minimal error exposure
- **XSS:** Output encoding, CSP headers, avoid `innerHTML`/`dangerouslySetInnerHTML`
- **Insecure Deserialization:** Never deserialize untrusted data without validation
- **Known Vulnerabilities:** `npm audit`, `go mod verify`, Dependabot
- **Insufficient Logging:** Log auth events, access control failures, input validation failures

## Common Gotchas

- **Timing attacks on string comparison.** Use `crypto/subtle.ConstantTimeCompare` for token/hash comparison.
- **CORS wildcards with credentials.** `Access-Control-Allow-Origin: *` cannot be used with `credentials: include`.
- **JWT in URL params.** Tokens in URLs leak via Referer header, server logs, browser history.
- **`eval()` with user input.** Never. Not even "sanitized" input.
- **Hardcoded secrets in source.** Use environment variables or secret managers (Vault, AWS Secrets Manager).
