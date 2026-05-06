<!-- STATUS: blocked -->
<!-- CREATED: 2026-05-05 -->
<!-- BLOCKED_REASON: 2026-05-05 build attempt: vitest 4 pulls rolldown which fails to load native bindings on Node 20.18 (local env). vitest 3 + jsdom 29 surfaced unhoisted-dep issues (missing convert-source-map) under npm 9 workspaces on Node 20.18. Both work on CI (Node 22.13) but need local-env validation gate. Path forward: bump engines.node from >=20 to >=22.12 across all workspace package.json first, require local devs to upgrade, then retry this spec. -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 33 -->

# Dep bumps unblocked by Node 22 LTS CI (D-UI2-1 follow-up)

## 1. Overview

The Node 22 LTS CI bump (PR #161) cleared the engine floor that several deferred dependency upgrades were waiting on. Now that the workspace + CI run on Node 22, four toolchain packages can be bumped to versions that need Node `^20.19.0` or `^22.x`.

Per `docs/decisions/index.md` D-UI2-1 the deferred bumps are:

- **jsdom 26.x → 29.x** (vitest's environment for DOM-dependent tests)
- **vitest 2.x → 4.x** (also brings vite peer to 6.x or 7.x; current root override pins vite 6)
- **@vitest/coverage-v8 → 4.x** (paired with vitest 4)
- **vite 6.x → 7.x** (root override; vite 7 needs Node `^20.19.0` which Node 22 satisfies)

This spec ships them as one rolled-up PR rather than four sequential PRs because the four packages have intertwined peer-dep matrices — bumping any one in isolation would force a downgrade of one of the others. One bump-test-fix cycle is faster than four.

## 2. Stack & Versions

| Package | From | To |
|---|---|---|
| jsdom | ^26.1.0 | ^29.0.0 |
| vitest | ^2.1.0 | ^4.0.0 |
| @vitest/coverage-v8 | ^2.1.0 | ^4.0.0 |
| vite (root override) | ^6.4.0 | ^7.0.0 |

Pin source: latest stable on npm at scope-time. Final exact-versions land in `package.json` + `package-lock.json` after `npm install` resolves.

## 3. Architecture impact

None. These are toolchain-only bumps — the production runtime never sees them. Test outputs stay byte-identical when the tests don't exercise jsdom/vitest internals; assertions that DO depend on internal behaviour (e.g. asserting on synthetic-event ordering) get pinned to the new behaviour in the same commit.

## 4. Boundaries

- **No production code changes.** If a test fails after the bump, fix the TEST, not the production code.
- **No additional bumps.** This spec covers the four named packages. React 19 / streamdown / lucide-react etc. are NOT in scope.
- **No tsconfig changes.** The dual-vite tsconfig workaround landed in #164 stays in place (drop vite.config.ts + vitest.config.ts from include) until vitest 4 makes the dual-vite issue moot — verify in T3 whether the workaround can be reverted.

## 5. Implementation checklist (5 items — self-contained)

### Bump

- [ ] T1 — Edit `web/package.json`: change `vitest` from `^2.1.0` (or current pin) to `^4.0.0`, change `@vitest/coverage-v8` to `^4.0.0`, change `jsdom` to `^29.0.0`. Edit root `/home/eric/repos/r1-agent/package.json` `overrides.vite` from `^6.4.0` to `^7.0.0`. Run `npm install --workspaces --include-workspace-root --no-audit --no-fund` from repo root. Commit message: `chore(deps): bump vitest 4 + jsdom 29 + vite 7 (post-Node-22)`.
- [ ] T2 — Run `cd web && npx tsc --noEmit -p .` — clean. If the dual-vite issue from #164 is gone (vitest 4 ships vite 6), revert `web/tsconfig.json` to include `vite.config.ts` + `vitest.config.ts` again. Commit message: `chore(web): re-enable tsc on vite/vitest configs (vitest 4 dedupes vite)`.

### Verify

- [ ] T3 — Run `npm --prefix web run build` — clean (asserts vite 7 still produces `internal/server/static/dist/index.html`). If vite 7 broke any plugin, fix or pin the plugin. Commit message: `chore(web): adapt to vite 7 build (if any plugin updates needed)`.
- [ ] T4 — Run `npm --prefix web run test` — clean (vitest 4 against jsdom 29). Any test breakage MUST be patched in the test (this spec's §4 boundary). Commit message: `test(web): adapt vitest 4 + jsdom 29 (assertions only, no prod changes)`.
- [ ] T5 — Run `npm --prefix web run test:e2e` if Playwright is installed (skip with note if not — release-rehearsal lane only). Verify the worker test (`graph-worker.test.ts` from Spec 5) still passes; vitest 4's worker support is more reliable than vitest 2 + Node 20 was. Commit message: `test(web): verify graph-worker test still passes under vitest 4`.

## 6. Acceptance

- `npm install` clean across all four workspaces (web, desktop, packages/web-components, root).
- `cd web && npx tsc --noEmit -p .` clean.
- `npm --prefix web run build` clean (dist/index.html populated).
- `npm --prefix web run test` clean.
- `go build ./... && go vet ./... && go test ./cmd/r1-server/...` clean (Go-side embedded vendor + smoke tests still work).
