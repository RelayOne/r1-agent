<!-- STATUS: blocked -->
<!-- CREATED: 2026-05-05 -->
<!-- BLOCKED_REASON: 2026-05-05 build: deleting the 12 legacy SPA files broke 12 test assertions across ui_test.go / index_test.go / ui_vendor_test.go / trace_test.go (TestUIServesIndex, TestUIGraphHTMLServed, TestHTMXVendoredAssetServed, TestTraceWaterfallFlagOffServesSPA, etc). Tests assert specifically on v1 SPA paths and need per-test triage. Path forward: multi-PR refactor — first delete v1-specific tests (per-file PR with reviewer judgment), then delete source files. Too risky in a single PR. -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 36 -->

# Legacy v1 SPA cleanup (D-UI2-3 follow-up)

## 1. Overview

Spec 1 §8 (`r1-server-ui-v2-foundation.md`) committed to "two release cycles of parallel code" then a final cleanup that:
- Deletes `cmd/r1-server/ui/` (the legacy vanilla-JS SPA — `app.js`, `style.css`, `index.html`, `graph.html`, `graph.js`, etc.).
- Renames `cmd/r1-server/ui/web/` → `cmd/r1-server/ui/`.
- Removes the `R1_SERVER_UI_V2` feature flag — the v2 surface becomes the default.

