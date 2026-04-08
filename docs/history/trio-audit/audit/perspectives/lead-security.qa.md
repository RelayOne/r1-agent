# Lead Security Engineer Audit

**Date:** 2026-04-01
**Scope:** ember/devbox (TypeScript web app), flare (Go VM control plane), stoke (Go AI orchestrator)
**Method:** Manual code review of auth, input validation, secrets, crypto, CSRF, CORS, rate limiting, injection vectors, SSRF, IDOR

---

## MUST FIX (Security Vulnerabilities / Data Loss)

- [ ] **CRITICAL** [flare/cmd/control-plane/main.go:79] **API key comparison uses `!=` (non-constant-time)** — The flare control plane compares bearer tokens with `token != apiKey` which is vulnerable to timing attacks. An attacker can progressively guess the API key one byte at a time by measuring response time deltas. This is the single authentication gate for ALL flare API operations (create/destroy VMs, start/stop machines). — fix: Use `crypto/subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1` for the comparison. — effort: trivial

- [ ] **CRITICAL** [flare/cmd/control-plane/main.go:88-89] **Internal auth skipped when FLARE_INTERNAL_KEY is empty** — `internalAuth` middleware allows ALL requests when `cp.internalKey == ""`. The host registration and heartbeat endpoints (`/internal/hosts/register`, `/internal/hosts/heartbeat`) are fully open if FLARE_INTERNAL_KEY is not set. A network-adjacent attacker could register rogue hosts and intercept VM traffic via the ingress proxy. Same pattern exists in `flare/cmd/placement/main.go:79`. — fix: Refuse to start (log.Fatal) if FLARE_INTERNAL_KEY is empty, or at minimum reject requests with an explicit error when no key is configured. — effort: small

- [ ] **CRITICAL** [flare/cmd/control-plane/main.go:88-89] **Internal key comparison uses `!=` (non-constant-time)** — Same timing attack vulnerability as the API key. `r.Header.Get("X-Internal-Key") != cp.internalKey` is used for internal host auth. — fix: Use `crypto/subtle.ConstantTimeCompare`. — effort: trivial

- [ ] **HIGH** [ember/devbox/src/middleware.ts:77-83] **CSRF bypass when Origin header is absent but session cookie exists** — The CSRF protection allows requests without an Origin header if a valid session cookie is present (line 80-81). This is exploitable: form submissions from `<form method="POST">` do NOT include an Origin header in some browsers (notably older versions). An attacker can craft a cross-origin form that posts to the API and the browser will attach the session cookie. The session check on line 80 then PASSES the CSRF check, allowing the mutation. — fix: Require Origin header on ALL state-changing requests (remove the session fallback). For same-origin non-fetch requests that legitimately lack Origin, the frontend should switch to fetch() which always sends Origin. — effort: small

- [ ] **HIGH** [ember/devbox/src/secrets.ts:20] **Encryption key derived via SHA-256 of arbitrary string (non-KDF)** — If `APP_ENCRYPTION_KEY` is not exactly 32 hex-bytes or 32-byte base64, `crypto.createHash("sha256").update(raw).digest()` is used (line 20). SHA-256 is not a proper KDF -- it has no work factor, no salt, no iteration count. A weak passphrase-style key would be trivially brutable. This key protects GitHub OAuth tokens at rest. — fix: Use a proper KDF like HKDF (available via `crypto.hkdf`) or require the key to be exactly 32 bytes (hex or base64). Log an error and refuse to start if the key format is invalid rather than silently deriving. — effort: small

- [ ] **HIGH** [ember/devbox/src/index.ts:172] **Reconcile secret comparison uses `===` (non-constant-time)** — `auth === \`Bearer ${secret}\`` at line 172 (health/detailed endpoint) and `auth !== \`Bearer ${secret}\`` at billing.ts:723 (reconcile endpoint) are both vulnerable to timing attacks. These secrets gate access to operational data and billing reconciliation. — fix: Use `crypto.timingSafeEqual(Buffer.from(auth || ''), Buffer.from(\`Bearer ${secret}\`))` with length check first. — effort: trivial

