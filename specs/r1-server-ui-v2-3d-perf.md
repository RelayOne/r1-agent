<!-- STATUS: done -->
<!-- CREATED: 2026-05-05 -->
<!-- BUILD_STARTED: 2026-05-05 -->
<!-- BUILD_COMPLETED: 2026-05-05 -->
<!-- DEPENDS_ON: r1-server-ui-v2-foundation -->
<!-- BUILD_ORDER: 29 -->

# r1-server UI v2 — 3D Perf (InstancedMesh + Web Worker)

## 1. Overview

The MVP `cmd/r1-server/ui/graph.js` allocates one `THREE.Mesh` per ledger node and runs `d3-force-3d` on the main thread. At ~1k nodes the FPS collapses to <15 and the page becomes unresponsive during simulation. Spec r1-server-ui-v2 §3 describes the fix: switch to a single `THREE.InstancedMesh` per node-shape pool, move the force simulation into a Web Worker, and freeze positions once converged so the time-scrubber operates as a per-instance visibility update (O(N)) instead of a re-simulation.

This spec is the perf refactor. It depends on the **foundation** spec landing first (the vendored `three.module.js` + `d3-force-3d.js` + import map.)

## 2. Stack & Versions

<!-- RESOLVED: InstancedMesh @ 192-tri spheres (widthSeg=12, heightSeg=8) hits ~110 FPS at 2600 instances with culling+LOD; high-segment geometry can lose at scale per mrdoob/three.js#30352. Pre-allocate at MAX=8192, mutate .count, swap-with-last on removal, computeBoundingSphere() after every batch flush or raycaster silently misses moved nodes. -->
<!-- RESOLVED: 9-message protocol (init, tick, positions, add, remove, set-alpha, freeze, shutdown, error). Transferable ArrayBuffer + double-buffering for positions. -->
<!-- RESOLVED: COOP `same-origin` + COEP `require-corp` enable SharedArrayBuffer when crossOriginIsolated; otherwise fall back to transferable Float32Array (zero-copy via postMessage[..., [buffer]]). -->

- three.js (vendored ES module form): per-instance matrix + per-instance color + raycaster picking
- d3-force-3d: standalone (NOT through 3d-force-graph) inside `web/js/graph-worker.js`
- 3d-force-graph: kept as the rendering wrapper but its built-in simulation is bypassed (`engine: false` or equivalent escape hatch — confirm via research)
- COOP/COEP headers may be required for SharedArrayBuffer; if so the import map page sets `Cross-Origin-Opener-Policy: same-origin` + `Cross-Origin-Embedder-Policy: require-corp`. Otherwise fall back to structured-clone postMessage with a Float32Array transferable.

## 3. Architecture

```
┌───────────── main thread ──────────────┐         ┌──── Worker ────┐
│ web/js/graph.js                         │         │ graph-worker.js│
│  ├─ InstancedMesh per node-shape (16+)  │  ←──────┤  d3-force-3d   │
│  ├─ raycaster picking → instanceId      │  Float32Array positions │
│  ├─ time-scrubber: per-instance visi    │  ──────→ {nodes, edges} │
│  └─ camera controls (OrbitControls)     │         └────────────────┘
└─────────────────────────────────────────┘
```

### 3.1 InstancedMesh refactor

- One `THREE.BufferGeometry` per node *shape* (the existing 11 shapes from MVP graph.js: sphere, cube, diamond, octahedron, cone, icosahedron, cylinder, plane, torus, hex_prism, ring + variants — confirm exact list against current code).
- Each shape gets one `THREE.InstancedMesh(geometry, MeshLambertMaterial, MAX_INSTANCES=8192)`.
- Per-instance state held in three Float32Array buffers:
  - `instanceMatrix` (16 floats × N) — written via `setMatrixAt(i, matrix4)` per tick; `instanceMatrix.needsUpdate = true` after the batch.
  - `instanceColor` (3 floats × N) — written via `setColorAt(i, color)` once per state change (verification status, redacted desat, skill opacity).
  - Side-table `instances[i] = nodeId` so raycaster hits map back to ledger nodes.
- A separate "label layer" remains as `three-spritetext` instances but only for nodes within camera near-frustum OR currently selected. NOT one sprite per node — the existing graph.js renders all sprite labels and that's a big chunk of the perf cost.

### 3.2 Web Worker layout

