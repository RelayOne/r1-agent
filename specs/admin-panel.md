<!-- STATUS: done -->
<!-- CREATED: 2026-05-11 -->
<!-- BUILD_COMPLETED: 2026-05-14 -->
<!-- DEPENDS_ON: relayone-sso -->
<!-- BUILD_ORDER: 37 -->
<!-- IMPLEMENTATION_NOTE_2026-05-15: The hosted public admin URL exists, but the current r1-admin service is still scaffold-heavy and should not be described as fully complete. See plans/TRUTH-STATE-2026-05-15.md. -->

# Admin Panel at `admin.r1.run` — Implementation Spec

## 1. Overview

SOW item A5 calls for an admin panel at `admin.r1.run` that surfaces R1 operational state: live sessions, tenants (introduced by A4 RelayOne SSO), billing rollups (from `internal/costtrack`), audit/ledger browsing (from `internal/ledger`), and anti-truncation events (from `internal/antitrunc`). Phase 1 is **read-only**; mutation surfaces ship in Phase 2 (Q3 2026, separate spec).

### 1.1 Template-selection decision: do NOT clone a `*-admin` template

Historical note as of 2026-05-15: the repo later gained a separate
hosted `services/r1-admin/` deployment. The design discussion below is
still useful context for why the original work stayed in Go templates,
but it no longer describes the exact live hosted topology.

The SOW lists `cloudswarm-admin`, `relaygate-admin`, `coderadar-admin`, `truecom-admin`, `veritize-admin` as candidate Next.js admin templates. After reading their `package.json` (all Next.js 14.2.29 + pg + jose + bcrypt) and `next.config.js` (all `output: "standalone"` on Cloud Run port 8080), they are coherent siblings — but R1 has a **different precedent that overrides the template choice**:

`cmd/r1-server/main.go` already serves an htmx-based session viewer at `platform.r1.run`. The `r1-server-ui-v2-foundation` spec (STATUS: done, build_order 28) shipped a vendored htmx 2.x + Go `html/template` `base.html` toolchain. At the time this spec was written, adding a parallel Next.js+Node stack just for admin pages would (a) double the deploy surface, (b) fork the auth path away from the Go middleware that A4 lands, (c) duplicate API consumers that already exist as Go handlers (`/api/sessions`, `/api/dashboard/cost`, ledger handlers), and (d) violate the then-current "reuse r1-server" boundary in the SOW.

**Decision at authoring time: extend the existing htmx surface in `cmd/r1-server/`.** The choice was justified by:

1. SOW boundary "DO NOT spin up a new Cloud Run service" maps cleanly to sub-paths.
2. The Go server already owns the data sources (sessionhub, costtrack, ledger, antitrunc) — no IPC needed.
3. `cmd/r1-server/templates/` + the v2 `base.html` foundation already exists; admin views are a thin partial set.
4. A4 SSO middleware (Go, JWT) protects everything in one place.

The `*-admin` templates remain a useful **visual** reference (sidebar layout, card grids, table patterns); we port their Tailwind-inspired CSS into a single `admin.css` served from `cmd/r1-server/ui/web/css/` but do not pull in Node, React, or Next.

### 1.2 What this spec ships

- 8 new read-only HTTP routes under `/admin/*` in `cmd/r1-server/`.
- 6 paired JSON API endpoints under `/api/admin/*` consumed by htmx swaps.
- Admin-only auth middleware (`internal/server/admin_middleware.go`) layered on A4's SSO JWT.
- htmx templates extending the existing v2 `base.html` plus a new `admin-base.html`.
- Historical plan: Cloudflare CNAME `admin.r1.run` → `ghs.googlehosted.com`, with a Cloud Run domain mapping on the existing `r1-server` service. The live hosted admin surface later diverged to `services/r1-admin/`.
- Phase-2 mutation buttons rendered DISABLED with a contractual tooltip.
- Audit trail: every admin page view emits a `LedgerNode: AdminViewed{path, user, ts}`.
- Documentation, unit tests, golden HTML snapshots, and a Playwright + axe-core E2E lane in the existing release-rehearsal CI workflow.

