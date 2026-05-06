# VP Engineering — Code Comments & API Documentation Audit

**Date:** 2026-04-01
**Scope:** ember/devbox (TypeScript), flare (Go), stoke (Go)
**Filter:** Only issues that would genuinely confuse a new developer or where comments are actively misleading. Obvious getters and self-documenting one-liners are excluded.

---

## Summary

Overall documentation quality is good to excellent. The codebases make heavy use of inline comments explaining non-obvious design decisions. The gaps are targeted and specific — mostly around opaque names, undocumented contracts, and a few public API surfaces with no JSDoc.

**Total findings: 17** — 2 CRITICAL, 6 HIGH, 9 MEDIUM

---

## Findings

### CRITICAL

- [ ] **CRITICAL** `stoke/internal/engine/types.go:10-13` — `AuthModeMode1` and `AuthModeMode2` are completely opaque names used throughout the entire codebase (20+ call sites). The constants have no comments. A new developer cannot tell what "mode1" vs "mode2" means without tracing calls into `engine/env.go` and `engine/claude.go`. This is a core concept: mode1 = subscription auth (uses CLAUDE_CONFIG_DIR, strips API keys), mode2 = API key auth (inherits full env). The wrong mode silently passes credentials to the agent or breaks auth entirely. — fix: Add doc comments to both constants: `// AuthModeMode1 uses subscription auth (Claude Code login). Strips API keys from the child process environment and sets CLAUDE_CONFIG_DIR to the pool's config directory.` and `// AuthModeMode2 uses API key auth. Inherits the full parent environment including ANTHROPIC_API_KEY.` — effort: trivial

