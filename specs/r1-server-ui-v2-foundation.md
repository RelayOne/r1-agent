<!-- STATUS: ready -->
<!-- CREATED: 2026-05-05 -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 28 -->

# r1-server UI v2 — Foundation (vendor + htmx layout)

## 1. Overview

The `r1-server-ui-v2` retrofit (build_order 27) shipped most Go-side handlers, the SSE endpoint, the v2 feature-flag gate, the htmx index, the trace waterfall, the share view, the memory CRUD, the run-diff handler, and `R1_SERVER_UI_V2`-gated routes. What it did **not** ship is the foundation that the rest of the UI v2 work plans to layer on:

- The `cmd/r1-server/ui/web/` directory tree per the original §2.2 file layout.
- A pinned, vendored set of frontend dependencies (htmx 2 core + sse extension, three.js InstancedMesh primitives, addons currently CDN-fetched in `graph.html`).
- A `base.html` htmx layout that all v2 templates extend, with the SSE hookup + import map declared once.
- The data-* attribute conventions for marking islands (`data-island="graph"`), per-row metadata, and SSE swap targets — currently inconsistent across the existing v2 templates.

This spec ships **only the foundation**. Specs 2 (3D perf), 3 (event rendering), 4 (handlers + routes), and 5 (tests) all depend on this landing first.

## 2. Stack & Versions

<!-- RESOLVED: htmx 2.0.4 stable since 2024-06-17 + htmx-ext-sse 2.2.4 paired per htmx#3337 (see specs/research/raw/RT-HTMX-SSE-DATA-ATTRS.md). -->
<!-- RESOLVED: curl + SRI shell script. SRI = "sha384-" + base64(openssl dgst -sha384 -binary FILE). NOT sha384sum (wrong format). See specs/research/raw/RT-VENDOR-SCRIPT-PATTERNS.md. -->

| Concern | Library | Pinned version | Why |
|---|---|---|---|
| Chrome | htmx | 2.x — confirm exact | Spec §2.1 mandates 2.x for partial swaps + import map support |
| SSE extension | htmx-ext-sse | latest compatible with htmx 2 | Reconnect with Last-Event-ID resume |
| 3D core | three.js | matches existing `three.min.js` | Re-vendored as ES module form (`three.module.js`) for import map use |
| Force layout | d3-force-3d | latest stable | Used by Spec 2's Web Worker |
| Force graph | 3d-force-graph | matches existing `3d-force-graph.min.js` | Existing graph.js consumer |
| Sprite labels | three-spritetext | matches existing | Lock + skill labels in 3D |
| Templates | Go `html/template` | stdlib | All v2 pages |
| Build pipeline | none | — | Vendor-at-tooling-time, embed at compile-time |

## 3. File layout

```
cmd/r1-server/ui/web/
├── base.html                 # htmx + SSE + import map shell — extended by all pages
├── partials/
│   ├── (created by Specs 3 + 4 — none in this spec, only the directory)
├── vendor/                   # written by scripts/vendor-ui.sh
│   ├── htmx.min.js
│   ├── htmx-ext-sse.js
│   ├── three.module.js
│   ├── three/
│   │   └── addons/
│   │       └── controls/
│   │           └── OrbitControls.js
│   ├── 3d-force-graph.js
│   ├── three-spritetext.js
│   └── d3-force-3d.js
├── README.md                 # vendor process + SRI hash table
└── (css/, js/ — created by Specs 2 + 4 as needed)

scripts/
└── vendor-ui.sh              # idempotent: re-run produces no diff if versions unchanged
```

## 4. base.html contract

Every v2 page extends `base.html` via `{{ template "base" . }}` so the import map, SSE extension load, and CSP headers stay in one place.

```html
<!doctype html>
<html lang="en" data-theme="auto">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{ block "title" . }}r1-server{{ end }}</title>
  <link rel="stylesheet" href="/ui/web/css/base.css">

  <!-- Spec §2.1: import map declared once in base; pages reference module
       paths without bundler involvement. -->
  <script type="importmap">{{ template "import-map" }}</script>

  <!-- Vendored htmx + sse extension. Order matters: core before extension. -->
  <script src="/ui/web/vendor/htmx.min.js"
          integrity="{{ .HtmxSRI }}" crossorigin="anonymous"></script>
  <script src="/ui/web/vendor/htmx-ext-sse.js"
          integrity="{{ .HtmxSseSRI }}" crossorigin="anonymous"></script>
</head>
<body hx-ext="sse" data-session-id="{{ .SessionID }}">
  {{ block "topbar" . }}{{ end }}
  <main id="main">
    {{ block "main" . }}{{ end }}
  </main>
  {{ block "scripts" . }}{{ end }}
</body>
</html>
```

