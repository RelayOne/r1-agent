# VP Engineering — Type Safety Audit

**Scope:** `ember/devbox/src/` (TypeScript/Hono) and `flare/sdk/typescript/src/`
**Date:** 2026-04-01
**tsconfig:** `strict: true` on both packages — all bypasses are intentional overrides.

---

## Summary

Both repos compile under `strict: true`, so these bypasses are explicit. The Flare SDK is nearly clean. The devbox is the main concern: a systemic pattern of untyped Hono context variables, untyped external API responses, and untyped postgres transaction callbacks creates a wide surface where property access bugs are invisible to the type checker.

**Critical:** 0  
**High:** 4  
**Medium:** 8  
**Skipped (legitimate):** ~30 (catch `e: any`, `setCookie` attribute mismatch, `orRes.status as any` for HTTP status narrowing)

---

## Findings

### HIGH

- [ ] [HIGH] `ember/devbox/src/routes/billing.ts:36`, `account.ts:17`, `machines.ts:15`, `github.ts:16`, `settings.ts:16`, `admin.ts:11`, `auth.ts:15` — **Hono Variables typed as `user: any; session: any`** across all route files. The middleware (`middleware.ts:8`) already has the correct typed `Env` with `user: { id: string; email: string; name: string | null; role: string; status: string; avatarUrl: string | null }`. Because route files declare their own `Env` with `user: any`, `c.get("user")` returns `any` and the redundant `as any` cast is noise covering the root problem. A wrong property access (e.g. `user.stripeCustomerId` instead of going to the DB) would not be caught.
  — fix: Extract the `Env` type from `middleware.ts` into a shared `types.ts`, import it in every route file, and remove the inline `{ Variables: { user: any; session: any } }` declarations. The per-file `c.get("user") as any` casts then go away for free.
  — effort: small

- [ ] [HIGH] `ember/devbox/src/fly.ts:53` — `flyApi` returns `Promise<any>`. Every caller accesses properties on the return value (`.state`, `.certificates`, `.id`, `.name`, etc.) with no type safety. A shape change in the Fly Machines API response would silently propagate — e.g. `machines[0].state` at `fly.ts:334` used to drive machine state in the DB.
  — fix: Define typed response interfaces for the Fly API objects actually used (`FlyMachine`, `FlyCertificate`, `FlyVolume`). Type `flyApi` as a generic `flyApi<T>(…): Promise<T | null>` and apply at callsites.
  — effort: medium

- [ ] [HIGH] `ember/devbox/src/fly.ts:77` — `flyGraphQL` returns `Promise<any>`. The result is accessed at `fly.ts:415-418` as `result.data.organization.apps.nodes` — a deeply nested chain that would throw `TypeError: Cannot read properties of undefined` if the GraphQL schema changes or returns an error envelope.
  — fix: Type the GraphQL response as an explicit interface and narrow with a guard before access. The existing `if (!result?.data?.organization?.apps?.nodes) return []` guard is correct logic but does not help TS track the narrowed type.
  — effort: small

- [ ] [HIGH] `ember/devbox/src/github-app.ts:111` — `listInstallationRepos` return type is `Promise<any[]>`. Callers in `routes/github.ts` iterate the array and access `.full_name`, `.name`, `.private`, etc. A GitHub API schema change or an error response (e.g. `{ message: "Not Found" }`) would not be caught at the type level and would produce `undefined` fields in the JSON response.
  — fix: Define `interface GitHubRepo { full_name: string; name: string; private: boolean; clone_url: string; default_branch: string; updated_at: string }` and return `Promise<GitHubRepo[]>`.
  — effort: trivial

### MEDIUM

- [ ] [MEDIUM] `ember/devbox/src/routes/billing.ts:269-270` — `(stripeSub as any).current_period_start`. `Stripe.Subscription` exposes `current_period_start` as a number on the object in stripe-node. The cast exists because older SDK typings had it on a nested object. With `stripe@^17`, this field is at the top level; the `as any` hides that the access is already valid. If the actual SDK version has it elsewhere, the `new Date(undefined * 1000)` produces `Invalid Date` which then gets written to the DB.
  — fix: Check `stripe` package version. If `>= 13`, access directly as `stripeSub.current_period_start`. Otherwise add a comment naming the SDK version that requires the cast.
  — effort: trivial

- [ ] [MEDIUM] `ember/devbox/src/routes/credits.ts:11`, `account.ts:15` — `{ apiVersion: "2024-04-10" as any }`. The Stripe SDK exports `Stripe.LatestApiVersion`; the cast is used because the hardcoded string doesn't match. This means if the SDK is upgraded and the `apiVersion` option changes semantics, TypeScript will not flag it.
  — fix: Use `const stripe = new Stripe(…)` without `apiVersion` (uses the library default) or import and use `Stripe.LatestApiVersion`.
  — effort: trivial

