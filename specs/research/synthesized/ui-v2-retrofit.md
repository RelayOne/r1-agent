# Synthesized — UI v2 retrofit (2026-05-05)

Cluster file consolidating findings from RT-INSTANCEDMESH-PERF, RT-D3-FORCE-WEBWORKER, RT-HTMX-SSE-DATA-ATTRS, RT-VENDOR-SCRIPT-PATTERNS, RT-REDACTION-UI-PATTERNS, RT-WATERFALL-DENSITY, RT-JSDOM-VITEST-NODE22.

## CI runtime

**Node 20 went EOL on 2026-04-30** (5 days before this scope). All 5 specs assume the existing Node 20.18 baseline but RT-JSDOM-VITEST-NODE22 strongly recommends bumping CI to Node 22 LTS now. Doing so unblocks:

- jsdom 29 (currently pinned at ^26.1.0)
- vitest 4 (currently pinned at ^2.1.0)
- @vitest/coverage-v8 4
- vite 7+ (currently pinned at ^6 because vite 7 needs Node ^20.19.0)

Recommendation: ship a separate small PR bumping `cloudbuild.yaml` (`node:20` → `node:22.13-bookworm-slim`) + `desktop-augmentation.yml` (3 × `node-version: '20'` → `'22'`, ubuntu-22.04 → ubuntu-24.04) BEFORE Spec 5's tests rely on Worker support.

## Vendoring

**Strategy A** (curl + per-file SRI shell script + committed blobs) wins. **Pinned versions:** htmx 2.0.4, htmx-ext-sse 2.2.4 (paired per htmx#3337), three 0.170.0 (replace existing 0.x global-THREE blob with ESM module), three-spritetext 1.9.5, 3d-force-graph 1.77.0, d3-force-3d 3.0.5.

**SRI format:** `openssl dgst -sha384 -binary FILE | openssl base64 -A` then prefix `sha384-`. Do NOT use `sha384sum` — wrong format.

**CDN preference:** jsdelivr primary, GitHub release tarball preferred where the upstream publishes SRI alongside the release. unpkg de-prioritised after March 2025 18-hour outage.

**Existing inconsistency** to clean up: `cmd/r1-server/ui/vendor/README.md` documents an ESM-style approach (`three.module.js` at 0.160.0) but the committed blob is a global-THREE bundle. Decision: Spec 1 normalises to ESM 0.170.0 + import map.

## htmx 2 + SSE

**Stable 2.0.x** (since 2024-06-17). Pin explicitly — htmx 4 ("Fetchening") is in design. **Last-Event-ID gotcha:** htmx-ext-sse uses native EventSource (which auto-forwards `Last-Event-ID` for browser-internal reconnects), but the extension's own backoff path constructs a fresh EventSource that resets `lastEventId=""`. **Fix:** override `htmx.createEventSource` client-side to thread `?last_event_id=` into the URL, AND have the Go handler accept either the header or the query. Spec 4 ships both halves.

**data-* migration:** htmx accepts both `hx-get` and `data-hx-get` (the latter is HTML5-valid). Spec 1 pins `data-hx-*` for everything user-authored; the framework-emitted attributes can stay `hx-*`. CI grep guard prevents drift.

**OOB updates:** `hx-swap-oob="beforeend:#selector"` for multi-target updates in a single response — used by the side-panel + waterfall update flow.

## InstancedMesh + Web Worker

- **One InstancedMesh per node-shape pool** (max ~22 shape types), pre-allocated at MAX_INSTANCES=8192. Use `setMatrixAt`/`setColorAt`; flip `instanceMatrix.needsUpdate=true` after batch.
- **Dynamic add/remove:** mutate `.count`, swap-with-last on removal, double on overflow + `dispose()`. Mirror `instances[]` side-table swap.
- **Picking via built-in raycaster** is sufficient at 3k. Critical: call `computeBoundingSphere()` after every batch flush or raycaster silently misses moved nodes.
- **Sphere geometry**: 192 triangles (widthSegments=12, heightSegments=8). Higher-tri spheres can lose to per-mesh at scale per mrdoob/three.js#30352.
- **Labels (three-spritetext)** stay as-is — per-label unique text breaks instancing's contract. Pool to ~150 visible max.
- **Memory at 3k**: ~1.4 MB instanceMatrix + ~270 KB instanceColor for 22 pools at 1024 cap each. <2 MB GPU.
- **Worker protocol**: 9 messages — init, tick, positions, add, remove, set-alpha, freeze, shutdown, error. Transferable ArrayBuffer (zero-copy) for positions. Double-buffer to allow overlap.
- **SharedArrayBuffer** requires COOP `same-origin` + COEP `require-corp`. Spec 1's foundation handler sets these when v2 is enabled. Fallback to transferable Float32Array if `crossOriginIsolated` is false.
- **Frozen positions + visibility-only updates** for time scrubbing (NOT re-simulation). O(N) per scrub frame.

## Waterfall density

**Strategy G primary**: `content-visibility: auto` + htmx server-paged chunks (`hx-trigger="revealed"`) + server-side aggregation. Zero JS bundle cost, htmx-native, degrades to plain pagination without JS.

**Aggregation rules** (server-side `Aggregate(rows []Row) []Row`): collapse adjacent rows when ALL of:
- same node type
- same parent
- gap < 50 ms
- run length ≥ 3
- no errors in run

**Soft-collapsible kinds**: `bus.event`, `tool.partial`, `stream.chunk`, `log.line`, `cache.*`, `prompt.token`, `model.heartbeat`.

**Hard-protected** (always shown): `task.*`, `mission.*`, `consensus.*`, `error.*`, `verify.*`, `merge.*`, `snapshot.*`.

Typically reduces 5k rows to 2-3k visible.

**Fallback Strategy H**: Clusterize.js (2.3 KB gz) if FPS telemetry beacon shows <50 on scroll. Gated behind a feature flag.

## Redaction UI

- **SVG lock glyph**, NOT emoji (rendering + SR pronunciation vary)
- **Color**: desaturate to ~15% saturation × 0.7 lightness (NOT red/yellow — those are reserved for failure/warning)
- **Reason wording**: specific cause (`"redacted by retention policy (90d)"`), never bare `"redacted"`. Avoid `"removed"`/`"deleted"`/`"hidden"` — wrong semantics for an append-only ledger.
- **Edges remain at full opacity** — topology is non-sensitive.
- **Reserved ⚠ overlay** when `isRedacted=true && len(events)==0` (anomaly: redaction-without-record).
- **A11y**: `aria-hidden="true"` on the lock SVG; meaning conveyed via adjacent `[content redacted]` text. Real `<ul>`/`<li>` for events list. Re-test contrast after desaturation — desat often drops below WCAG AA 4.5:1.

## Reference library + version table

| Library | Pin | Source | SRI source | License |
|---|---|---|---|---|
| htmx | 2.0.4 | github.com/bigskysoftware/htmx | upstream release notes | BSD-2-Clause |
| htmx-ext-sse | 2.2.4 | github.com/bigskysoftware/htmx-extensions | computed | BSD-2-Clause |
| three (ESM) | 0.170.0 | github.com/mrdoob/three.js | computed | MIT |
| three-spritetext | 1.9.5 | github.com/vasturiano/three-spritetext | computed | MIT |
| 3d-force-graph | 1.77.0 | github.com/vasturiano/3d-force-graph | computed | MIT |
| d3-force-3d | 3.0.5 | github.com/vasturiano/d3-force-3d | computed | ISC |

Total gzipped (estimated): ~110 KB chrome (htmx + ext) + ~140 KB three.js stack = ~250 KB. Right at spec budget.