- [ ] **HIGH** [ember/devbox/src/routes/machines.ts:25] **Region parameter not validated against allowlist** — The `region` field in `createSchema` uses `z.string().default("sjc")` with no enum or pattern validation. An attacker could pass arbitrary strings like `../../etc` or `' OR 1=1--` as the region, which flows into Fly API calls and potentially the database. While SQL injection is prevented by parameterized queries, the unchecked region could cause SSRF against unexpected Fly API endpoints or trigger unexpected Fly behavior. — fix: Change to `z.enum(["sjc", "lax", "iad", ...])` with an explicit allowlist of valid regions. — effort: trivial

- [ ] **HIGH** [stoke/internal/pools/pools.go:60] **Pool manifest written with 0644 permissions** — The manifest file at `~/.stoke/pools/manifest.json` (containing pool IDs and config paths to credential directories) is world-readable. While credentials themselves are in 0700 dirs, the manifest leaks which accounts are registered and where credentials live. — fix: Use `os.WriteFile(ManifestPath(), data, 0600)`. — effort: trivial

- [ ] **HIGH** [stoke/internal/pools/pools.go:164] **Token fragment used as account ID dedup key** — `accountID = "token-" + token[:min(16, len(token))]` stores the first 16 characters of an access token as a persistent identifier in the manifest file. This is a partial credential leak to disk. If the manifest is compromised, the attacker gets 16 chars of a valid OAuth token, significantly narrowing brute-force space. — fix: Hash the full token with SHA-256 and use the hash as the dedup key: `accountID = "token-" + sha256(token)[:16]`. — effort: trivial

---

## SHOULD FIX (Reliability / Missing Validation)

- [ ] **MEDIUM** [ember/devbox/src/routes/billing.ts:390-398] **Stripe webhook allows requests when STRIPE_WEBHOOK_SECRET is unset** — `process.env.STRIPE_WEBHOOK_SECRET!` with the `!` assertion means if the env var is missing, `constructEvent` gets `undefined` as the secret. Depending on the Stripe SDK version, this could skip signature verification entirely or throw an unhelpful error. The production config validator catches this, but non-production environments (staging, dev) are exposed. — fix: Guard with an explicit check: `if (!process.env.STRIPE_WEBHOOK_SECRET) return c.json({ error: "Webhook secret not configured" }, 500)` before calling constructEvent. — effort: trivial

- [ ] **MEDIUM** [ember/devbox/src/routes/billing.ts:722-725] **Reconcile endpoint accessible when RECONCILE_SECRET is unset** — `if (secret && auth !== ...)` means when RECONCILE_SECRET is empty/undefined, the entire condition is false and the endpoint is open to anyone. In development this is by design, but if accidentally deployed without the secret, any unauthenticated user can trigger billing reconciliation. — fix: Return 503 when RECONCILE_SECRET is not configured: `if (!secret) return c.json({ error: "Reconcile not configured" }, 503)`. — effort: trivial

- [ ] **MEDIUM** [ember/devbox/src/middleware.ts:64-67] **CSRF exemption list uses exact string match (fragile)** — `exemptPaths` uses `Set.has(c.req.path)` which won't match paths with trailing slashes, query strings already parsed into the path, or URL-encoded variants. If Hono normalizes paths differently, the exemptions could fail. — fix: Use `c.req.path.startsWith()` or `new Set` with normalized paths. Verify behavior with trailing slashes. — effort: small

- [ ] **MEDIUM** [flare/cmd/control-plane/main.go:662-692] **VM ingress proxy does not validate Host header against registered hostnames before proxying** — `proxyVMTraffic` resolves a hostname from the database and proxies to the host's ingress address. The proxy preserves the original Host header (`req.Host = r.Host`). If the DB lookup succeeds but the machine has been destroyed between lookup and proxy, the request reaches a stale host. More critically, the reverse proxy passes the full original path through to the host, potentially allowing path traversal if the host-side routing is not strict. — fix: Add a timeout/context on the proxy, validate machine is in `running` state (already done via the query), and ensure the host-side `/_vm/` routing strips the prefix properly. — effort: small

- [ ] **MEDIUM** [ember/devbox/src/routes/machines.ts:416-419] **Upload filename passed through without sanitization** — `file.name || "upload"` is passed to `fly.uploadFileToMachine()` without sanitizing for path traversal characters (e.g., `../../../etc/passwd`). If the machine-side upload handler uses this filename to write to disk, it could be exploited. — fix: Sanitize filename: strip path separators, limit to alphanumeric + dots + hyphens, truncate length. — effort: small