## 2. Stack & Versions

| Concern | Choice | Notes |
|---|---|---|
| Server | Go 1.22+, `cmd/r1-server/main.go` | Existing |
| Templates | `html/template` extending `base.html` | Shipped in `r1-server-ui-v2-foundation` |
| Frontend | htmx 2.x (vendored), no React/Next | Per UI-v2 decision |
| Styling | Hand-rolled CSS in `cmd/r1-server/ui/web/css/admin.css`, dark theme matching `index.tmpl` | No Tailwind toolchain |
| Auth | A4 RelayOne SSO JWT, `role=admin` claim | DEPENDS_ON `relayone-sso` |
| Tests | `testing` + `httptest`; Playwright + `@axe-core/playwright` for E2E | Existing release-rehearsal lane |
| Deploy | Cloud Run service `r1-server`, region `us-central1` | Reuse |
| DNS | Cloudflare CNAME → `ghs.googlehosted.com` | Per Google docs (2026) |

## 3. Existing Patterns to Follow

- HTTP mux registration: `cmd/r1-server/main.go` (search for `mux.HandleFunc("GET /api/sessions", ...)`).
- Content negotiation (HTML vs JSON on same path): `cmd/r1-server/index.go`.
- Auth wrap: `internal/server/auth_middleware.go` (`requireBearer`); admin middleware composes alongside.
- htmx templates: `cmd/r1-server/templates/index.tmpl`, `session_row.tmpl`.
- v2 foundation: `cmd/r1-server/ui/web/base.html`, vendor at `cmd/r1-server/ui/web/vendor/htmx.min.js`.
- Cost data: `internal/server/dashboard.go::handleCost` (already wired through `costtrack.Tracker`).
- SessionHub list snapshot: `internal/server/sessionhub/sessionhub.go::List()`.
- Ledger node type registration: `internal/ledger/nodes/nodes.go` (add new `AdminViewed` type).
- Cloud Build: pattern from `cloudswarm-admin/cloudbuild.yaml` informs domain-mapping step; r1-server has its own build file.

## 4. Data Models

### 4.1 `AdminViewed` ledger node (new)
| Field | Type | Notes |
|---|---|---|
| `Path` | `string` | e.g. `/admin/sessions` |
| `User` | `string` | SSO subject (email) |
| `Tenant` | `string` | tenant slug from JWT |
| `Timestamp` | `time.Time` | server clock |
| `RemoteAddr` | `string` | redacted to /24 |

### 4.2 Tenant (read-only projection — provided by A4)
| Field | Type | Notes |
|---|---|---|
| `Slug` | `string` | unique, lowercase |
| `DisplayName` | `string` | |
| `CreatedAt` | `time.Time` | |
| `AdminEmails` | `[]string` | from A4 SSO directory |

If A4 does not yet ship a tenant store, fall back to a static `tenants.json` loaded at startup (documented limitation; promoted to DB after A4 lands).

### 4.3 Anti-trunc event view
| Field | Type | Source |
|---|---|---|
| `EventID` | `string` | bus message id |
| `SessionID` | `string` | |
| `TriggerPhrase` | `string` | from `antitrunc/phrases.go` |
| `OutcomeNodeCID` | `string` | ledger CID of the resolution node |
| `Timestamp` | `time.Time` | |

## 5. Routes (8 read-only)

| Method | Path | Renders | Source |
|---|---|---|---|
| GET | `/admin` | dashboard with KPI cards | sessionhub, costtrack, antitrunc bus |
| GET | `/admin/sessions` | paginated list (filters: tenant, status, age) | sessionhub.List() |
| GET | `/admin/sessions/{id}` | detail | sessionhub.Get() + ledger |
| GET | `/admin/tenants` | list | tenant store |
| GET | `/admin/tenants/{id}` | detail (membership, monthly spend, session count) | tenant + costtrack + sessionhub |
| GET | `/admin/billing` | month-over-month rollup + CSV export (`?format=csv`) | costtrack |
| GET | `/admin/audit` | paginated ledger node browser (filter by node type) | ledger.Store |
| GET | `/admin/anti-trunc-events` | paginated event stream | antitrunc + bus |

