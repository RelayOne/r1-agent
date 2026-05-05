# build-plan.md — r1-server-ui-v2-foundation (Spec 1 of 5)

**Spec:** specs/r1-server-ui-v2-foundation.md (BUILD_ORDER 28)
**Mode:** FEATURE (no `<!-- TYPE: repair -->` header)
**Branch:** build/r1-server-ui-v2-foundation
**Started:** 2026-05-05

Each item is one task = one subagent = one `feat(TASK-N): ...` commit. After all 10
ship, this plan is archived to `plans/archive/`.

## Tasks

### T1 — Create `cmd/r1-server/ui/web/` directory tree

**MUST:**
- Create `cmd/r1-server/ui/web/` with subdirs `partials/`, `vendor/three/addons/controls/`, `css/`, `js/`.
- Add `cmd/r1-server/ui/web/.gitkeep` so empty subdirs are committed.
- This task only creates the layout — the subdir-specific files (vendor blobs, base.html, css, js) are added in later tasks. The README.md lives in T10.
- `go build ./cmd/r1-server/...` must remain clean.
- Commit message: `feat(TASK-1): scaffold cmd/r1-server/ui/web/ directory tree`

- [ ] T1 done

### T2 — Write `scripts/vendor-ui.sh`

**MUST:**
- New file `scripts/vendor-ui.sh`, executable (`chmod +x`).
- Idempotent: re-running with unchanged version pins produces zero diff (use `curl ... -o tmp && mv` only when SRI changes).
- Fetches: htmx 2.0.4, htmx-ext-sse 2.2.4, three.module.js 0.170.0, three/addons/controls/OrbitControls.js 0.170.0, 3d-force-graph 1.77.0, three-spritetext 1.9.5, d3-force-3d 3.0.5.
- Uses `set -euo pipefail`.
- SRI helper: `printf 'sha384-%s' "$(openssl dgst -sha384 -binary "$f" | openssl base64 -A)"` — NOT `sha384sum` (wrong format).
- Sources: jsdelivr primary (where SRI matches release notes), GitHub release tarball preferred for htmx (upstream-published SRI).
- Adds a `--check` mode that verifies SRI without fetching (used by CI).
- Run the script once, fetch all files into `cmd/r1-server/ui/web/vendor/`, commit the resulting blobs in this task.
- If network is unavailable in this environment, mark BLOCKED — do not fake the blobs.
- Commit message: `feat(TASK-2): vendor-ui.sh + initial vendored htmx/three/d3 blobs`

- [ ] T2 done

### T3 — Fill SRI table in `vendor-ui.sh` + write `web/vendor/README.md`

**MUST:**
- Replace placeholder `sha384-...` strings in vendor-ui.sh with actual sha384 hashes computed from the blobs landed in T2.
- Format: `sha384-<base64-of-binary-digest>` per the openssl pipeline.
- Write `cmd/r1-server/ui/web/vendor/README.md` listing each file: source URL, version, license, SRI hash. Include the regenerate command (`bash scripts/vendor-ui.sh`).
- Commit message: `feat(TASK-3): real SRI hashes + vendor README`

- [ ] T3 done

### T4 — Add `cmd/r1-server/sri_test.go`

**MUST:**
- New Go test file at `cmd/r1-server/sri_test.go`.
- For each vendored file, recompute sha384 and assert it matches the SRI declared in `vendor-ui.sh` (parse the SRI table from the script).
- Test must fail if any blob is corrupted or if the script's SRI table drifts from blob content.
- Standard `testing.T` patterns; no external deps.
- `go test ./cmd/r1-server/...` clean.
- Commit message: `feat(TASK-4): sri_test.go guards vendored blob integrity`

- [ ] T4 done

### T5 — Write `cmd/r1-server/ui/web/base.html`

**MUST:**
- New file `cmd/r1-server/ui/web/base.html` per spec §4.
- Defines `{{ block "title" . }}`, `{{ block "topbar" . }}`, `{{ block "main" . }}`, `{{ block "scripts" . }}` blocks.
- Loads `/ui/web/vendor/htmx.min.js` and `/ui/web/vendor/htmx-ext-sse.js` with `integrity="{{ .HtmxSRI }}"` and `crossorigin="anonymous"`. Order: core before extension.
- Includes `<script type="importmap">{{ template "import-map" . }}</script>`.
- `<body hx-ext="sse" data-session-id="{{ .SessionID }}">`.
- CSS link: `<link rel="stylesheet" href="/ui/web/css/base.css">`.
- A CSP header is added in T7 (Go handler), not here — but base.html must NOT use inline scripts.
- Commit message: `feat(TASK-5): base.html htmx + SSE + import-map shell`