- `web/js/graph-worker.js` (a vanilla ES module worker; loaded via `new Worker('/ui/web/js/graph-worker.js', { type: 'module' })`).
- Imports `d3-force-3d` from `/ui/web/vendor/d3-force-3d.js` via the import map.
- Receives `{ kind: 'init', nodes: [...], edges: [...] }` on first message. Initialises the simulation but does NOT call `start()` — the main thread requests ticks.
- Receives `{ kind: 'tick' }` and posts `{ kind: 'positions', positions: Float32Array, alpha: number }` back. The position buffer is transferred (not copied) when SharedArrayBuffer isn't available; copied via structured clone with `transferable: [positions.buffer]` otherwise.
- Receives `{ kind: 'add', node, neighbors }` for streaming insert; inserts at mean-of-neighbors position; restarts at `alpha(0.3)` (NOT `alpha(1).restart()` — that re-jiggles the entire layout).
- Receives `{ kind: 'shutdown' }` on tab close — calls `simulation.stop()` and `self.close()`.

### 3.3 Time scrubber

Once the simulation cools (`alpha < 0.02`) positions are frozen. The scrubber slider:

- Sets each instance's scale (via `instanceMatrix`) to `0` if `node.created_at > cursor` else its styled size.
- For redacted nodes whose `redacted_at > cursor`, render normally; at/after cursor, desaturate via `setColorAt`.
- For skill nodes between `skill_loaded` and subsequent `skill_unloaded`, opacity 1.0; after `skill_unloaded`, opacity 0.3.
- This is an O(N) visibility update per scrub tick, NO re-simulation. The worker is idle during scrub.

### 3.4 Focused subtree view

- Shift-click a node OR click "Focus subtree" in the side panel: BFS 1–3 hops from the selected node; non-focused instances scale → 0.15 (or fade alpha to 0.15 via `setColorAt` lerp); camera animates to the BFS bounding box.
- Toolbar "Global view" button restores all instances to their unfocused scale + color.

## 4. Picking (raycaster → instanceId → nodeId)

Hover/click uses `THREE.Raycaster.intersectObject(instancedMesh)`. The hit returns `intersection.instanceId`. Look up `instances[instanceId]` to get the nodeId, then call out to the side-panel partial via htmx.

<!-- RESOLVED: built-in raycaster sufficient at 3k nodes; returns `instanceId`. GPU-picking via per-instance unique colors only needed if profiling shows raycaster bottleneck (deferred). -->

If raycaster perf is inadequate at scale, fall back to GPU-picking via a hidden render target with per-instance unique colors (see RT recommendation).

## 5. Boundaries — what NOT to do

- Do not delete the existing `cmd/r1-server/ui/graph.js` — it's the v1 SPA's graph and is not gated by `R1_SERVER_UI_V2`. The new graph code lives in `cmd/r1-server/ui/web/js/graph.js` + `graph-worker.js`.
- Do not run d3-force-3d on the main thread — that's the entire point of moving to a worker.
- Do not pre-render labels for all nodes; that's the second-largest perf cost after per-mesh allocation.
- Do not use `THREE.LineSegments` for edges — at 3k+ nodes use `LineSegments2` (from `three/examples/jsm/lines`) or thin `MeshLineMaterial`. Either way, edges are also instanced.
- Do not re-simulate on time scrubbing. Frozen positions + visibility-only updates.

## 6. Testing

- `cmd/r1-server/ui/web/js/graph-worker.test.js`: a vitest test running the worker logic against a fixture `{nodes, edges}` and asserting positions converge in N ticks. Loads via vitest's worker support.
- `cmd/r1-server/graph_perf_test.go` (Playwright + headless Chromium, gated behind `//go:build e2e`): renders 3000 fixture nodes, scrolls/scrubs, asserts mean FPS ≥ 30 over 5s window. Skipped in default CI; runs on a release-rehearsal lane.
- Side-table integrity: a Playwright test that hovers each instance in a 50-node fixture, captures the resulting side-panel `data-node-id`, asserts each instance maps back to a unique node.

## 7. Implementation checklist (11 items — self-contained)

### Worker

- [ ] Write `cmd/r1-server/ui/web/js/graph-worker.js`: a module-type Web Worker that imports `d3-force-3d` via the import map, accepts `{kind:'init',nodes,edges}` / `{kind:'tick'}` / `{kind:'add',node,neighbors}` / `{kind:'shutdown'}` messages, and posts `{kind:'positions',positions:Float32Array,alpha:number}` back. Use `postMessage({...}, [positions.buffer])` so the buffer is transferred (zero-copy). Wire shutdown to `self.close()` after flushing the simulation.
- [ ] Add a SharedArrayBuffer fallback path in `graph-worker.js`: if `crossOriginIsolated && self.SharedArrayBuffer` is true, allocate the positions buffer once at `init` and reuse across ticks; otherwise allocate per tick and rely on the transferable contract. Document the COOP/COEP headers needed in `cmd/r1-server/ui_v2_foundation.go` (set when v2 is enabled).

