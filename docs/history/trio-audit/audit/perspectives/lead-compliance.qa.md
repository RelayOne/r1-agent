# Lead Compliance & Code Quality Audit

**Scope:** ember/devbox (TypeScript), flare (Go), stoke (Go)
**Auditor:** Lead Compliance & Code Quality Engineer
**Date:** 2026-04-01

---

## Methodology

Searched for:
- TypeScript: `@ts-ignore`, `@ts-nocheck`, `as any`, `eslint-disable`, empty catch blocks
- Go: `//nolint`, `_ = err`-style discards, unhandled error returns
- Filtered to cases where the bypass hides a real bug or creates genuine confusion

Skipped:
- Legitimate `as any` for external API JSON responses (GitHub, Google OAuth) where the type is truly unknown and the fields are field-accessed immediately
- `_ = os.MkdirAll` on init paths where the directory not existing would surface immediately on first use
- Best-effort fire-and-forget operations with explicit comments (MMDS injection, audit log, etc.)
- Test file `_ =` discards

---

## Findings

### CRITICAL

- [ ] [CRITICAL] [ember/devbox/src/routes/billing.ts:269-270] `(stripeSub as any).current_period_start` — Stripe SDK v16+ exposes these as typed fields (`Subscription.current_period_start: number`). The `as any` bypass means if Stripe ever renames or removes these fields the access silently produces `undefined`, leading to `new Date(NaN * 1000)` being stored as ISO string `"Invalid Date"` in `current_period_start`/`current_period_end` columns. Webhook processing would continue silently with corrupted period data.
  — fix: Remove `as any`. Use `stripeSub.current_period_start` and `stripeSub.current_period_end` directly — they are typed on `Stripe.Subscription`.
  — effort: trivial

- [ ] [CRITICAL] [ember/devbox/src/routes/account.ts:15] `{ apiVersion: "2024-04-10" as any }` — The Stripe SDK enforces its `ApiVersion` literal type at compile time. This `as any` silences a compiler error that would fire if the pinned API version is not recognized by the installed SDK version. Using a stale API version string with `as any` means the mismatch is invisible; the SDK may silently send requests with a version the current binary does not understand, causing unexpected response shapes for payment-critical operations (password reset, account management).
  — fix: Either upgrade `stripe` package so `"2024-04-10"` is a recognized literal, or pin to a version string the current SDK exports (e.g. `Stripe.latestApiVersion`). Remove the `as any`.
  — effort: trivial
  — note: Same pattern appears in `credits.ts:11`. Fix both at once.

- [ ] [CRITICAL] [ember/devbox/src/routes/credits.ts:11] Same `{ apiVersion: "2024-04-10" as any }` as account.ts:15 — same risk for credit purchase and webhook handling.
  — fix: Same as above — align SDK version or use `Stripe.latestApiVersion`.
  — effort: trivial

---

### HIGH

- [ ] [HIGH] [flare/internal/reconcile/reconciler.go:159,172,185,210,215] `cfg.Store.UpdateMachineObserved(...)` return value silently dropped — This function returns `error`. Dropping the error in the hot reconciliation loop means a transient DB write failure (connection loss, constraint violation) goes completely undetected. The reconciler then calls `ReleaseClaim` or returns `nil`, treating a failed state update as success. The machine gets released with stale `observed_state`, and the next reconcile cycle may compute the wrong action (e.g., repeatedly attempting to start a machine that is actually already running but whose observed state was never updated).
  — fix: Capture and log the error; on failure, call `cfg.Store.RequeueWithBackoff` instead of proceeding. Pattern: `if err := cfg.Store.UpdateMachineObserved(...); err != nil { return cfg.Store.RequeueWithBackoff(ctx, m.ID, m.ReconcileAttempts+1, "update observed failed: "+err.Error()) }`.
  — effort: small

- [ ] [HIGH] [flare/internal/reconcile/reconciler.go:166,191] `cfg.Store.SetTransitionDeadline(...)` return value silently dropped — Returns `error`. If the deadline write fails, the machine will never be re-observed (no deadline = no actionObserve trigger on next cycle). Combined with the already-issued start/stop command, this creates a machine permanently stuck in transition with no self-healing path.
  — fix: Capture and log; requeue on failure.
  — effort: small

