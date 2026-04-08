# Sneaky Finder Audit Report

**Date:** 2026-04-01
**Scope:** ember/devbox, flare, stoke
**Filter:** Only findings where suppression hides a real bug or swallowed error causes silent data loss.

---

## Summary

Searched all three repos for: lint/type suppressions, empty catch blocks, swallowed errors, silent null returns, tautological tests, skipped tests, hidden early returns, and hardcoded placeholders.

Most `catch {}` blocks in ember/devbox are on non-critical UI polling or parse-attempt paths where silent failure is acceptable. The critical findings below are cases where silent failure masks auth bypass, data loss, or infrastructure failures.

---

## Findings

### ember/devbox — Empty Catch Blocks (Silent Error Swallowing)

- [ ] [HIGH] [ember/devbox/src/index.ts:188] Admin auth session validation wrapped in bare `catch {}`. If `lucia.validateSession()` throws (corrupt DB, connection timeout), the error is silently swallowed and `authorized` remains `false`. **This is acceptable for auth denial** but masks database failures from operators — the admin stats endpoint silently returns 401 instead of 500, hiding a broken DB connection. — fix: Log the error before falling through: `catch (e) { console.error("[admin] session check failed:", e); }` — effort: trivial

- [ ] [CRITICAL] [ember/devbox/src/routes/ai.ts:116] In the SSE stream `flush()` handler, JSON parse failure in `catch {}` silently discards the final usage data. If the last SSE chunk contains valid usage but has a trailing newline or whitespace issue, **AI usage metering silently loses the cost record**. The INSERT into `ai_usage` never fires, so the user gets free API calls. — fix: Add `console.error("[ai] flush parse error:", e, lineBuf)` and attempt to extract `total_cost` from a partial match as fallback. — effort: small

- [ ] [HIGH] [ember/devbox/src/routes/github.ts:209,249] Two identical patterns: GitHub API call to `/user` wrapped in `catch {}`. If the fetch throws a network error (DNS failure, timeout), `ghUsername` stays `null` and the endpoint returns `{ repos: [], method: "github_api_failed" }` or `{ configured: true, installed: false }`. The user sees "no repos" or "not installed" with **no indication the real cause is a network outage**. — fix: `catch (e) { console.error("[github] user fetch failed:", e.message); }` — effort: trivial

- [ ] [MEDIUM] [ember/devbox/web/src/pages/Settings.tsx:49] Billing portal redirect fails silently with `catch {}`. If `POST /api/billing/portal` throws, the user clicks "Manage Billing" and **nothing happens** — no error message, no feedback. — fix: `catch { setError("Could not open billing portal"); }` — effort: trivial

- [ ] [MEDIUM] [ember/devbox/web/src/pages/Dashboard.tsx:62] Terminal ticket fetch fails silently. If `POST /api/machines/{id}/terminal` throws, the terminal pane never opens and user has **no indication why**. — fix: Catch and set an error state on the terminal pane. — effort: small

- [ ] [MEDIUM] [ember/devbox/web/src/pages/Dashboard.tsx:267] Stoke status polling: `try { setStokeData(...) } catch {}`. If the API returns malformed JSON or 500, the stoke panel shows stale data indefinitely with **no staleness indicator**. — fix: On catch, set a `stokeError` state or clear stale data. — effort: small

### ember/devbox — Silent Null Returns

- [ ] [HIGH] [ember/devbox/src/fly.ts:62-65] `flyApi()` returns `null` on any non-OK HTTP response after logging to console. Every caller must check for `null`, but callers like `getMachineState()` (line 334) propagate `null` upward. If Fly API is down, machine state shows as `null` throughout the UI — **machines appear to not exist rather than showing an error state**. — fix: Throw a typed `FlyApiError` with status code; let callers decide whether to catch or propagate. — effort: medium

- [ ] [HIGH] [ember/devbox/src/fly.ts:70-74] Non-JSON response body from Fly API is silently treated as `{ ok: true }`. If Fly returns an HTML error page (502 from their load balancer), this function returns `{ ok: true }` — **the caller thinks the operation succeeded when it did not**. — fix: Return `{ ok: true, rawBody: text }` or throw on unparseable non-empty body. — effort: small

