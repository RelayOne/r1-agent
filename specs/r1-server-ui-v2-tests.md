<!-- STATUS: done -->
<!-- CREATED: 2026-05-05 -->
<!-- BUILD_STARTED: 2026-05-05 -->
<!-- BUILD_COMPLETED: 2026-05-05 -->
<!-- DEPENDS_ON: r1-server-ui-v2-foundation, r1-server-ui-v2-3d-perf, r1-server-ui-v2-event-rendering, r1-server-ui-v2-handlers-and-routes -->
<!-- BUILD_ORDER: 32 -->

# r1-server UI v2 — Cross-cutting Tests + Golden + Vendor Verify

## 1. Overview

Specs 1-4 each ship their own per-feature unit tests. This spec catches the cross-cutting test concerns that don't fit cleanly into one feature spec:

- **htmx-shell golden test** — pin the `base.html` rendered output so future spec changes to layout get caught.
- **Golden template suite** — golden files for every page template (index, session, session-graph, session-stream, memories, share, diff). Auto-update via `-update` flag.
- **3D worker fixture test** — vitest-driven `graph-worker.test.js` against a 50-node fixture, asserting position convergence.
- **E2E smoke** — Playwright test (`//go:build e2e`) that drives a real headless Chromium through instance-list → session → 3D tab → memory tab → add memory → delete memory → share link.
- **Vendor freshness** — a CI-only check that the vendored files' SRI hashes still match the table; fails if anyone hand-edits.
- **Accessibility audit** — axe-core run inside the Playwright E2E, asserting no WCAG AA failures on each rendered page.

These tests live in the existing `cmd/r1-server/` Go test suite + the existing `web/` vitest suite + a new `cmd/r1-server/e2e/` directory for the Playwright E2E.

## 2. Stack & Versions

- Go testing stdlib + `httptest` (existing)
- vitest 2.1 (current pin; bumps to 4 after Node 22 CI bump per RT-JSDOM-VITEST-NODE22)
- Playwright 1.49 (already vendored in `web/`)
- @axe-core/playwright (already vendored in `web/`)
- Headless Chromium (Playwright provides)

## 3. Test architecture

```
cmd/r1-server/
├── golden_test.go              # NEW: golden template suite + -update
├── testdata/
│   └── golden/
│       ├── base.html
│       ├── index.html
│       ├── session.html
│       ├── session-graph.html
│       ├── session-stream.html
│       ├── memories.html
│       ├── share.html
│       └── diff.html
├── e2e/                        # NEW
│   ├── e2e_test.go             # //go:build e2e — drives Playwright
│   ├── go.mod                  # NEW: e2e/ as a sub-module so default
│   │                           # `go test ./...` doesn't try to compile it
│   └── go.sum
└── ui_attr_lint_test.go        # from Spec 1, extended here
```

## 4. Golden template suite

Pattern: each test fixtures a context, renders the template, compares against `testdata/golden/<name>.html`. `go test -update` rewrites the golden when the change is intentional.

```go
// cmd/r1-server/golden_test.go
package main

import (
    "bytes"
    "flag"
    "os"
    "path/filepath"
    "testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func goldenAssert(t *testing.T, name string, got []byte) {
    t.Helper()
    path := filepath.Join("testdata", "golden", name)
    if *update {
        if err := os.WriteFile(path, got, 0o644); err != nil { t.Fatal(err) }
        return
    }
    want, err := os.ReadFile(path)
    if err != nil { t.Fatal(err) }
    if !bytes.Equal(got, want) {
        t.Fatalf("%s: golden mismatch\n--- want\n%s\n--- got\n%s", name, want, got)
    }
}

func TestGolden_Base(t *testing.T) {
    cfg := V2Config{Enabled: true, HtmxSRI: "sha384-FAKEHTMX", HtmxSseSRI: "sha384-FAKESSE"}
    var buf bytes.Buffer
    if err := tpls.ExecuteTemplate(&buf, "base", baseFixture(cfg)); err != nil { t.Fatal(err) }
    goldenAssert(t, "base.html", buf.Bytes())
}
```