The two release cycles have elapsed (the v2 surface shipped through PRs #154/#155/#156/#160/#162 + integration #167). This spec is the cleanup pass.

## 2. Stack & Versions

No new deps. This is pure deletion + renaming + dropping a feature flag.

## 3. Architecture impact

```
cmd/r1-server/ui/                       cmd/r1-server/ui/
├── app.js              ← DELETE        ├── README.md           (was ui/web/README.md)
├── style.css           ← DELETE        ├── base.html           (was ui/web/base.html)
├── index.html          ← DELETE        ├── partials/           (was ui/web/partials/)
├── graph.html          ← DELETE        ├── vendor/             (was ui/web/vendor/)
├── graph.js            ← DELETE        ├── css/                (was ui/web/css/)
├── graph.css           ← DELETE        ├── js/                 (was ui/web/js/)
├── ...legacy SPA bits  ← DELETE        ├── index.html          (was ui/web/index.html)
└── web/                ← RENAME up     ├── session.html        (was ui/web/session.html)
                                        ├── session-graph.html
                                        ├── session-stream.html
                                        ├── memories.html
                                        ├── share.html
                                        └── diff.html
```

Code paths that need to change:
- **`cmd/r1-server/ui.go`**: `//go:embed ui/*` already covers everything; just remove the `serveIndex` legacy fallback paths since v2 is now default.
- **`cmd/r1-server/ui_v2_foundation.go`**: `//go:embed ui/web/*.html ui/web/partials/*.html` → `//go:embed ui/*.html ui/partials/*.html`. `parseV2Templates` paths update accordingly.
- **`cmd/r1-server/ui_v2_flag.go`** (`V2Config.Enabled`, `LoadV2Config`, `Renderable`, `CanServeShare`): `Enabled` becomes hardcoded `true`. The `R1_SERVER_UI_V2` env var is dropped from `LoadV2Config`. Existing callsites (the `v2Enabled()` shim in share.go and `traceV2Enabled()` in trace.go) continue to compile against the new always-true return.
- **Documentation**: `cmd/r1-server/README.md` feature-flag table loses the row.
- **All v2 templates that reference `/ui/web/...`**: update to `/ui/...` (vendor scripts, css, js paths).

## 4. Boundaries

- **R1_SERVER_SHARE_ENABLED stays.** That's a separate gate, not the v2 mount toggle.
- **R1_SERVER_TRACE_STUB stays.** Dev-only flag, orthogonal.
- **R1_SERVER_SHARE_TEMPLATE_V2 stays.** That was an opt-in template-rollout knob and is conservative-by-design.
- **Don't delete tests.** Move v1-SPA-specific tests (if any) into a "legacy SPA" archive or delete only after verifying they aren't asserting against generic surface behaviour.
- **Don't break existing dist/ embedding.** The web/ workspace's `vite build` outputs to `internal/server/static/dist/` and serves that — independent of the cmd/r1-server/ui/ tree.

## 5. Implementation checklist (8 items — self-contained)

### Migration

- [ ] T1 — Identify the full list of legacy v1 SPA files in `cmd/r1-server/ui/`. Run `git ls-files cmd/r1-server/ui/ | grep -v "^cmd/r1-server/ui/web"` and cross-reference against the post-rename target list (everything currently in `cmd/r1-server/ui/web/`). Files to delete are: every legacy file that doesn't have a counterpart in `web/`. Make the explicit list in `plans/legacy-spa-files-to-delete.md` for review-before-deletion.
- [ ] T2 — `git mv cmd/r1-server/ui/web/* cmd/r1-server/ui/` (after T1 clears the target dir). Verify no merge conflicts via dry-run with `git status`. The cmd/r1-server/ui/ subdirs (vendor/, css/, js/, partials/) overwrite their legacy v1 counterparts cleanly because v1 had no such subdirs.
- [ ] T3 — `git rm` everything from T1's deletion list. One commit: `feat(ui): remove legacy v1 SPA + lift v2 from ui/web/ to ui/`.

### Source updates

- [ ] T4 — Update `cmd/r1-server/ui_v2_foundation.go`:
    * `//go:embed ui/web/*.html ui/web/partials/*.html` → `//go:embed ui/*.html ui/partials/*.html`
    * `parseV2Templates` paths: `ui/web/base.html` → `ui/base.html`, `ui/web/*.html` → `ui/*.html`, `ui/web/partials/*.html` → `ui/partials/*.html`
    * Same edit pattern in `ui/index.html`, `ui/session.html`, `ui/share.html`, `ui/diff.html`, etc.: every `/ui/web/vendor/...`, `/ui/web/css/...`, `/ui/web/js/...` URL becomes `/ui/vendor/...`, `/ui/css/...`, `/ui/js/...`.
    * Run `grep -rn "ui/web/" cmd/r1-server/` after the edits to confirm no stragglers.
- [ ] T5 — Drop the `R1_SERVER_UI_V2` flag from `cmd/r1-server/ui_v2_flag.go`:
    * `V2Config.Enabled` field stays for compile-time backward compat but `LoadV2Config()` always sets it to true.
    * Remove the env var read.
    * `Renderable()` returns true unconditionally.
    * Update `cmd/r1-server/share.go::v2Enabled()` to return true (or delete it + inline `true` at every callsite — pick one).
    * Update `cmd/r1-server/trace.go::traceV2Enabled()` similarly.
    * The grep guard test (`no_direct_env_test.go`) stays — it was forbidding direct env reads, which is still the right invariant for the remaining flags.

### Tests

- [ ] T6 — Run `go test ./cmd/r1-server/...` — every test that referenced the legacy SPA paths gets updated to the new flat-`ui/` paths. The CSP smoke test (`TestServeSmoke`) keeps its current assertion (CSP via meta in dist/index.html) — that's the React SPA, not the legacy vanilla-JS SPA, so it stays.
- [ ] T7 — Update `cmd/r1-server/sri_test.go` + `cmd/r1-server/vendor_freshness_test.go`: the vendor blob root moves from `cmd/r1-server/ui/web/vendor/` to `cmd/r1-server/ui/vendor/`. Single-line change in each.

### Documentation

- [ ] T8 — Update:
    * `cmd/r1-server/README.md` — drop the `R1_SERVER_UI_V2` row from the feature-flag table; update every `/ui/web/...` URL in the route table to `/ui/...`.
    * `docs/decisions/index.md` — append a D-UI2-7 entry recording the legacy-SPA deletion + the elapsed two-release-cycle gate.
    * `docs/FEATURE-MAP.md` — the v2 retrofit moves from "Recently shipped" to "Production default".
    * `cmd/r1-server/ui/README.md` (was `ui/web/README.md`) — strip the "parallel to legacy ui/" framing language.
    * `scripts/vendor-ui.sh` — `VENDOR=cmd/r1-server/ui/web/vendor` → `VENDOR=cmd/r1-server/ui/vendor`.

## 6. Acceptance

- `go build ./... && go vet ./... && go test ./cmd/r1-server/...` clean.
- `bash scripts/vendor-ui.sh --check` clean.
- `R1_SERVER_UI_V2` env var has zero hits in the source tree.
- `git ls-files | grep "ui/web"` returns zero rows.
- Cloud Build green on a fresh PR (the vendor freshness + sri + golden tests catch any path drift).