JSON twins: `/api/admin/{sessions|tenants|billing|audit|antitrunc}` — same query params, `Accept: application/json`.

## 6. Auth Gate Contract

`internal/server/admin_middleware.go`:

1. Extract bearer JWT from `Authorization` header or `r1_session` cookie.
2. Verify with A4 SSO public key (key id from JWT header; jwks fetched at startup, cached 24h).
3. Require `role=admin` claim.
4. On missing/expired token: HTTP 302 → `/auth/sso/start?next=<originalpath>`.
5. On valid token without `role=admin`: HTTP 403 + JSON `{"error":"forbidden","reason":"admin_role_required"}`.
6. On valid admin token: set `r.Context()` values `user`, `tenant`, `roles`; call next; emit `AdminViewed` ledger node async.

## 7. Cloud Run Domain Mapping

```bash
gcloud beta run domain-mappings create \
  --service=r1-server \
  --domain=admin.r1.run \
  --region=us-central1 \
  --project=relayone-488319
```

Cloudflare DNS (proxy OFF — Cloud Run terminates TLS):

```
admin.r1.run.   CNAME   ghs.googlehosted.com.   (DNS-only)
```

Domain ownership of `r1.run` already verified (per 9-service convention). The mapped domain serves `/admin/*` and `/api/admin/*`; root paths still serve via `platform.r1.run`. The Go server inspects `Host` and (optionally) returns 404 for non-admin paths on `admin.r1.run` — this is a soft guard, not a security boundary (auth still enforced everywhere).

## 8. Boundaries — What NOT To Do

- Do NOT add any mutation endpoints. POST/PUT/PATCH/DELETE under `/admin/*` are explicitly Phase 2 (Q3 2026).
- Do NOT spin up a new Cloud Run service. Reuse `r1-server`.
- Do NOT duplicate session, ledger, or cost storage. Read-only consumers.
- Do NOT build a new auth system. Reuse A4 SSO JWT with `role=admin`.
- Do NOT introduce Node/React/Next/Tailwind toolchains. Vendored htmx + hand-rolled CSS only.
- Do NOT clone `*-admin` repos. They are visual reference only.
- Do NOT expose raw ledger node JSON containing redacted-pending content; pass through existing `ledger.Redact` path.

## 9. Acceptance Criteria — measurable

- WHEN an unauthenticated request hits `/admin/*` THE SYSTEM SHALL return HTTP 302 to `/auth/sso/start?next=<path>`.
- WHEN a valid SSO user lacking `role=admin` hits `/admin/*` THE SYSTEM SHALL return HTTP 403.
- WHEN a valid admin hits any of the 8 routes against staging data (10k sessions / 100 tenants) THE SYSTEM SHALL respond in <500ms p50 and <2s p99.
- WHEN any `/admin/*` page renders THE SYSTEM SHALL emit exactly one `AdminViewed` ledger node within 1s.
- WHEN Playwright + axe-core run the 8 routes THE SYSTEM SHALL report zero serious/critical accessibility violations.
- WHEN the CSV export is requested for billing THE SYSTEM SHALL stream with `Content-Type: text/csv; charset=utf-8` and `Content-Disposition: attachment; filename="billing-YYYY-MM.csv"`.
- WHEN a mutation button is rendered THE SYSTEM SHALL set `disabled` + `aria-disabled="true"` and a tooltip "Phase 2 — Q3 2026".

## 10. Implementation Checklist

1. [ ] **Tenant store interface.** Create `internal/tenants/tenants.go` exposing `type Store interface { List() []Tenant; Get(slug string) (Tenant, bool) }` and a `StaticStore` backed by `~/.r1/tenants.json` for the pre-A4-DB period. Add unit tests for empty file, malformed file, and happy path. Document the migration to A4's tenant DB as a TODO comment referencing `specs/relayone-sso.md`.