Each page template gets a `TestGolden_<Name>` function. Fixtures hardcoded with deterministic timestamps (`time.Date(2026,1,1,...)`), session ids, hashes — the goldens have to be byte-stable across runs.

## 5. 3D worker fixture test

In `web/` vitest suite:

```ts
// web/src/test/graph-worker.test.ts
import { describe, it, expect } from 'vitest'

describe('graph-worker', () => {
  it('converges the layout for a 50-node fixture in <200 ticks', async () => {
    const worker = new Worker('/cmd/r1-server/ui/web/js/graph-worker.js', { type: 'module' })
    const fixture = JSON.parse(await fs.readFile('testdata/graph-50.json', 'utf-8'))
    const result = await new Promise((resolve) => {
      worker.onmessage = (e) => {
        if (e.data.kind === 'positions' && e.data.alpha < 0.02) resolve(e.data)
      }
      worker.postMessage({ kind: 'init', ...fixture })
      let i = 0
      const tick = () => {
        worker.postMessage({ kind: 'tick' })
        if (i++ < 200) setTimeout(tick, 0)
      }
      tick()
    })
    expect(result.positions.length).toBe(150) // 50 nodes × 3 axes
    expect(result.alpha).toBeLessThan(0.02)
    worker.terminate()
  })
})
```

Note: depends on Spec 2's `graph-worker.js` landing first.

## 6. E2E (Playwright)

`cmd/r1-server/e2e/e2e_test.go`:

```go
//go:build e2e

package e2e

import (
    "context"
    "os/exec"
    "testing"
    "time"

    "github.com/playwright-community/playwright-go"
)

func TestE2E_FullFlow(t *testing.T) {
    // Boot r1-server with a fixture DB
    cmd := exec.CommandContext(context.Background(), "go", "run", "../...",
        "--data-dir", t.TempDir(), "--listen", ":0")
    // ... start, wait for port, drive Playwright through:
    //   /                       → assert instance-list table
    //   /session/<id>           → assert waterfall renders
    //   /session/<id>/graph     → wait for canvas + assert FPS via perf API
    //   /memories               → assert grouped list
    //   POST /api/memories      → assert new card appears
    //   DELETE /api/memories/X  → assert card removed
    //   /share/<hash>           → assert read-only banner
    //   /api/session/<id>/export.tracebundle → assert tar.gz returned
    //
    // axe-core run on each page; fail on any WCAG AA violation.
}
```

The e2e dir is a separate Go submodule so default `go test ./...` doesn't pull in Playwright dependencies. Runs only in a release-rehearsal CI lane.

## 7. Vendor freshness check

```go
// cmd/r1-server/vendor_freshness_test.go
package main

import (
    "crypto/sha512" // for sha384
    "encoding/base64"
    "os"
    "testing"
)

// vendoredSRIExpected is the same map as ui_v2_flag.go's vendoredSRI,
// generated at compile time from web/vendor/sri.json. This test
// recomputes each file's sha384 and asserts it matches — guards
// against silent corruption + accidental hand-edits.
func TestVendor_SRI(t *testing.T) {
    for path, wantSRI := range vendoredSRIExpected {
        b, err := os.ReadFile(filepath.Join("ui/web/vendor", path))
        if err != nil { t.Fatalf("%s: %v", path, err) }
        h := sha512.Sum384(b)
        gotSRI := "sha384-" + base64.StdEncoding.EncodeToString(h[:])
        if gotSRI != wantSRI {
            t.Errorf("%s: SRI mismatch want=%s got=%s", path, wantSRI, gotSRI)
        }
    }
}
```

This complements Spec 1's vendor-script SRI verification — Spec 1's check runs at vendor time; this check runs at test time, catching post-vendor edits.

## 8. Accessibility audit

In the Playwright E2E, after each page load:

```ts
import AxeBuilder from '@axe-core/playwright'

const results = await new AxeBuilder({ page }).analyze()
expect(results.violations).toEqual([])
```