### InstancedMesh main-thread renderer

- [ ] Write `cmd/r1-server/ui/web/js/graph.js`: a module that imports `three`, `OrbitControls`, `three-spritetext` via the import map; creates one `InstancedMesh(geom, MeshLambertMaterial, 8192)` per node shape (port the 11 shapes from the existing `cmd/r1-server/ui/graph.js`); maintains a side-table `instances[i] = nodeId` keyed by instance index; subscribes to the worker's positions stream and writes `setMatrixAt`/`instanceMatrix.needsUpdate=true` per tick.
- [ ] Implement raycaster picking in `graph.js` per §4: on `pointermove` raycast through all InstancedMeshes; if hit, look up `instances[hit.instanceId]` and call `htmx.ajax('GET', '/api/session/'+sid+'/node/'+nodeId, '#side-panel')` to swap the side panel. Throttle with `requestAnimationFrame` so we hover-pick at most once per frame.
- [ ] Implement label layer in `graph.js`: maintain a small `Set<nodeId>` of labelled nodes (selected + camera-near-frustum, capped at ~50). On each frame, frustum-cull and update sprite positions. Do NOT instantiate a `three-spritetext` per node — recycle sprites from a pool of size 64.

### Time scrubber + focus

- [ ] Implement `web/js/scrubber.js`: a vanilla-JS island bound to `#timeline-scrubber`. Reads `data-cursor` from each row in the waterfall (set server-side by Spec 3) and updates `instanceMatrix` scale per node-state in the 3D graph (post a `{kind:'visibility',cursor,visibility:Uint8Array}` message to the main thread → graph.js applies). No worker involvement.
- [ ] Implement focused-subtree view in `graph.js`: shift-click handler runs BFS 1-3 hops over the in-memory edge list, computes an axis-aligned bounding box, animates the camera (ease-out 800 ms), fades non-focused instances by writing `setColorAt(i, color.lerp(grey, 0.85))`. A "Global view" toolbar button restores via the cached pre-focus color buffer.

### Streaming insert

- [ ] Wire streaming insert path: when the SSE endpoint pushes a new node event (`event: ledger.node.append`), `graph.js` parses it, finds neighbour positions in the cached final-positions buffer, posts `{kind:'add',node,neighbors}` to the worker. Worker inserts at mean-of-neighbours, restarts simulation at `alpha(0.3)`, posts position deltas. Main thread only updates the affected instance's `setMatrixAt`.

### Feature flag + entry point

- [ ] Update `cmd/r1-server/ui_v2_foundation.go` (from Spec 1) so the `serveGraphIndex` handler renders `web/session-graph.html` when `R1_SERVER_UI_V2=1`. The template extends `base.html`, declares `<canvas data-island="graph">`, and loads `/ui/web/js/graph.js` as a module. Pre-v2 path keeps serving the existing `cmd/r1-server/ui/graph.html`.
- [ ] Write `cmd/r1-server/ui/web/session-graph.html`: extends `base.html`, has a top toolbar (`Waterfall ✓`, `3D Graph ✓`, `Stream`, `Memories`), a `<canvas data-island="graph">`, a side panel container `#side-panel`, and a footer with the timeline scrubber `<input type="range" id="timeline-scrubber">`.

### Tests

- [ ] Add `cmd/r1-server/ui/web/js/graph-worker.test.js` (vitest, jsdom env, web/ workspace): drives the worker through init+tick cycles against a 50-node fixture; asserts the simulation converges (`alpha < 0.02`) within 200 ticks and the final positions are stable across re-runs (deterministic seed).
- [ ] Add `cmd/r1-server/graph_e2e_test.go` (Playwright + chromium, `//go:build e2e`): renders 3000 fixture nodes, scrolls + scrubs + focuses; asserts mean FPS ≥ 30 over a 5 second window AND no console errors. Skipped from default `go test ./...`; runs in release-rehearsal CI lane only.

## 8. Acceptance

- `go test ./cmd/r1-server/...` clean.
- vitest in web/ passes new `graph-worker.test.js`.
- Manual: `R1_SERVER_UI_V2=1 go run ./cmd/r1-server` + load `/session/<id>/graph` in Chromium with a 1k-node session, see ≥45 FPS in DevTools perf panel during simulation; ≥60 FPS once frozen; scrubber updates visually with no jank.
- E2E lane: 3k-node fixture renders + scrubs + focuses without console errors; mean FPS ≥ 30.
