# Legacy v1 SPA test-deletion triage (Spec D unblock)

Audit-only. No production or test files modified.

Date: 2026-05-06
Branch: `audit/legacy-spa-triage-2026-05-06` (off `main` @ `2a8e5d77`)
Spec frontmatter claim: "deleting the 12 legacy SPA files broke 12 test assertions"

## Scope

The 12 legacy SPA files (all under `cmd/r1-server/ui/`):

```
app.js                             graph-worker.js
graph.css                          graph.html
graph.js                           index.html
style.css                          vendor/3d-force-graph.min.js
vendor/README.md                   vendor/htmx.min.js
vendor/three-spritetext.min.js     vendor/three.min.js
```

## Branch-state correction

The dispatch said `git ls-files cmd/r1-server/ui/web/` returns ZERO files and concluded the spec's T2 "git mv ui/web/* ui/" step is moot. **That observation is true on `build/web-chat-ui` but FALSE on `main`** (where this audit was run, per dispatch step 4).

| branch | `ui/web/` tracked files | `ui/` legacy files |
|---|---|---|
| `main` (HEAD `2a8e5d77`)         | 38 | 12 |
| `build/web-chat-ui` (HEAD `a12d0bd1`) |  0 | 12 |

On `main` the v2 surface IS lifted under `ui/web/` (templates, css, js, vendor). The triage below uses `main` as base because that is what the spec is written against and what CI gates on.

## Empirical failure count: 14 (predicted: 12)

Empirically deleted the 12 files on a temp branch off `main`, then `go test ./cmd/r1-server/...`:

- `go build ./cmd/r1-server/` succeeds (no compile-time references to the deleted files).
- 14 test functions fail (not 12). The frontmatter undercounted by 2.

Failing tests, in source order:

1. `cmd/r1-server/index_test.go` — `TestIndexFallsBackToSPAWhenV2Disabled`
2. `cmd/r1-server/index_test.go` — `TestHTMXVendoredAssetServed`
3. `cmd/r1-server/trace_test.go` — `TestTraceWaterfallFlagOffServesSPA`
4. `cmd/r1-server/ui_test.go`    — `TestUIServesIndex`
5. `cmd/r1-server/ui_test.go`    — `TestUIServesStaticAssets`
6. `cmd/r1-server/ui_test.go`    — `TestUISPAFallbackForSessionPath`
7. `cmd/r1-server/ui_test.go`    — `TestUIGraphHTMLServed`
8. `cmd/r1-server/ui_test.go`    — `TestUIGraphJSServed`
9. `cmd/r1-server/ui_test.go`    — `TestUIGraphCSSServed`
10. `cmd/r1-server/ui_test.go`   — `TestUIGraphHTMLLoadsVendoredLibs`
11. `cmd/r1-server/ui_test.go`   — `TestUIGraphJSHasNodeStyleContract`
12. `cmd/r1-server/ui_test.go`   — `TestUIGraphRouteDoesNotShadowSPA`
13. `cmd/r1-server/ui_vendor_test.go` — `TestGraphHTMLNoCDNRefs`
14. `cmd/r1-server/ui_vendor_test.go` — `TestGraphHTMLReferencesVendorPaths`

Note: `vendor_check_test.go::TestCheckVendoredLibs_Missing` references `graph.html` only as fake `fstest.MapFS` fixture data (line 40); the real file's existence is irrelevant. That test does NOT fail. It is included in the grep hits but is not load-bearing on the deletion.

## Per-assertion triage

Classification key:
- (a) **Delete the test** — test purely exercises legacy-only behavior; v2 has no equivalent surface or asserts the opposite.
- (b) **Update the test** — assertion still meaningful against v2 surface; rewrite the path/marker.
- (c) **Reviewer judgment** — assertion encodes a real contract (e.g. NODE_STYLES, CDN ban) that should migrate; needs human decision on where it should live in v2.