Specifically catches:
- Missing aria-label on icon buttons
- Insufficient contrast on the desaturated redacted style (RT-REDACTION-UI-PATTERNS gotcha)
- Missing alt text on any introduced `<img>` (none expected, but guard against future regression)

## 9. Boundaries

- This spec does NOT add new feature behavior. Tests only.
- This spec does NOT cover load-test / perf at scale beyond a single 3D worker convergence test. Sustained-perf load testing is a separate spec.
- This spec does NOT cover the cargo Rust E2E in `desktop/` — that's the existing `desktop-augmentation.yml` workflow.
- Playwright E2E runs only in a release-rehearsal lane, not on every PR. Default `go test ./...` does not require Playwright.

## 10. Implementation checklist (6 items — self-contained)

- [ ] Write `cmd/r1-server/golden_test.go` with the `goldenAssert` helper + 8 `TestGolden_*` functions (one per page template: base, index, session, session-graph, session-stream, memories, share, diff). Each fixtures a deterministic context (fixed timestamps, session ids, chain hashes) so the golden output is byte-stable. Add `cmd/r1-server/testdata/golden/.gitkeep` + run `go test -update ./cmd/r1-server/...` once to seed the goldens. `make golden-update` documented in CONTRIBUTING.md.
- [ ] Write `web/src/test/graph-worker.test.ts` per §5. Fixture file `web/src/test/testdata/graph-50.json` checked in. The test needs Web Worker support in vitest (jsdom env doesn't provide it; use vitest's `@vitest/ui` browser worker support OR happy-dom env). On Node 20, this test may need to be skipped via `it.skip(skipReason, ...)` until the Node 22 bump (per RT-JSDOM-VITEST-NODE22) — document the skip with a TODO + issue #145-style follow-up reference.
- [ ] Write `cmd/r1-server/e2e/go.mod` declaring `module github.com/RelayOne/r1/cmd/r1-server/e2e` (separate from main module) + `go.sum`. Write `cmd/r1-server/e2e/e2e_test.go` with `//go:build e2e` + the full TestE2E_FullFlow flow per §6. Boot r1-server as a child process with a temp data-dir; drive Playwright through instance-list → session → 3D → memory CRUD → share → tracebundle. Each page load runs an `@axe-core/playwright` audit; any WCAG AA violation fails the test. Add a Cloud Build step to a new `services/cloudbuild-e2e.yaml` that runs the e2e tag in a release-rehearsal trigger (NOT every PR).
- [ ] Write `cmd/r1-server/vendor_freshness_test.go` per §7. `vendoredSRIExpected` is the same map declared in `ui_v2_flag.go` (or accessed via a public-test getter). On test failure, the message tells the developer which file is corrupt and to re-run `bash scripts/vendor-ui.sh`.
- [ ] Add an htmx-shell smoke test `cmd/r1-server/htmx_shell_test.go`: boots an in-memory mux, GETs `/`, asserts the response body contains `hx-ext="sse"` on `<body>`, the `<script type="importmap">` block, the htmx + htmx-ext-sse `<script>` tags with SRI integrity attributes, and a `data-testid="instance-list"` element. Catches base.html regressions that golden tests would only catch via byte-diff.
- [ ] Document the test layout in `cmd/r1-server/testdata/README.md`: golden vs e2e vs vitest; how to run each; how to update goldens; Playwright Chromium setup (`npx playwright install --with-deps`). Cross-link from the root-level `CONTRIBUTING.md` and `docs/DEPLOYMENT.md`.

## 11. Acceptance

- `go test ./cmd/r1-server/...` clean (golden tests + vendor_freshness + htmx_shell pass).
- `npm test --workspace=web` clean (graph-worker.test.ts passes if Worker support is available; skipped with documented TODO if Node-version-blocked).
- `go test -tags=e2e ./cmd/r1-server/e2e/...` runs end-to-end against a freshly booted server, drives the full flow, completes with zero axe violations and zero console errors. Manual on a developer machine; CI in the release-rehearsal lane only.