`import-map` template renders a JSON object mapping bare-import names to vendored paths:

```json
{
  "imports": {
    "three": "/ui/web/vendor/three.module.js",
    "three/addons/controls/OrbitControls.js": "/ui/web/vendor/three/addons/controls/OrbitControls.js",
    "d3-force-3d": "/ui/web/vendor/d3-force-3d.js",
    "3d-force-graph": "/ui/web/vendor/3d-force-graph.js",
    "three-spritetext": "/ui/web/vendor/three-spritetext.js"
  }
}
```

## 5. Data-attribute conventions (the migration)

<!-- RESOLVED: htmx accepts both `hx-get` and `data-hx-get` (HTML5-valid). Spec pins `data-hx-*` for user-authored attributes; framework-emitted attributes can stay `hx-*`. CI grep guard. -->

The existing v2 templates mix `hx-*` attributes with ad-hoc `data-*` markers. v2 §2.4 implies a forward migration to `data-hx-*` (htmx supports both forms; `data-` is HTML5-valid). This spec pins the convention:

| Purpose | Attribute | Example |
|---|---|---|
| htmx behaviour | `hx-*` | `hx-get="/api/foo"` (kept; htmx 2 still treats unprefixed as canonical) |
| SSE swap target | `sse-swap` | `sse-swap="waterfall-node"` |
| Island marker | `data-island` | `data-island="graph"` (read by `app.js` to bootstrap vanilla-JS islands) |
| Per-row metadata | `data-node-id` / `data-node-type` / `data-cursor` | Cited by scrubber JS without re-parsing innerHTML |
| Test selector | `data-testid` | Mirrors web-chat-ui spec convention |

**Anti-pattern**: do not encode behaviour in class names (`.collapsed`, `.active`) — use `data-state="collapsed"` etc. so CSS selectors and JS selectors share the same source of truth.

## 6. Vendor script

<!-- RESOLVED: short-circuit on hash match + atomic mv from temp file. Re-run with unchanged manifest produces zero diff. CI runs in --check mode (no network). -->

`scripts/vendor-ui.sh` pulls pinned versions into `cmd/r1-server/ui/web/vendor/` from each library's GitHub release tarball. Each fetch is followed by an SRI hash check. Re-running the script with no version changes produces no diff.

Script outline (final form to be confirmed by RT-VENDOR-SCRIPT-PATTERNS):

```bash
#!/usr/bin/env bash
# scripts/vendor-ui.sh — vendor r1-server UI assets at build-tooling time.
set -euo pipefail
VENDOR=cmd/r1-server/ui/web/vendor
mkdir -p "$VENDOR/three/addons/controls"

declare -A SRI=(
  ["htmx.min.js"]="sha384-..."
  ["htmx-ext-sse.js"]="sha384-..."
  ["three.module.js"]="sha384-..."
  ["3d-force-graph.js"]="sha384-..."
  ["three-spritetext.js"]="sha384-..."
  ["d3-force-3d.js"]="sha384-..."
)

fetch() { curl -fsSL "$2" -o "$VENDOR/$1"; verify_sri "$1"; }
verify_sri() {
  local f=$VENDOR/$1
  local want=${SRI[$1]}
  local got
  got=$(printf 'sha384-%s' "$(openssl dgst -sha384 -binary "$f" | openssl base64 -A)")
  [[ $got == "$want" ]] || { echo "SRI mismatch for $1: $got"; exit 1; }
}

fetch htmx.min.js     "https://github.com/bigskysoftware/htmx/releases/download/v2.0.x/htmx.min.js"
# ... etc
```

The SRI table is checked into the repo. CI never runs this script — it runs at developer/release time and the artifacts are committed.

Total budget: ≤250 KB gzipped. The README.md inside `vendor/` documents each file's source URL, version, license, and SRI.

## 7. Boundaries — what NOT to do

- **No build pipeline.** No webpack, no esbuild, no rollup. The vendored files ship as-is.
- **No CDN at runtime.** Every script must load from `/ui/web/vendor/`.
- **No npm/pnpm install in CI.** The vendor step is a developer/release-time action only.
- **No tailwind, no CSS-in-JS, no preprocessor.** Plain CSS in `cmd/r1-server/ui/web/css/`.
- **No client-side router.** htmx's `hx-push-url` handles deep links.
- **Do not migrate the existing `cmd/r1-server/ui/index.html` SPA.** The vanilla-JS SPA stays untouched until v3; this spec adds a parallel `web/` tree gated by `R1_SERVER_UI_V2=1`.

## 8. Migration path (one release cycle of parallel code)