2. [ ] **Admin auth middleware.** Implement `internal/server/admin_middleware.go::RequireAdmin(next http.Handler) http.Handler`. Verify JWT via JWKS fetched from `A4_SSO_JWKS_URL` env, cached 24h, with single-flight refresh on key rotation. Behavior matrix: no token → 302 to `/auth/sso/start?next=<urlencode(path)>`; expired → same 302; valid no-role → 403 JSON; valid admin → pass-through with context values `user`, `tenant`, `roles`. Constant-time string compare for the role check. Unit tests for each behavior plus a "JWKS unavailable" failure mode (returns 503).

3. [ ] **Ledger `AdminViewed` node type.** Add `internal/ledger/nodes/admin_viewed.go` defining the struct in §4.1 with `Type() = "admin_viewed"`. Register in the `nodes.go` switch table. Test round-trip encode/decode + index by `User` and `Path`.

4. [ ] **Emit-on-view hook.** Wrap the admin handlers with a deferred goroutine that calls `ledger.Emit(ctx, AdminViewed{...})` after the response completes. Use the existing ledger writer; do not block request latency. Redact `RemoteAddr` to /24 before emit. Tests assert one emission per request and zero on auth failures (we don't log unauthorized scans).

5. [ ] **Dashboard route `GET /admin`.** Handler in `cmd/r1-server/admin_handlers.go`. KPI cards: (a) active sessions from `sessionhub.List()` filtered by `State == "running"`; (b) month-to-date USD from `costtrack.Tracker.Total()` scoped to current month; (c) anti-trunc events count from the last 24h via `bus.Subscribe("antitrunc.*")` snapshot. Template at `cmd/r1-server/templates/admin/dashboard.tmpl` extending `admin-base.html`. JSON twin returns same shape.

6. [ ] **Sessions list `GET /admin/sessions`.** Paginated (default 50/page, max 200), query params `?tenant=&status=&age_lt=`. Source `sessionhub.List()` (returns slice — fine for 10k; revisit at 100k). Sort by `StartedAt` desc. htmx hx-get every 5s. Template `templates/admin/sessions_list.tmpl` + partial `templates/admin/sessions_rows.tmpl` for the swap target.

7. [ ] **Session detail `GET /admin/sessions/{id}`.** Show: SessionRoot, Model, State, StartedAt, recent N events (from journal), ledger CIDs touched, total cost (from `costtrack.ByTask()`). Link to existing platform.r1.run trace waterfall. Template `templates/admin/session_detail.tmpl`. Return 404 if id unknown via `sessionhub.Get`.

8. [ ] **Tenants list `GET /admin/tenants`.** Render table from `tenants.Store.List()`. Each row links to detail. Phase-2 "Suspend Tenant" button: disabled + tooltip per §9.

9. [ ] **Tenant detail `GET /admin/tenants/{id}`.** Show: AdminEmails, CreatedAt, session count (filter `sessionhub.List()` by `Tenant == slug`), monthly spend (sum `costtrack` events tagged with tenant). Phase-2 disabled buttons: "Revoke Admin", "Edit Quota". Template `templates/admin/tenant_detail.tmpl`.

10. [ ] **Billing route `GET /admin/billing`.** Month-over-month rollup table for last 12 months. Use `costtrack.Tracker.ByModel()` + `ByTask()` aggregated by month boundary. Detect optional Cloud SQL `r1_costs` table via env `R1_COSTS_DSN`; if absent, render in-memory-only with a banner "Persistent billing requires r1_costs DB". CSV export at `?format=csv` streamed via `encoding/csv`. Template `templates/admin/billing.tmpl`.

11. [ ] **Audit route `GET /admin/audit`.** Paginated ledger node browser. Filter by `?type=` (multi-value), `?session=`, `?since=`. Read via `ledger.Store.Range(...)` — confirm read-only API exists; if not, add a `ledger.ReadOnlyRange` method that does not touch write locks. Link each row to the existing tracebundle export path at `/api/tracebundle?node=<cid>`. Template `templates/admin/audit.tmpl`.

12. [ ] **Anti-trunc events route `GET /admin/anti-trunc-events`.** Subscribe to `bus` topic `antitrunc.*` with a ring buffer of last 1000 events held in-process. Render paginated. Each row shows trigger phrase, session, resolution ledger CID. Template `templates/admin/antitrunc.tmpl`. The ring buffer is process-local (acceptable for Phase 1); document the limitation.

13. [ ] **JSON twins under `/api/admin/*`.** Each route negotiates via `Accept` header (HTML default; JSON if `application/json`). Reuse handler bodies; the negotiation lives at the top of each handler. Tests for both content types per route.

14. [ ] **`admin-base.html` template.** New file `cmd/r1-server/ui/web/admin-base.html` extending the v2 `base.html`. Adds a sidebar nav with 8 links, the logged-in-user pill (top-right), the tenant context indicator, and a feature-flag visible "Phase 1 — read-only" banner. Visual reference: cloudswarm-admin sidebar layout.

15. [ ] **`admin.css`.** New `cmd/r1-server/ui/web/css/admin.css` with the dark-theme tokens from `index.tmpl`. Sidebar (200px left), main grid for KPI cards, dense table styling, status badges. Inline `<style>` minimal — most rules live in this CSS file.

16. [ ] **Disabled mutation buttons.** Add a reusable Go template helper `{{ template "phase2_btn" "Edit Quota" }}` rendering `<button disabled aria-disabled="true" title="Phase 2 — Q3 2026" class="btn btn-disabled">Edit Quota</button>`. Use across all 8 routes wherever a mutation surface will eventually exist. List the contractual buttons per route in `docs/operations/admin-panel.md`.

17. [ ] **Cloud Run domain mapping.** Add to `cmd/r1-server/cloudbuild.yaml` a step that creates the domain mapping idempotently (gcloud `--ignore-existing` or check-then-create). Verify with `gcloud beta run domain-mappings describe admin.r1.run`. Document Cloudflare DNS step in `docs/operations/admin-panel.md` — proxy MUST be OFF (DNS-only), CNAME to `ghs.googlehosted.com`. Per Google's 2026 docs and the existing 9-service convention.

18. [ ] **Host-aware routing (soft).** In `cmd/r1-server/main.go`, when `r.Host` ends in `admin.r1.run` and path is not under `/admin/` or `/api/admin/` or `/auth/` or `/ui/`, return 404 with a small HTML body explaining the surface. UX guardrail, not a security boundary.

19. [ ] **Pagination helper.** Add `internal/server/admin_pagination.go` with `type Page struct { Offset, Limit int; Total int }` and HTML helpers for prev/next links. Constants: default 50, min 1, max 200. Unit tests for edge cases (negative, overflow, total=0).

20. [ ] **CSV streaming helper.** Add `internal/server/csv_export.go` wrapping `encoding/csv.Writer` with proper `Content-Disposition` and flush-per-row to avoid OOM on large exports. Unit test with 100k synthetic rows asserting constant-memory streaming.

21. [ ] **JWKS cache.** `internal/auth/jwks_cache.go` (new package `auth` — confirm absence first): fetches `A4_SSO_JWKS_URL`, caches 24h, single-flight refresh on `kid` miss, returns clear errors. Unit tests for cache hit, miss + refresh, network failure, malformed JWKS.

22. [ ] **Unit tests per handler.** For each of the 8 routes: happy path renders 200; unauth → 302; non-admin → 403; pagination boundaries; filter combinations. Place in `cmd/r1-server/admin_handlers_test.go`.

23. [ ] **Golden HTML snapshots.** Each route renders against a seeded fixture (`testdata/admin/fixtures.json`). Golden files at `testdata/admin/golden/<route>.html`. Update via `UPDATE_GOLDEN=1 go test ./cmd/r1-server -run TestAdmin`. Stable output: deterministic sort order, fixed time via injected clock.

24. [ ] **Playwright + axe-core E2E.** Add `cmd/r1-server/e2e/admin-e2e.mjs` modeled on existing `e2e-fullflow.mjs`. Sign in via fake SSO test harness (admin role), visit each of the 8 routes, run axe-core, assert zero serious/critical violations. Hook into `.github/workflows/e2e-rehearsal-manual.yml` as a new step `admin-e2e`. Smoke test (no Playwright install) skipped on the default lane.

25. [ ] **Integration test — auth gate.** `cmd/r1-server/admin_auth_integration_test.go`. Spin up server with a test JWKS, mint three JWTs (no-token, valid non-admin, valid admin), exercise all 8 routes, assert status codes per §9.

26. [ ] **Performance probe.** Add `scripts/admin-perf-probe.sh` that seeds 10k sessions + 100 tenants in a local server, hits each route 100x, reports p50/p99. Wire as opt-in CI gate behind `R1_PERF=1`. Hard-fail on >500ms p50 or >2s p99.

27. [ ] **Anti-trunc event ring buffer.** `internal/server/admin_antitrunc_buffer.go`. Size 1000, FIFO, thread-safe. Subscribes to `bus` at server startup. Snapshot method returns a copy for paginated render. Unit tests for wrap-around, concurrent subscribe/snapshot.

28. [ ] **Docs.** `docs/operations/admin-panel.md`: (a) sign-in flow walkthrough; (b) how to grant `role=admin` via A4 SSO directory; (c) how to revoke; (d) audit trail explanation (every view → ledger node); (e) Cloudflare CNAME setup; (f) Phase-2 button reference table; (g) troubleshooting "JWKS unavailable → 503". Include a screenshot placeholder block.

29. [ ] **Sibling-repo stub.** Create `specs/r1-admin-sibling-repo.md` with STATUS: rejected, documenting the choice in §1.1 above so future readers do not re-litigate the Next.js path. One paragraph + a link to this spec.

30. [ ] **CHANGELOG + announcement.** Append to repo CHANGELOG.md: "Admin panel Phase 1 at admin.r1.run (read-only). Sessions, tenants, billing, audit, anti-trunc events. Mutation surfaces deferred to Phase 2 (Q3 2026)." Note in the announcement that A4 RelayOne SSO is a hard dependency.

31. [ ] **Release-rehearsal CI lane wiring.** Update `.github/workflows/e2e-rehearsal-manual.yml` to invoke the new `admin-e2e` step after the existing fullflow. Ensure the runner has the admin test JWT secret available via `secrets.A4_TEST_ADMIN_JWT`. Document in the workflow comments.

32. [ ] **Self-review pass.** Before declaring done: (a) `go vet ./...` clean; (b) `go test ./...` green; (c) `go test -race ./cmd/r1-server/...` green; (d) golden snapshots stable across two consecutive runs; (e) Playwright run zero violations; (f) `scripts/admin-perf-probe.sh` passes; (g) `docs/operations/admin-panel.md` reviewed against the as-built; (h) `grep -rn "TODO\|FIXME" cmd/r1-server/admin*` empty.

## 11. Notes on Dependencies

- **A4 RelayOne SSO** (`specs/relayone-sso.md`): MUST land first. This spec assumes A4 provides:
  - `A4_SSO_JWKS_URL` env var,
  - JWT with `sub` (email), `tenant` (slug), `roles` (string array containing `admin` for admin users),
  - `/auth/sso/start?next=` endpoint to bounce unauth users into the SSO flow.
- If A4 is delayed, the admin panel can ship behind `R1_ADMIN_DEV_BYPASS=1` for local dev only — production deploys MUST set it to 0 and fail closed.

## 12. Out of Scope

- Tenant onboarding UI (Phase 2).
- Quota editing (Phase 2).
- Session termination / suspension (Phase 2).
- Real-time SSE streaming on admin pages (use the 5s htmx poll; SSE arrives in Phase 2 if needed).
- Per-tenant region pinning (cloudswarm has this; R1 is single-region until further notice).
- Multi-region image mirroring (cloudswarm-admin's pattern); not needed for a single-service sub-path mount.