- [ ] [HIGH] [ember/devbox/src/routes/settings.ts:20,26,34,40,65,76] `(c as any).get("user") as any` — Every handler in settings.ts double-casts the Hono context. The outer `(c as any)` is unnecessary because the `Hono` app is correctly typed with `{ Variables: { user: any; ... } }` on line 16, so `c.get("user")` is already valid. The inner `as any` on the result then defeats all downstream type checking for the user object. In contrast, every other route file (billing.ts, machines.ts, etc.) correctly uses `c.get("user") as any` without the extra context cast. This is inconsistent and masks the real issue: `user` is typed `any` in the Variables generic, so no narrowing ever applies — user property access errors (e.g., `user.nonExistent`) are invisible.
  — fix: Remove the outer `(c as any)` cast — it is spurious. Then type the Variables generic properly: `{ Variables: { user: { id: string; email: string; role: string; status: string }; ... } }` (same as `middleware.ts` Env type). This is already defined in middleware.ts; reuse it.
  — effort: small

- [ ] [HIGH] [ember/devbox/src/routes/ai.ts:68] `orRes.status as any` — HTTP status from OpenRouter is cast to `any` to pass to `c.json(..., status)`. Hono's second argument expects `StatusCode` (a union of valid HTTP status codes). If OpenRouter returns a non-standard or 5xx status code outside Hono's union, the cast silently passes an invalid value, potentially causing Hono to emit a malformed HTTP response with an undefined status line. The fix is cheap and avoids the silent runtime corruption.
  — fix: `return c.json({ error: ... }, orRes.status >= 400 && orRes.status < 600 ? orRes.status as StatusCode : 502)`. Import `StatusCode` from `hono/utils/http-status`.
  — effort: trivial

- [ ] [HIGH] [stoke/cmd/stoke/main.go:1469] `secMap, _ = scanpkg.MapSecuritySurface(absRepo, nil)` — The error is silently discarded. `secMap` is then passed to `audit.BuildPrompt` or similar consumers. If `MapSecuritySurface` fails (e.g., directory not found, parse error), `secMap` is `nil` and any downstream code that dereferences it will panic. The `--security` flag path is user-invoked; a silent failure here means the user believes the security surface was mapped when it was not.
  — fix: `secMap, err = scanpkg.MapSecuritySurface(absRepo, nil); if err != nil { fmt.Fprintf(os.Stderr, "warning: security surface mapping failed: %v\n", err) }`. This is non-fatal but should be visible.
  — effort: trivial

---

### MEDIUM

- [ ] [MEDIUM] [ember/devbox/src/routes/api-keys.ts:27] `(body as any).label` — `body` is already typed as `{}` from `.catch(() => ({}))`. Accessing `.label` through `as any` means there is zero validation: any string (or non-string) value is passed directly as the label into the DB. There is no length cap, no sanitization.
  — fix: Apply a zod schema (`z.object({ label: z.string().max(100).default("default") })`) consistent with how every other route in this codebase validates request bodies.
  — effort: trivial

