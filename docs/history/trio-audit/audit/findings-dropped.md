# Dropped Findings
**TIER 3 — filtered out as low ROI. These do not become tasks.**

| ID | Reason |
|----|--------|
| EMBER-022 | Hono Variables typed as `user: any` — type cleanup pattern migration; current code works, runtime unaffected |
| EMBER-023 | flyApi returns `Promise<any>` — type safety improvement on working code; medium effort for marginal runtime gain |
| EMBER-024 | Admin credit adjustment idempotency key uses Date.now() — admin-only endpoint, collision risk is theoretical |
| EMBER-035 | Fly provisioning failure partial-cleanup is non-atomic — medium effort for a rare crash-window edge case; background job is a new system |
| EMBER-036 | No .env.example file — documentation/onboarding nit, not a runtime problem |
| EMBER-046 | Admin user listing has no pagination limit — admin-only endpoint, DoS risk is self-inflicted |
| EMBER-047 | CSRF exemption list uses exact string match — fragile pattern but not currently broken |
| EMBER-050 | Terminal session creation has no deduplication — double-click creates extra rows but no user-facing bug |
| EMBER-052 | Ban handler iterates machines sequentially — admin-only operation, timeout risk is low with typical machine counts |
| EMBER-054 | Worker region hardcoded to "sjc" — feature gap, not a bug; US-only is current product scope |
| EMBER-056 | Workers and Managed AI feature flags undocumented — README documentation nit |
| EMBER-057 | rawSql.begin callbacks typed as `tx: any` — type cleanup; current code works |
| EMBER-058 | GitHub API responses cast to `any` — type safety improvement; runtime errors are edge cases handled by existing error paths |
| EMBER-059 | encryptSecret silently returns plaintext when key unset — documented behavior, not a bug in current deployment |
| EMBER-019 | Postgres rate limiter runs INSERT+COUNT+DELETE per request — medium effort to replace; current approach works at current scale |
| FLARE-019 | Exec endpoint disabled (501) but SDK exposes methods — endpoint is disabled, SDK stubs are confusing but not harmful |
| FLARE-026 | Hostname resolution queries DB on every proxied request — performance optimization; not yet at scale where this matters |
| FLARE-027 | Reconciler cleanup not atomic — acknowledged as safe in practice since retry is idempotent |
| FLARE-029 | No CI pipeline — process improvement, not a code defect |
| FLARE-031 | Manager has only one test — test coverage gap but medium effort and no current regression |
| STOKE-017 | AuthModeMode1/Mode2 are opaque names — naming/comment preference |
| STOKE-018 | workflow.Engine struct has zero doc comments — missing docs on internal struct |
| STOKE-019 | EmberBackend.Name() returns "flare" — cosmetic naming mismatch in logs |
| STOKE-020 | flareWorker.Stdout() returns empty reader — medium effort feature gap, not a bug |
| STOKE-021 | Retry loop re-uses same worktree name — edge case on crash restart; worktree cleanup handles this |
| STOKE-024 | advanceState errors systematically discarded — logging improvement; state machine still functions |
| CROSS-005 | Root README is unfilled template — documentation nit |
| CROSS-010 | stoke README command count and package count are stale — documentation nit |