1. This spec ships `web/` alongside the existing `ui/` tree.
2. `mountUI` already branches on `v2Enabled()` — when on, it serves `web/` templates; when off, it serves the SPA. Tests cover both branches.
3. Two release cycles: v2 stays opt-in.
4. After two releases, `cmd/r1-server/ui/` (the legacy SPA) is deleted in a follow-up; `web/` is renamed back to `ui/`. Tracked separately.

## 9. Testing

- `cmd/r1-server/ui_v2_foundation_test.go`: unit tests for the `import-map` template render, the base.html template parse, and the SRI verification helper used by `vendor-ui.sh` (factored to Go for cross-platform reproducibility — see §6).
- `scripts/vendor-ui-test.sh`: integration test that runs `vendor-ui.sh` against a temp dir, asserts each fetched file's SRI matches the table, and re-runs to assert no diff.
- Golden test for `base.html` rendered with a fixture context — pinned in `cmd/r1-server/testdata/golden/base.html`.

## 10. Implementation checklist (10 items — self-contained)

### Foundation directory + vendor script

- [ ] Create the `cmd/r1-server/ui/web/` directory with subdirs `partials/`, `vendor/three/addons/controls/`, `css/`, `js/`. Add a placeholder `web/.gitkeep` and a `web/README.md` that describes the layout per §3.
- [ ] Write `scripts/vendor-ui.sh` per §6: idempotent, SRI-verifying, pulls htmx + htmx-ext-sse + three.module.js + OrbitControls.js + 3d-force-graph.js + three-spritetext.js + d3-force-3d.js into `web/vendor/`. Run once and check the resulting files into git.
- [ ] Fill the SRI table in `vendor-ui.sh` with the actual sha384 hashes of the vendored files. Document each source URL + version + license in `web/vendor/README.md`.
- [ ] Add `cmd/r1-server/sri_test.go` with a unit test that recomputes each file's sha384 and asserts it matches the table — guards against silent corruption + fails CI if the script is re-run with mismatched content.

### base.html + import map

- [ ] Write `cmd/r1-server/ui/web/base.html` per §4. It must declare `{{ block "title" }}`, `{{ block "topbar" }}`, `{{ block "main" }}`, `{{ block "scripts" }}` and load the import map + htmx + htmx-ext-sse with SRI integrity attributes. Add a CSP header in the Go handler that allows `script-src 'self'` (no inline scripts).
- [ ] Write `cmd/r1-server/ui/web/partials/import-map.html` rendered as a `{{ template "import-map" }}` block — the JSON import map per §4. Mapped names: `three`, `three/addons/controls/OrbitControls.js`, `d3-force-3d`, `3d-force-graph`, `three-spritetext`.
- [ ] Add `cmd/r1-server/ui_v2_foundation.go`: a new `parseV2Templates()` helper that uses `template.ParseFS(webFS, "*.html", "partials/*.html")` once at init and panics on parse error. Call it from `mountUI` when `v2Enabled()`. The existing `serveHTMLIndex` migrates to extending `base.html` in a follow-up; this spec only sets up the parser + does not break existing index rendering.

### data-* convention guard

- [ ] Add `cmd/r1-server/ui_attr_lint_test.go`: a unit test that scans every `web/*.html` and `web/partials/*.html` template body and fails if any element uses both `class="collapsed"` (or similar behaviour-encoding class names from a denylist) AND a corresponding `data-state` attribute. Lock in the convention from §5 so future templates can't drift.

### CSS scaffolding

- [ ] Write `cmd/r1-server/ui/web/css/base.css`: a 100-200 line stylesheet defining the topbar, main grid, side-panel layout, and CSS custom properties for `--color-fg / --color-bg / --color-muted / --color-accent / --color-redacted-overlay`. Use prefers-color-scheme to swap between light/dark; high-contrast support via a `data-theme="hc"` override. No tailwind, no preprocessor.

### Documentation

- [ ] Write `cmd/r1-server/ui/web/README.md` describing the vendor process, the import-map contract, the SRI verification flow, and the data-* attribute convention. This is the contributor-onboarding doc that future spec authors will read first.

## 11. Acceptance

- `go build ./cmd/r1-server/...` clean.
- `go test ./cmd/r1-server/...` clean (sri_test, ui_attr_lint_test, golden base.html test all pass).
- `bash scripts/vendor-ui.sh` is a no-op on a clean checkout (re-runnable).
- `R1_SERVER_UI_V2=1 go run ./cmd/r1-server` serves a 200 on `/` with the import-map present in the response body and the htmx + sse scripts loaded with SRI integrity attributes.
- Total `web/vendor/` size ≤ 250 KB gzipped (CI gate via `cmd/r1-server/vendor_size_test.go`).