- [ ] T5 done

### T6 — Write `cmd/r1-server/ui/web/partials/import-map.html`

**MUST:**
- New file `cmd/r1-server/ui/web/partials/import-map.html` defining `{{ define "import-map" }}` template.
- Body: JSON object per spec §4 with imports for `three`, `three/addons/controls/OrbitControls.js`, `d3-force-3d`, `3d-force-graph`, `three-spritetext`.
- Paths must resolve to `/ui/web/vendor/...` files vendored in T2.
- Commit message: `feat(TASK-6): import-map partial template`

- [ ] T6 done

### T7 — Add `cmd/r1-server/ui_v2_foundation.go`

**MUST:**
- New file `cmd/r1-server/ui_v2_foundation.go`.
- `parseV2Templates()` helper using `template.ParseFS(webFS, "*.html", "partials/*.html")`. Cache the parsed `*template.Template` in a package-level `sync.Once`-initialised var or call from init() and panic on parse error.
- Add a build-tagged `//go:embed ui/web/*.html ui/web/partials/*.html ui/web/css/*.css ui/web/vendor/*.js ui/web/vendor/three/addons/controls/*.js` directive declaring `webFS embed.FS`.
- Wire `parseV2Templates()` into `mountUI` only when `v2Enabled()`. Existing `serveHTMLIndex` is NOT migrated yet — this task only sets up the parser.
- CSP header added by handler: `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:`.
- `go build ./cmd/r1-server/...` clean.
- Commit message: `feat(TASK-7): parseV2Templates + webFS embed + CSP header`

- [ ] T7 done

### T8 — Add `cmd/r1-server/ui_attr_lint_test.go`

**MUST:**
- New Go test file `cmd/r1-server/ui_attr_lint_test.go`.
- Walks `cmd/r1-server/ui/web/*.html` and `cmd/r1-server/ui/web/partials/*.html`.
- For each, parses with `golang.org/x/net/html` (already in go.sum, used elsewhere in the repo) and asserts: no element has BOTH `class` containing a behaviour denylist token (`collapsed`, `active`, `expanded`, `selected`, `hidden`) AND a corresponding `data-state="<token>"` attribute. (Pick one — the `data-state` form per §5; flag the duplicate.)
- Test fails if any drift is detected — locks in the convention from §5.
- `go test ./cmd/r1-server/...` clean.
- Commit message: `feat(TASK-8): ui_attr_lint_test.go pins data-state convention`

- [ ] T8 done

### T9 — Write `cmd/r1-server/ui/web/css/base.css`

**MUST:**
- 100-200 line stylesheet.
- Defines: topbar layout, main grid, side-panel layout.
- CSS custom properties: `--color-fg`, `--color-bg`, `--color-muted`, `--color-accent`, `--color-redacted-overlay`.
- `prefers-color-scheme: dark` swaps via `:root[data-theme="auto"]` + `:root[data-theme="dark"]` overrides.
- High-contrast: `:root[data-theme="hc"]` block with WCAG AA-compliant 4.5:1 contrast.
- No tailwind, no preprocessor — plain CSS.
- Commit message: `feat(TASK-9): base.css scaffold + theme custom-properties`

- [ ] T9 done

### T10 — Write `cmd/r1-server/ui/web/README.md`

**MUST:**
- Contributor onboarding doc.
- Sections: directory layout (per §3), vendor process (per §6, link to scripts/vendor-ui.sh), import-map contract (per §4), data-* attribute convention (per §5).
- Verbose, paragraph-style — not a brief checklist.
- Commit message: `feat(TASK-10): web/README.md contributor onboarding`

- [ ] T10 done

## Supervisor verification (after all 10)

- `go build ./cmd/r1-server/...` clean
- `go test ./cmd/r1-server/...` clean (sri_test, ui_attr_lint_test pass)
- `bash scripts/vendor-ui.sh --check` clean (re-runnable, no diff)
- `R1_SERVER_UI_V2=1 go run ./cmd/r1-server` returns 200 on `/` with import-map + htmx scripts in body
- `du -sh cmd/r1-server/ui/web/vendor/` ≤ 250 KB gzipped
- All 10 task commits present in `git log build/r1-server-ui-v2-foundation`