- [ ] [MEDIUM] [ember/devbox/src/routes/credits.ts:27] `(body as any).packageId` — Same pattern as api-keys.ts:27. No validation on `packageId` before using it as a lookup key. The lookup itself is safe (key doesn't exist → 400), but the inconsistency is notable: every other route uses zod schemas; this one does a raw body cast. A future change that trusts `packageId` further (e.g., string interpolation into a message) would have no validation backstop.
  — fix: Add `const parsed = z.object({ packageId: z.string().max(100) }).safeParse(body)` and use `parsed.data.packageId`.
  — effort: trivial

- [ ] [MEDIUM] [stoke/internal/session/store.go:117] `s.readJSON(path, &attempts)` error silently discarded — In `SaveAttempt`, the initial `readJSON` to load existing attempts discards its error. If the existing history file is corrupted (partial write, disk error), the error is swallowed and `attempts` remains `nil`. The new attempt is then written as a single-element slice, erasing all prior history. The learning deduplication and retry intelligence is lost silently.
  — fix: `if err := s.readJSON(path, &attempts); err != nil && !os.IsNotExist(err) { return fmt.Errorf("load attempts for %s: %w", a.TaskID, err) }` — treat corruption as an error rather than silently overwriting.
  — effort: trivial

- [ ] [MEDIUM] [stoke/internal/session/store.go:146] `learning, _ := s.LoadLearning()` — In `addLearnedPattern`, the error is discarded. If learning.json is corrupted, `learning` is nil (guarded on line 147), but the corruption goes unreported and is overwritten silently on `SaveLearning`. Cross-task learning patterns are silently destroyed.
  — fix: `learning, err := s.LoadLearning(); if err != nil { return /* or log */ }` — at minimum log the error before proceeding.
  — effort: trivial

- [ ] [MEDIUM] [flare/cmd/control-plane/main.go:166] `b, _ := json.Marshal(body)` — In `callPlacement`, marshal errors are silently discarded. `b` would be `nil` on error, and `bytes.NewReader(nil)` produces an empty body, sending a contentless HTTP request to the placement daemon. The placement daemon then sees an empty JSON body and returns a decode error, which propagates back as an HTTP error — confusing because the root cause (marshal failure) is invisible. `json.Marshal` only fails on types that cannot be marshaled (channels, functions, circular references); the current callers pass `struct` values so the risk is low, but the pattern is a maintenance trap.
  — fix: `b, err := json.Marshal(body); if err != nil { return nil, fmt.Errorf("marshal request body: %w", err) }`.
  — effort: trivial

- [ ] [MEDIUM] [ember/devbox/web/src/pages/Dashboard.tsx:62,96,267] Empty `catch {}` swallowing terminal UI state — Three separate catch blocks in Dashboard silently swallow errors. Lines 62 and 96 catch errors in the terminal ticket fetch and hostname polling loops respectively. Line 267 catches stoke-status polling errors. The UI remains stuck in whatever state it was in with no feedback. These are polling loops where a persistent failure (e.g., machine deleted, auth expired) should surface rather than silently looping.
  — fix: At minimum, log to `console.warn` with the machine ID and error. For line 62 (terminal ticket), set a local error state so the pane shows "Failed to connect" instead of a stuck spinner.
  — effort: small

- [ ] [MEDIUM] [stoke/internal/workflow/workflow.go — multiple lines] `_ = e.advanceState(taskstate.Failed, ...)` — The state transition error is systematically discarded in every failure path that already returns a non-nil error (lines 158, 261, 330, 343, 354, 366, 378, 462, 477, 485, 494, 515, 527, 536, 543, 550, 581, 615). This is a deliberate pattern when already on an error return path. However, `advanceState` can return an error when the transition is invalid (e.g., trying to transition to `Failed` from a terminal state). This invalid-transition error is silently lost, meaning the state machine audit trail is incomplete and `TaskState.Phase()` may not reflect the actual workflow outcome — which breaks `CanCommit()` and the anti-deception display.
  — fix: Log the advance error even when discarding: `if advErr := e.advanceState(taskstate.Failed, reason); advErr != nil { log.Printf("[workflow] state advance failed: %v (original error: %v)", advErr, err) }`. Alternatively, wrap in a helper that logs and continues.
  — effort: small

---

### Patterns Reviewed and NOT Reported (filtered as legitimate)

- `c.get("user") as any` in billing.ts, machines.ts, github.ts, admin.ts, etc. — The Hono Variables generic is typed with `user: any` at the app level, so the cast is vacuously correct. The real fix is to type the Variables generic properly (covered in settings.ts finding above). Flagging every individual `c.get("user")` would be noise without the underlying fix.
- `await res.json() as any` for GitHub API, Google OAuth, OpenRouter responses — These are external API responses where the upstream type is genuinely not modeled. The fields accessed immediately after are guarded (`.login`, `.email_verified`, `.usage`). This is correct usage of `as any` for unmodeled external contracts.
- `setCookie(c, ..., cookie.attributes as any)` in auth.ts and middleware.ts — Lucia's cookie attributes type does not exactly match Hono's `CookieOptions`. This is a known library interop gap. The cast is consistent across all callers and causes no runtime issue.
- `_ = os.MkdirAll(root, 0700)` in stoke/session/store.go:35-36 — Init-time directory creation; any failure surfaces immediately on the first file write.
- `_ = mgr.Cleanup(ctx, handle)` in stoke integration test — test teardown, fine.
- `_ = timeout // comment` in stoke/cmd/stoke/main.go:1583 — explicit suppress of unused variable warning, with comment. Legitimate.
- `rescanResult, _ := scanpkg.ScanFiles(...)` in stoke/cmd/stoke/main.go:1589 — Re-scan in Phase 4 of scan-and-repair; the result is used immediately for the diff count. Error means zero findings, which is a safe default for a display-only re-scan step.
- `flare/internal/networking/tap.go:95,133` `strconv.Atoi` discarded error — These are parsing IP octets from strings that the Manager itself constructed, so the format is known-valid. The `_ =` is safe here.
- `flare/internal/reconcile/reconciler.go` — `RecordEvent` and `DeleteHostnamesByMachine` have `void` return types (not errors). No issue.

---

## Summary by Repo

| Repo | Critical | High | Medium |
|------|----------|------|--------|
| ember/devbox | 3 | 2 | 3 |
| flare | 0 | 2 | 1 |
| stoke | 0 | 1 | 3 |
| **Total** | **3** | **5** | **7** |

### Cross-repo inconsistency

The most pervasive issue is ember/devbox's **untyped Variables generic**. Every route file re-derives `c.get("user") as any` instead of sharing the `Env` type that middleware.ts already defines correctly. Fixing the Variables generic in one shared place would eliminate ~30 `as any` casts across the codebase and restore type safety for all user property accesses. The settings.ts double-cast is a symptom of this root cause.
