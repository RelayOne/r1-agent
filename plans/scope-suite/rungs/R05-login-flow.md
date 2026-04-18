# R05 — Login flow end-to-end (multi-file feature)

A small but realistic feature spanning 5-7 coordinated files:
backend validation + API endpoint + React form + client-side hook
+ test. Tests whether stoke can coordinate across frontend+backend.

## Scope

Build a login flow in a Next.js App Router project:

**Backend**:
- `packages/types/src/auth.ts` — Zod: `LoginRequest {email, password}`,
  `LoginResponse {token, user: {id, email}}`
- `app/api/auth/login/route.ts` — `POST /api/auth/login`
  - validates body against `LoginRequest`
  - fixed dev-user check: `email === "demo@example.com" && password === "demo123"`
    → returns `{ token: "mock-jwt-demo", user: {...} }`, 200
  - Invalid body → 400 with Zod error array
  - Wrong creds → 401 with `{ error: "invalid_credentials" }`

**Frontend**:
- `app/login/page.tsx` — React form with email + password fields +
  submit button. Uses controlled inputs + `useState`. On submit, calls
  the API via `fetch`. On 200: store token + redirect `/dashboard`.
  On 4xx: show error message inline.
- `app/dashboard/page.tsx` — server component. Reads token from cookie;
  if absent, redirect to `/login`. If present, display "Welcome, demo!".
- `packages/api-client/src/auth.ts` — typed wrapper exposing
  `loginRequest(email, password): Promise<LoginResponse>` that wraps
  fetch + error handling.

**Test**:
- `app/api/auth/login/__tests__/route.test.ts` — 3 cases:
  happy path, invalid body (400), wrong creds (401).

## Acceptance

- All files exist as declared above
- `npm test` passes the 3 login-route test cases
- `next build` produces no type errors
- `packages/types/src/auth.ts` schemas match the API contract
  (API returns exactly the shape the type declares)

## What NOT to do

No database, no real JWT crypto, no password hashing, no session
storage beyond cookies, no rate limiting. This is a functional-
contract test, not a production auth system.