| # | file:line | test | assertion summary | classification | recommended action |
|---|---|---|---|---|---|
| 1 | `index_test.go:116` | `TestIndexFallsBackToSPAWhenV2Disabled` | flag-off `/` body must contain `/ui/app.js` | (a) | Delete the test. Once the legacy SPA is gone there is no flag-off SPA fallback to test; v2 is the only surface. |
| 2 | `index_test.go:334-349` | `TestHTMXVendoredAssetServed` | GET `/ui/vendor/htmx.min.js` returns non-empty JS body | (b) | Repoint to `/ui/web/vendor/htmx.min.js` (v2 vendored copy already exists). Same intent — air-gap-able vendor asset. |
| 3 | `trace_test.go:144` | `TestTraceWaterfallFlagOffServesSPA` | flag-off `/session/:id` body contains `/ui/app.js`; must NOT contain `class="waterfall"` | (a) | Delete the test. Same rationale as #1 — no SPA fallback when legacy SPA is removed. |
| 4 | `ui_test.go:42` | `TestUIServesIndex` | GET `/` body contains `/ui/app.js` | (a) | Delete. Whole-test purpose is "legacy SPA shell renders." Replaced by v2 golden + htmx_shell tests. |
| 5 | `ui_test.go:49-58` | `TestUIServesStaticAssets` | GET `/ui/app.js` and `/ui/style.css` 200 | (a) | Delete. Files don't exist post-deletion. v2 assets are tested via golden + ui_v2_foundation tests. |
| 6 | `ui_test.go:74` | `TestUISPAFallbackForSessionPath` | GET `/session/r1-...` 200 with body containing `r1-server` | (a) | Delete. Test boots `newUIServer` with no `R1_SERVER_UI_V2` env, so it goes through the legacy `serveIndex` → reads `index.html`. Without the file the entire test 500s. v2 has explicit `/session/{id}` waterfall handler. |
| 7 | `ui_test.go:112-121` | `TestUIGraphHTMLServed` | GET `/session/abc/graph` body contains `graph.js`, `Ledger Graph`, NOT `/ui/app.js` | (b) | Repoint to v2 `session-graph` template (`serveSessionGraph` already serves this when v2=1). Markers shift from `graph.js`/`Ledger Graph` to v2 template fragments — see `golden_test.go::TestGolden_SessionGraph`. |
| 8 | `ui_test.go:128-143` | `TestUIGraphJSServed` | GET `/ui/graph.js` returns body containing `ForceGraph3D` | (b) | Repoint to `/ui/web/js/graph.js` (the v2 file). Same JS, new path. |
| 9 | `ui_test.go:152-173` | `TestUIGraphCSSServed` | GET `/ui/graph.css` 200; body contains `#graph`, `#sidepanel`, `#tooltip`, `#fallback` | (b)/(c) | Repoint to `/ui/web/css/base.css` or whatever v2 stylesheet owns those selectors; if v2 uses different selectors, this is (c) — reviewer must confirm v2 layout uses equivalent slots. |
| 10 | `ui_test.go:194-207` | `TestUIGraphHTMLLoadsVendoredLibs` | graph.html references the three `/ui/vendor/*.min.js` paths and NOT `unpkg.com`/`cdn.jsdelivr.net`/`@latest` | (b) | Repoint to v2 `session-graph.html` template + new `/ui/web/vendor/three*.js` paths. CDN-ban half is still a useful contract. |
| 11 | `ui_test.go:225-252` | `TestUIGraphJSHasNodeStyleContract` | graph.js declares NODE_STYLES for 16 node types + 7 edge types + `detectWebGL` + `showFallback` | (c) | **Highest-value assertion in the set** — encodes RS-4 item 20 spec contract. Reviewer must decide where this lives in v2: `ui/web/js/graph.js` (file exists), or moved into `graph-layers.js`. Don't drop blindly. |
| 12 | `ui_test.go:269-285` | `TestUIGraphRouteDoesNotShadowSPA` | precedence: `/session/abc/graph` serves graph.html, `/session/abc` serves SPA index.html | (b) | First half migrates to v2 template; second half (`/session/abc` → SPA) is (a) — no SPA fallback any more. Net effect: split into a single v2 precedence test. |
| 13 | `ui_vendor_test.go:42-54` | `TestGraphHTMLNoCDNRefs` | embedded `graph.html` does NOT reference `unpkg.com` or `cdn.jsdelivr.net` | (b) | Repoint to embedded `web/session-graph.html`. CDN ban still meaningful. |
| 14 | `ui_vendor_test.go:62-76` | `TestGraphHTMLReferencesVendorPaths` | embedded `graph.html` references the three `/ui/vendor/*.min.js` paths | (b) | Repoint to v2 vendor paths under `/ui/web/vendor/three*.js` (UMD → ESM rename: `three.min.js` → `three.module.js`, `three-spritetext.min.js` → `three-spritetext.js`, `3d-force-graph.min.js` → `3d-force-graph.js`). |