- [ ] **MEDIUM** [ember/devbox/src/routes/account.ts:30] **Forgot-password endpoint has no rate limiting** — The `/api/account/forgot-password` endpoint is not covered by any rate limiter in index.ts. While there's an in-app limit of 3 active tokens per user, an attacker can spam this endpoint with different email addresses to enumerate users (timing differences) or cause email flooding. — fix: Add a rate limiter: `app.use("/api/account/forgot-password", authLimiter)` in index.ts. — effort: trivial

- [ ] **MEDIUM** [ember/devbox/src/routes/account.ts:133] **Email verification token not validated via Zod** — `const { token } = await c.req.json()` extracts the token without schema validation. A malformed body could cause unexpected errors. — fix: Add a Zod schema like the reset endpoint: `z.object({ token: z.string().min(1) })`. — effort: trivial

- [ ] **MEDIUM** [ember/devbox/src/routes/admin.ts:23] **Admin user listing has no pagination limit enforcement** — The page parameter is parsed from user input (`c.req.query("page")`) but only has `Math.max(1, ...)` protection. A very large page number causes a large OFFSET which is a DoS vector (Postgres scans through all skipped rows). — fix: Cap page number: `Math.min(page, 1000)` or similar, and add a total count query for proper pagination. — effort: small

- [ ] **MEDIUM** [stoke/internal/hooks/hooks.go:96-101] **Hook script parses JSON with grep/cut (fragile, bypassable)** — The PreToolUse hook extracts `tool_name` and `tool_input` using `grep -o` and `cut` on JSON. Malformed or nested JSON can bypass these guards. For example, a command containing `"tool_name":"safe"` in a string value could confuse the parser. An AI agent executing within stoke could potentially craft inputs that bypass the bash guards. — fix: Use `jq` for JSON parsing (check for availability), or rewrite hooks in Go/Python for reliable JSON parsing. — effort: medium

- [ ] **MEDIUM** [stoke/internal/engine/env.go:71-76] **Mode 2 passes full environment including secrets** — `safeEnvMode2()` copies the entire parent environment and appends extras. If stoke runs with cloud credentials (AWS_*, GOOGLE_*), Mode 2 Claude sessions get full access to those credentials. The function name suggests awareness ("safe") but Mode 2 is explicitly not stripped. — fix: Document this as intentional for Mode 2 (user provides own key), or add a configurable deny-list for Mode 2 as well. — effort: small

- [ ] **MEDIUM** [ember/devbox/src/routes/machines.ts:36] **Client IP from X-Forwarded-For not validated** — `getClientIp()` trusts `fly-client-ip` and `x-forwarded-for` headers directly. If the app is accessed without Fly's proxy (e.g., directly via internal network), these headers can be spoofed, poisoning audit logs with fake IP addresses. — fix: Document that the app must always be behind Fly's proxy, or validate the source of XFF headers. — effort: small

- [ ] **MEDIUM** [ember/devbox/src/auth.ts:44-48] **OAuth providers initialized with empty strings instead of failing** — `process.env.GITHUB_CLIENT_ID || ""` means if credentials are missing, the GitHub/Google objects are created with empty strings rather than failing at startup. In dev this is intentional, but in production it could lead to confusing OAuth failures instead of a clear startup error. The production validator does not check for these. — fix: Add GITHUB_CLIENT_ID, GITHUB_CLIENT_SECRET, GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET to the production config validator, or at minimum log a warning at startup. — effort: trivial

---

## Positive Findings (Already Well-Implemented)

- Parameterized SQL throughout (Drizzle ORM + tagged template literals) -- no SQL injection vectors found
- AES-256-GCM with random 12-byte IV for secret encryption (correct usage)
- Stripe webhook signature verification present
- OAuth state parameter validation on both GitHub and Google callbacks
- Google OAuth uses PKCE (code verifier)
- Session invalidation on password reset, ban, and logout (including terminal sessions)
- Exchange codes are single-use, time-limited, and atomically consumed
- Zod schema validation on most input endpoints
- CSP headers configured with restrictive defaults
- Rate limiting on auth, billing, machines, and terminal endpoints
- Audit logging on security-critical operations
- File upload size limits (100MB)
- Machine ownership checks (userId) on all machine operations -- no IDOR
- Password hashing uses Argon2 (@node-rs/argon2)
- Symlink attack prevention in stoke hook installation (safeWrite, safeMkdirAll)
- Process group isolation in stoke Claude runner (prevents orphaned processes)
- Production config validator catches many missing secrets at startup