- [ ] [MEDIUM] [ember/devbox/src/github-app.ts:97-99,104-106] `createInstallationToken()` returns `null` on failure. Callers like `listInstallationRepos()` (line 115) return `[]` on null. **Repo listing silently degrades to empty** when GitHub App auth fails, indistinguishable from "user has no repos". — fix: Return a discriminated result `{ ok: false, error: string }` or throw. — effort: medium

### flare — Silent Error Swallowing in Critical Paths

- [ ] [CRITICAL] [flare/internal/firecracker/manager.go:384-391] `persistVM()` silently returns on JSON marshal failure (line 388-389) AND silently discards `os.WriteFile` errors (line 391 — return value unchecked). If `vm.json` fails to write, **RecoverFromDisk() will not find this VM after daemon restart — the VM process keeps running but flare loses track of it**, creating an orphaned Firecracker process consuming resources with no way to stop it. — fix: Return error from `persistVM()`, propagate to `Create()` and `Start()` callers. Log at minimum. — effort: small

- [ ] [HIGH] [flare/internal/firecracker/manager.go:492-494] `RecoverFromDisk()` returns `0` on `os.ReadDir` failure with no logging. If the VM directory is on a failed mount or has permission issues, **all VMs appear lost after restart** and the daemon reports 0 recovered with no error. — fix: Log the error: `log.Printf("recover: read vmdir: %v", err)` and consider returning an error. — effort: trivial

### flare — Skipped Integration Tests

- [ ] [HIGH] [flare/cmd/control-plane/integration_test.go:112,188,239,265] Four out of five integration tests are permanently `t.Skip()`-ed with "Requires running control plane and placement daemon". These tests cover the four critical invariants listed in the file header (state survives restart, placement survives restart, routing correctness, destroy-after-failed-create). **The test file creates a false impression of test coverage — `go test ./...` passes but tests nothing beyond MAC address generation and state machine logic.** — fix: Either implement the subprocess management or move these to a separate `_manual_test.go` file with clear documentation that they are not run in CI. — effort: large

### stoke — No Actionable Findings

All `@ts-ignore`/`eslint-disable`/`nolint` references in stoke are in string literals used to *detect* these patterns in code being audited (scan rules, prompt instructions, failure analyzers). These are correct and intentional. No actual suppression pragmas found in stoke's own Go code.

### Cross-Repo — `as any` Usage in ember/devbox

- [ ] [MEDIUM] [ember/devbox/src/routes/machines.ts:437] `c.req.json().catch(() => ({} as any))` — if request body parsing fails, code proceeds with an empty object cast to `any`. The `code` destructure produces `undefined`, which is checked on the next line, so this is safe. However, `as any` masks the actual type and other fields could be silently missing. — fix: Type the fallback: `.catch(() => ({ code: undefined }))` — effort: trivial

- [ ] [MEDIUM] [ember/devbox/src/routes/machines.ts:41,56,207,249,265,298,352,399,532] Repeated `c.get("user") as any` pattern (9 occurrences). Hono middleware sets a typed user but the route handlers bypass the type with `as any`. If the middleware type changes, **no compile-time error warns the route handlers**. — fix: Define a `UserContext` type and use `c.get("user") as UserContext` or properly type the Hono app variable. — effort: small

---

## Patterns Searched (No Actionable Hits)

| Pattern | Result |
|---------|--------|
| `eslint-disable` in source code | None in app code (only in tooling/scan config) |
| `@ts-ignore` / `@ts-nocheck` | None in app code |
| `nolint` in Go source | None in app code |
| Tautological tests (`expect(true).toBe(true)`) | None found |
| Hardcoded secrets / "CHANGEME" | None found |
| `TODO`/`FIXME` in ember/devbox | None found |

---

## Risk Summary

| Severity | Count | Repos |
|----------|-------|-------|
| CRITICAL | 2 | ember/devbox (1), flare (1) |
| HIGH | 5 | ember/devbox (3), flare (2) |
| MEDIUM | 5 | ember/devbox (5) |
| **Total** | **12** | |

**Top priority:** The `persistVM()` silent failure in flare (orphaned VMs) and the AI usage metering gap in ember/devbox (lost billing records).