### Breakdown

| classification | count | tests |
|---|---|---|
| (a) delete | 5 | #1, #3, #4, #5, #6 |
| (b) update | 7 | #2, #7, #8, #9, #10, #12 (partial), #13, #14 (counts #12 once → 7) |
| (c) reviewer | 2 | #9 (if v2 selectors differ) and #11 |

Counted strictly: 5(a) / 7(b) / 2(c) = **14 assertions / tests**.

`#9` straddles (b)/(c) — if v2 reuses the same CSS selectors for the side panel, it's (b); if not, the selector list itself is the v2 contract and reviewer must remap.

## v2-OFF behavior section

Read of `cmd/r1-server/ui_v2_flag.go` + dispatch sites:

- `LoadV2Config().Renderable()` returns `Enabled`, which is strict equality `os.Getenv("R1_SERVER_UI_V2") == "1"`.
- Dispatch sites when `Renderable() == false`:

| handler | flag-off path | post-deletion behavior |
|---|---|---|
| `serveSessionGraph` (`ui_v2_foundation.go:189`) | calls `serveGraphIndex` → `fs.ReadFile(uiFS, "graph.html")` | **500** ("graph ui missing: file does not exist") |
| `serveMemoryGraph` (`ui_v2_foundation.go:231`) | `http.NotFound` | 404 (safe; no panic) |
| `serveStreamView` (`stream_view.go:38`) | `http.NotFound` | 404 (safe) |
| `serveShare` / `v2Enabled()` (`share.go:207`) | `http.NotFound` | 404 (safe) |
| `serveDiff` (`diff.go:210`) | falls through to legacy diff HTML | Still works (legacy diff doesn't read deleted files) |
| `tracebundle` flag-off (`tracebundle.go:102`) | `http.NotFound` | 404 (safe) |
| `serveHTMLIndex` (`index.go:197`) flag-off | calls `serveIndex` → `fs.ReadFile(uiFS, "index.html")` | **500** ("ui missing: file does not exist") |
| `db.serveTraceWaterfall` (flag-off) | calls `serveIndex` (per `trace_test.go:144`) | **500** (same reason) |
| `mux GET /session/` registered to `serveIndex` directly | `fs.ReadFile(uiFS, "index.html")` | **500** for any `/session/...` GET |
| `mux GET /ui/` static handler | `http.FileServer(http.FS(uiFS))` | **404** for the deleted asset paths |

**v2-OFF risk: HIGH (until legacy deletion is paired with the V2-mandatory cutover).**

The v2-OFF code paths panic-via-500 on the index/SPA/legacy-graph routes when the embedded files are gone. Flag-off operators today get a working SPA; flag-off operators after deletion get a 500 on `/`, `/session/...`, and `/session/{id}/graph`. Three remediations possible (reviewer's call):

1. **Cutover**: bake `R1_SERVER_UI_V2=1` as default and remove the flag entirely (see `share.go:207` `v2Enabled()` for the choke point — flip default to true).
2. **Replace fallback bodies**: have `serveIndex`, `serveGraphIndex`, the `serveTraceWaterfall` flag-off branch, and the `serveSessionGraph` flag-off branch render a small "v2-only — set R1_SERVER_UI_V2=1" placeholder instead of `fs.ReadFile`.
3. **Branch-precedence detection**: the `mountUI` `db != nil` guard plus the explicit `mux.HandleFunc("GET /session/", serveIndex)` registration always wires the legacy fallback. Add a v2-aware switch at mount time so flag-off operators see the v2 surface anyway (keeps default behavior sane).

Empirical evidence (from the temp-branch run) confirms: 8 of 14 failures are `status=500`, 6 are `status=404`, no panics observed in `go test`. Production hot-path under flag-off would 500.

## Files to delete

All 12 are safe to delete from a *production-code* perspective — `go build ./cmd/r1-server/` passes after deletion. The block on deletion is purely the test suite + the v2-OFF runtime regression above.

```
cmd/r1-server/ui/app.js
cmd/r1-server/ui/graph-worker.js
cmd/r1-server/ui/graph.css
cmd/r1-server/ui/graph.html
cmd/r1-server/ui/graph.js
cmd/r1-server/ui/index.html
cmd/r1-server/ui/style.css
cmd/r1-server/ui/vendor/3d-force-graph.min.js
cmd/r1-server/ui/vendor/README.md
cmd/r1-server/ui/vendor/htmx.min.js
cmd/r1-server/ui/vendor/three-spritetext.min.js
cmd/r1-server/ui/vendor/three.min.js
```

Recommended deletion order for a follow-up build branch (NOT done in this audit — audit-only):

1. Replace v2-OFF fallback bodies (or default the flag to on) — closes the v2-OFF HIGH risk above.
2. Apply (a) deletions: drop `TestUIServesIndex`, `TestUIServesStaticAssets`, `TestUISPAFallbackForSessionPath`, `TestIndexFallsBackToSPAWhenV2Disabled`, `TestTraceWaterfallFlagOffServesSPA` and the SPA half of `TestUIGraphRouteDoesNotShadowSPA`.
3. Apply (b) repoints: rewrite the 7 path-rewrite tests against the `ui/web/` v2 surface.
4. Reviewer triage on (c): `TestUIGraphJSHasNodeStyleContract` (NODE_STYLES contract — must migrate to v2 graph.js) and the `TestUIGraphCSSServed` selector list (must verify v2 CSS uses the same slots).
5. `git rm` the 12 files in the same commit as the test edits so CI stays green.

## Out of scope but adjacent

- `cmd/r1-server/ui/web/vendor/README.md` and `cmd/r1-server/ui/web/vendor/htmx.min.js` are SEPARATE v2-tree files (they live under `ui/web/vendor/`, not `ui/vendor/`). The grep hits in `htmx_shell_test.go`, `ui_v2_foundation_test.go`, `diff_v2_test.go`, `index_test.go:91` referencing `/ui/web/vendor/htmx.min.js` and `/ui/vendor/htmx.min.js` (line 91) are NOT impacted by deleting the 12 legacy files — except `index_test.go:91` is, because `TestIndexServesHTMXShellWhenV2Enabled` asserts the v2 template renders `/ui/vendor/htmx.min.js` (no `web` prefix). This is a v2-tier reference that should already match an existing path; see `cmd/r1-server/ui/vendor/htmx.min.js` (legacy) vs `cmd/r1-server/ui/web/vendor/htmx.min.js` (v2). The v2 template `templates/index.tmpl` in main currently emits the legacy path; the v2 spec mandates `/ui/web/vendor/`. Reviewer should confirm which path the v2 dashboard wants and align template + test together.
- `vendor_check_test.go::TestCheckVendoredLibs_Missing` uses `graph.html` only as in-memory fixture data; not impacted.