- [ ] [MEDIUM] `ember/devbox/src/routes/auth.ts:138,144`, `github.ts:79,135,155,170,206,246`, `machines.ts:86` — GitHub API responses cast to `any` or `any[]`. Properties accessed: `.id`, `.login`, `.avatar_url`, `.name`, `.clone_url`, `.full_name`, etc. If a GitHub API call returns an error body (`{ message: string; documentation_url: string }`), property accesses like `ghUser.id` silently return `undefined`, which can produce DB writes with `undefined` values (e.g. `providerId: String(undefined)` → `"undefined"` in the DB).
  — fix: Define minimal typed interfaces for the GitHub user and repo objects. Add a runtime guard: `if (!ghUser?.id) return c.redirect("/login?error=oauth_failed")` before accessing `ghUser.id`.
  — effort: small

- [ ] [MEDIUM] `ember/devbox/src/routes/auth.ts:201` — `const updates: any = { providerId: ghId }`. This object is passed to `db.update(oauthAccounts).set(updates)`. With `any`, there is no check that `updates` keys match the Drizzle schema for `oauthAccounts`. An accidental key (e.g. a typo) would silently be ignored by Drizzle or cause a runtime error.
  — fix: Type as `Partial<typeof oauthAccounts.$inferInsert>` or use an explicit narrowed type: `const updates: { providerId: string; accessToken?: string } = { providerId: ghId }`.
  — effort: trivial

- [ ] [MEDIUM] `ember/devbox/src/routes/sessions.ts:46` — `z.array(z.any())` for the `tasks` field. The `tasks` array is stored as JSONB and returned to clients verbatim. No shape validation means malformed task objects (e.g. missing `id` or `status` fields that the UI depends on) would be stored and returned without error.
  — fix: Define a `taskSchema` with at least `z.object({ id: z.string(), status: z.string() }).passthrough()` to enforce minimal structure.
  — effort: small

- [ ] [MEDIUM] `ember/devbox/src/routes/ai.ts:84` — `let lastUsage: any = null`. The `lastUsage` variable is populated by parsing SSE stream chunks and then accessed as `lastUsage.total_cost`, `lastUsage.prompt_tokens`, `lastUsage.completion_tokens`. If the OpenRouter response shape changes, these become `undefined` and `undefined * MARKUP_PERCENT` silently inserts `NaN` into the `cost_usd` column.
  — fix: Define `interface OpenRouterUsage { total_cost?: number; prompt_tokens?: number; completion_tokens?: number }` and type `lastUsage: OpenRouterUsage | null`. Use `?? 0` guards already present, but with proper typing TS will enforce them.
  — effort: trivial

- [ ] [MEDIUM] `ember/devbox/src/rate-limit.ts:58` — `let pgSql: any = null`. This module-level variable is set once via lazy import. Because it is typed `any`, calling `pgSql\`...\`` performs no type checking on the SQL template calls. An import failure would result in `null` being called as a function with a runtime TypeError rather than a typed error.
  — fix: Type as `import('postgres').Sql | null` (lazy import typing). The existing null-check guard (`if (!pgSql)`) is correct but TypeScript cannot verify downstream usage without the type.
  — effort: trivial

- [ ] [MEDIUM] `ember/devbox/src/routes/billing.ts:187`, `billing.ts:420`, `billing.ts:745`, `admin.ts:73`, `machines.ts:110`, `machines.ts:223` — `rawSql.begin(async (tx: any) => {…})`. The postgres.js `Sql` type exports a typed transaction callback `begin<T>(fn: (sql: TransactionSql) => Promise<T>): Promise<T>`. Typing `tx` as `any` means SQL queries inside transactions are not checked — e.g. a `tx\`SELECT …\`` call would accept any string with no validation.
  — fix: Import `TransactionSql` from `postgres` and type the callback parameter: `rawSql.begin(async (tx: TransactionSql) => {…})`.
  — effort: trivial

### Flare SDK

- [ ] [MEDIUM] `flare/sdk/typescript/src/index.ts:69` — `request<T>(method, path, body?: any)`. The `body` parameter accepts any value. For an SDK, this is a usable but imprecise signature — callers cannot get type-checking on the request body shape.
  — fix: Type as `body?: Record<string, unknown>` or introduce per-method typed overloads. Since `request` is `private` and all call sites already pass typed objects, the risk is low but the improvement is free.
  — effort: trivial

- [ ] [MEDIUM] `flare/sdk/typescript/src/index.ts:88` — `(err as any).error`. The `err` from `res.json().catch(() => ({}))` is already typed as `{}`. The cast is needed to access `.error`. This is a legitimate JSON parsing pattern, but the cast on `err` hides the fact that `err` could be any shape.
  — fix: Type the error envelope: `const err = await res.json().catch(() => ({})) as { error?: string }`. Then `err.error` is typed without `as any`.
  — effort: trivial

---

## Patterns Skipped (Not Reportable)

- `catch (e: any)` — standard TypeScript pattern pre-`useUnknownInCatchVariables`; accessing `.message` is safe by convention.
- `setCookie(c, …, cookie.attributes as any)` — documented Lucia/Hono compatibility shim; no runtime risk, the attributes are a literal object from Lucia.
- `orRes.status as any` in `routes/ai.ts:68` — narrowing `number` to Hono's `StatusCode` union; the actual value is an HTTP status code from fetch.
- `flyApi` returning `null` on error at callsites — the null-check pattern (`if (!result)`) is consistently applied.
- `ghRes.json() as any` where the value is only used for `.login` after an `if (ghRes.ok)` guard — medium-risk, included in findings above.