- [ ] **CRITICAL** `stoke/internal/workflow/workflow.go:32-51` — `workflow.Engine` is the central orchestrator struct for the entire stoke execution pipeline, but it has zero doc comments on the struct or any of its fields. A new developer looking at `DryRun`, `PlanOnly`, `AllowedFiles`, `State`, `Verifier`, `Runners` has no idea what contract each field has or whether fields are optional vs required. This struct is constructed in 3+ different places (`cmd/r1/main.go`, tests, integration tests) — fix: Add a struct-level doc comment explaining what Engine is, plus inline field comments for at minimum: `DryRun` (prepare commands but don't execute), `PlanOnly` (run plan phase only, no execute/verify), `AllowedFiles` (scope restriction — nil means unrestricted), `State` (external task state tracker, may be nil), `Verifier` (nil disables build/test/lint verification). — effort: small

---

### HIGH

- [ ] **HIGH** `stoke/internal/engine/env.go:9-13` — `SafeEnvForClaudeMode1` (the exported wrapper) has a doc comment, but `safeEnvMode2` (line 71) has none despite doing the opposite: it passes the entire parent environment through. This asymmetry is security-relevant — mode2 includes all env vars including secrets. The function's behavior must be obvious. — fix: Add `// safeEnvMode2 returns the full parent environment, optionally extended with extra vars. Used for Mode 2 (API key auth) where the child needs the caller's credentials.` — effort: trivial

- [ ] **HIGH** `flare/internal/firecracker/manager.go:424-456` — `Get`, `List`, and `ResourceUsage` are exported functions with no doc comments. `ResourceUsage` is non-obvious: it only counts VMs with a live PID (skips stopped VMs), and returns allocated CPU+memory totals for the heartbeat. This distinction matters to callers computing capacity. — fix: Add `// Get returns the VM with the given ID, or false if not found. Thread-safe.`, `// List returns all VMs including stopped ones. Callers should use State() to check liveness.`, `// ResourceUsage returns the sum of CPUs and memory (MB) for VMs whose OS process is alive. Used to report actual host utilization.` — effort: trivial

- [ ] **HIGH** `stoke/internal/subscriptions/manager.go:265-278` — `UpdateUtilization` accepts `fiveHour` and `sevenDay` float64 parameters but does not document what the values represent (percentage 0-100? fraction 0-1?). The thresholds in the body (>95, >80) confirm these are percentages, but the parameter names don't make this clear. If a caller passed a fraction (0.95), the pool would never be marked throttled. — fix: Document the parameters: `// fiveHour and sevenDay are utilization percentages (0-100) from the Claude/Codex OAuth usage endpoint.` — effort: trivial

- [ ] **HIGH** `ember/devbox/src/fly.ts:53-75` — `flyApi` is the single function wrapping all Fly Machines API calls, but it has no JSDoc. The return value contract is particularly confusing: it returns `null` on HTTP error, `{ ok: true }` for empty-body responses, and parsed JSON otherwise. Callers must pattern-match on null vs object everywhere. — fix: Add JSDoc above `flyApi`: `/** Low-level Fly Machines API wrapper. Returns null on HTTP error. Returns {ok: true} for successful responses with empty bodies. Returns parsed JSON otherwise. */` — effort: trivial

- [ ] **HIGH** `ember/devbox/src/secrets.ts:25-35` — `encryptSecret` silently returns the plaintext value unchanged if `APP_ENCRYPTION_KEY` is not configured (line 27). This is an intentional graceful-degradation design (dev mode works without key), but a new developer storing a sensitive token and seeing it in the DB without the `enc:v1:` prefix would not know whether encryption failed or was skipped. The function has no JSDoc. — fix: Add JSDoc: `/** Encrypts value using AES-256-GCM. If APP_ENCRYPTION_KEY is not set, returns value unchanged (dev mode). Idempotent: already-encrypted values are returned as-is. */` Also add the same note to `decryptSecret`. — effort: trivial

- [ ] **HIGH** `stoke/internal/plan/plan.go:33-42` — `Status` is an int enum with no String() method and no iota comments explaining what each value means operationally. These values are serialized to JSON (`stoke-plan.json`) and read back by the scheduler and TUI. `StatusVerifying` and `StatusBlocked` in particular have non-obvious semantics — `StatusVerifying` means verification is running (not the plan phase), `StatusBlocked` means a dependency failed. — fix: Add inline comments: `StatusPending // not yet started`, `StatusActive // execute phase running`, `StatusVerifying // build/test/lint verification running`, `StatusDone // all phases passed`, `StatusFailed // at least one attempt failed permanently`, `StatusBlocked // a dependency task failed`. Also add a `String()` method so log output is human-readable. — effort: small

---

### MEDIUM

- [ ] **MEDIUM** `flare/internal/store/store.go:233-240` — `ReleaseReservation` has no doc comment. It's called on failed machine placement to release capacity previously reserved by `PlaceAndReserve`. The `GREATEST(..., 0)` guard is intentional (prevents negative reserved counts under double-release), but without a comment a developer might simplify it to subtraction. — fix: Add `// ReleaseReservation decrements reserved capacity atomically. GREATEST prevents underflow if called twice (double-release safety).` — effort: trivial

- [ ] **MEDIUM** `flare/cmd/control-plane/main.go` (ControlPlane struct, ~line 37-41) — The `ControlPlane` struct has no doc comment and its `internalKey` field is undocumented. A new developer won't know that this is the secret shared with placement daemons (via `X-Internal-Key` header) and that leaving it empty disables host-to-control-plane auth entirely. — fix: Add a struct doc comment: `// ControlPlane handles the public Fly-compatible REST API and reconciliation. internalKey guards placement daemon endpoints — empty string disables authentication (dev only).` — effort: trivial

- [ ] **MEDIUM** `stoke/internal/verify/pipeline.go:94-107` — `CheckProtectedFiles` is a public function with a good doc comment, but `matchProtected` (line 109) documents its pattern semantics using inline comments only, with no function-level comment describing the three distinct match modes. The wildcard rule (`".env*"` matches `.env`, `.env.local`) uses a non-standard prefix-match that differs from glob. — fix: Add a function doc comment to `matchProtected` listing all three match modes explicitly: directory suffix `/`, wildcard dot-prefix `.*`, and exact match. — effort: trivial

- [ ] **MEDIUM** `stoke/internal/model/router.go:122-142` — `InferTaskType` is a public function with no doc comment. Its keyword matching is used to automatically route tasks to the optimal provider, but the fallback behavior (returns `TaskTypeRefactor` for anything unrecognized) is not documented. A caller submitting a devops task spelled "infrastructure" would silently get the refactor route. — fix: Add `// InferTaskType returns the TaskType best matching task description keywords. Defaults to TaskTypeRefactor for unrecognized descriptions. The keyword list is not exhaustive.` — effort: trivial

- [ ] **MEDIUM** `ember/devbox/src/middleware.ts:62-95` — `csrfProtection` documents its exempt paths via inline comment, but the regex exemption for `/:id/startup` at line 75 (`/^\/api\/machines\/[^/]+\/startup$/`) is not explained inline. A developer adding a new machine-to-API endpoint would not realize they need to add it to either `exemptPaths` or a new regex. — fix: Add a comment above the regex line: `// Machine startup script (GET) is exempt: authenticated via Bearer machine token, not cookies` and above `exemptPaths`: `// Add new Bearer-token-auth paths here — these bypass cookie-based CSRF protection` — effort: trivial

- [ ] **MEDIUM** `ember/devbox/src/rate-limit.ts:116-136` — The exported `rateLimit` factory function has no JSDoc. The `name` parameter (used as the bucket name) is undocumented — callers don't know whether names must be unique across the app, or what happens if two limiters share the same name (they'd share their hit counts, silently merging traffic). — fix: Add JSDoc: `/** Creates a rate limit middleware. name is the bucket identifier — use unique names per endpoint group. Limiters with the same name share hit counts. Defaults key by client IP. */` — effort: trivial

