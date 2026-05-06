# r1-server UI v2 — `cmd/r1-server/ui/`

This directory holds the htmx-driven dashboard that ships behind the
`R1_SERVER_UI_V2=1` flag. It exists in parallel with the legacy vanilla-
JS SPA at `cmd/r1-server/ui/`; the two trees do not share code at
runtime. When the flag is on, the v2 handlers in `mountUI` serve from
this tree; when the flag is off, the v1 SPA serves and the v2 handlers
are unwired (or 404, as appropriate).

The split-tree migration is intentional. We don't want a single in-place
rewrite that risks breaking the v1 surface during development. After two
release cycles with v2 on by default and the legacy SPA off, the v1
tree is deleted and `web/` is renamed back to `ui/`. That cleanup is a
follow-up ticket; for now, both trees coexist.

## Directory layout

```
cmd/r1-server/ui/
├── README.md                    you are here
├── base.html                    htmx + SSE + import-map shell — every page extends it
├── partials/                    template fragments included by base.html or page templates
│   └── import-map.html          {{ define "import-map" }} — bare-specifier → vendored path
├── vendor/                      pinned, content-addressed npm blobs (see vendor/README.md)
│   ├── htmx.min.js              htmx 2.0.4
│   ├── htmx-ext-sse.js          htmx-ext-sse 2.2.4
│   ├── three.module.js          three 0.170.0 ESM (minified upstream)
│   ├── three/addons/controls/
│   │   └── OrbitControls.js     three 0.170.0 example addon
│   ├── three-spritetext.js      three-spritetext 1.9.5
│   ├── 3d-force-graph.js        3d-force-graph 1.77.0
│   ├── d3-force-3d.js           d3-force-3d 3.0.5 (UMD; ESM wrapped by Spec 2 worker)
│   └── README.md                pin/version/license/SRI table + bump procedure
├── css/                         plain CSS — no tailwind, no preprocessor
│   └── base.css                 chrome + design tokens + theme model
└── js/                          plain ES modules — no bundler
    └── (filled by Specs 2-4)    graph.js, scrubber.js, redaction.js, ...
```

## Vendor process

Frontend dependencies are vendored: pinned, content-addressed copies
fetched at developer/release time and committed. Production runtime
never touches the npm registry, jsdelivr, or any other CDN. Deploys
are reproducible bit-for-bit from a clean checkout.

The fetch script lives at `scripts/vendor-ui.sh`. It is **idempotent** —
re-running with no version pin changes produces zero diff because each
fetch goes into a temp file and atomic-`mv`s into place only on SRI
match. CI runs the script with `--check` to verify the on-disk blobs
match the SRI table without any network access.

The full version table, source URL list, license summary, SRI hash
format, and bump procedure live in [`vendor/README.md`](vendor/README.md).
**Read that before touching the vendor tree.**

## Import-map contract

`base.html` declares a single `<script type="importmap">` block sourced
from `partials/import-map.html`. The map covers five bare specifiers
the application code uses:

| Specifier | Resolves to |
|---|---|
| `three` | `/ui/vendor/three.module.js` |
| `three/addons/controls/OrbitControls.js` | `/ui/vendor/three/addons/controls/OrbitControls.js` |
| `d3-force-3d` | `/ui/vendor/d3-force-3d.js` |
| `3d-force-graph` | `/ui/vendor/3d-force-graph.js` |
| `three-spritetext` | `/ui/vendor/three-spritetext.js` |

The `three` mapping is load-bearing: three.js's own addons (e.g.
`OrbitControls.js`) `import * as THREE from 'three'`, and the import map
must resolve that bare specifier to the **same** vendored module that
application code loads. If a separate `three` shows up — for example,
because someone vendored a second copy of three under a different path
and pointed only some imports at it — the addon and the app run in two
THREE namespaces and `InstancedMesh` rendering silently breaks.

When you add a new bare-import dependency, update the import map AND
add the matching SRI row to `scripts/vendor-ui.sh` AND bump the
`wantCount` constant in `cmd/r1-server/sri_test.go` so the integrity
guard tracks it.

## Data-attribute conventions

Per spec §5:

| Purpose | Attribute | Example |
|---|---|---|
| htmx behaviour | `hx-*` | `hx-get="/api/foo"` (kept; htmx 2 still treats unprefixed as canonical) |
| SSE swap target | `sse-swap` | `sse-swap="waterfall-node"` |
| Island marker | `data-island` | `data-island="graph"` (Spec 4 reads it to bootstrap vanilla-JS islands) |
| Per-row metadata | `data-node-id` / `data-node-type` / `data-cursor` | scrubber reads them without re-parsing innerHTML |
| Test selector | `data-testid` | matches the web-chat-ui spec's selector convention |
| Behaviour state | `data-state="<token>"` | `data-state="collapsed"`, `data-state="redacted"` |

**Anti-pattern**: do not encode behaviour state in class names
(`.collapsed`, `.active`, `.expanded`, `.selected`, `.hidden`,
`.disabled`). Use `data-state="<token>"` so CSS selectors and JS
selectors share one source of truth. The lint test
`cmd/r1-server/ui_attr_lint_test.go` walks every template under this
tree and fails CI if a class+`data-state` pair drifts.

If you need a class for purely visual styling that is unrelated to
state (e.g. `.row-zebra`, `.muted`, `.mono`), that is fine — the
denylist is specifically about state tokens.

## Adding a new page template

1. Create `cmd/r1-server/ui/<name>.html`. Top of file:
   ```
   {{ define "<name>" }}
   {{ template "base" . }}
   {{ end }}

   {{ define "main" }}
   <!-- your page body here -->
   {{ end }}
   ```
   `parseV2Templates()` (in `ui_v2_foundation.go`) picks up `*.html`
   automatically.

2. If you need shared fragments, drop them under `partials/` with
   `{{ define "<frag-name>" }}` and reference via `{{ template "<frag-name>" . }}`.

3. Wire a handler in `cmd/r1-server/ui_v2_foundation.go` (or a
   feature-specific file). Call `setV2CSP(w.Header())` before
   `WriteHeader`.

4. Add a golden test under `cmd/r1-server/testdata/golden/` if the
   page is part of the v2 acceptance surface — Spec 5 will gate the
   golden assertions.

## Boundaries

- **No build pipeline.** No webpack/esbuild/rollup. Vendored files ship as-is.
- **No CDN at runtime.** Every script must load from `/ui/vendor/`.
- **No npm/pnpm install in CI.** The vendor step is dev/release-time only.
- **No tailwind, no CSS-in-JS, no preprocessor.** Plain CSS in `css/`.
- **No client-side router.** htmx's `hx-push-url` handles deep links.
- **No inline scripts.** The CSP header set by `setV2CSP` blocks them.
- **Do not migrate the legacy `cmd/r1-server/ui/index.html` SPA.** It
  stays untouched until the v3 cleanup follow-up.