- [ ] **MEDIUM** `ember/devbox/src/github-app.ts:64-66` — `getInstallationForOrg` delegates entirely to `getInstallationForUser` with a comment "Same API, same logic," but this is subtly misleading. For orgs with Business/Enterprise plans, the installations API may return different results. The comment suggests equivalence where there may be edge cases. — fix: Change the comment to: `// Org installations use the same API endpoint. Note: org-level installations may require the org admin to have approved the GitHub App.` — effort: trivial

- [ ] **MEDIUM** `stoke/internal/remote/session.go:13-36` — `SessionReporter`, `TaskProgress`, and `SessionUpdate` are exported types used to report live progress to Ember's dashboard. None have doc comments. A developer integrating a new workflow runner won't know which fields are required vs optional, or what `BurstWorkers` means (it's the count of parallel Flare microVM workers currently active). — fix: Add struct-level and field-level comments. Minimum: doc `SessionReporter` (nil-safe: all methods check `r == nil`), doc `BurstWorkers` as parallel remote worker count, doc `TaskProgress.Phase` as the current workflow phase name. — effort: small

- [ ] **MEDIUM** `flare/internal/networking/tap.go:32-38` — `Manager` struct has no doc comment. The relationship between `nextIP`, `freeIPs`, and the allocation strategy (recycled IPs from released TAPs are preferred over sequential allocation) is not explained at the struct level. This matters when debugging IP exhaustion or TAP naming collisions. — fix: Add `// Manager allocates TAP devices and /24 IP addresses for VM guests. IPs are recycled on Release to avoid exhaustion. Supports up to 253 concurrent VMs per bridge (/24 minus gateway .1 and broadcast .255).` — effort: trivial

- [ ] **MEDIUM** `ember/devbox/src/fly.ts:109-120` — `CreateMachineOpts` is an exported interface with no JSDoc on any fields. `imageDigest` in particular is non-obvious: it must be a Docker image digest (sha256:...), not a tag, because tags are mutable. Using a tag would cause fleet drift. `apiUrl` vs `INTERNAL_API_URL` is also confusing — the env var comment on line 237 explains this but the interface field doesn't. — fix: Add JSDoc comments to the interface: `/** imageDigest: full Docker image digest (sha256:...) — tags are mutable and cause fleet drift. apiUrl: internal Fly 6PN URL for machine-to-API calls, falls back to public URL. */` — effort: trivial

---

## Patterns

1. **Opaque enum constants** — Both stoke (`AuthModeMode1/2`, `Status` int enum) and subscriptions (`PoolStatus`) use bare constants where the operational meaning isn't evident from the name alone. Go's convention of `// ConstName meaning` on each iota line is underused.

2. **Public interface fields without contracts** — `workflow.Engine`, `compute.SpawnOpts`, `remote.SessionReporter` all expose exported structs that are constructed directly by callers, but field-level optionality and zero-value behavior is undocumented.

3. **Security-relevant silent fallbacks** — `encryptSecret` silently skips encryption without APP_ENCRYPTION_KEY. `csrfProtection` has hidden exempt regexes. `internalKey` empty string disables host auth. These are defensible design choices that need explicit documentation precisely because the failure mode is invisible.

4. **Fly API wrapper return contract** — `flyApi` returning `null | {ok:true} | ParsedJSON` is spread through ~20 call sites with null checks but no documentation of the tri-state contract at the source.
