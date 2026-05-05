# RT-INSTANCEDMESH-PERF

## Topic

The r1-server v2 ledger graph viewer (`cmd/r1-server/ui/graph.js`) currently
renders nodes via `3d-force-graph`'s default `nodeThreeObject` path, which
allocates one `THREE.Mesh` per node. Spec `r1-server-ui-v2 §3.1` mandates
~3000 simultaneous nodes at smooth FPS, which is roughly the empirical
breakpoint where per-`Mesh` allocation collapses (each `Mesh` becomes a
separate draw call plus its own `BufferGeometry`/material binding cost).
This research file collects sourced data on `THREE.InstancedMesh` --
performance, dynamic resizing, picking, label coexistence, and memory --
and turns it into a concrete migration pattern the spec author can copy
into a checklist. Sources prioritise threejs.org docs, the threejs forum
(discourse.threejs.org), the mrdoob/three.js issue tracker, and 2024-2026
blog posts. All URLs were accessed 2026-05-05.

## Key findings

### 1. Draw-call reduction is large and well documented; raw FPS gain is workload-dependent

- `InstancedMesh` "render[s] a large number of objects with the same
  geometry and material(s) but with different world transformations"
  in a single draw call -- official docs.
  (https://threejs.org/docs/pages/InstancedMesh.html, accessed 2026-05-05)
- A real-estate optimisation case study moved from 9000 draw calls to 300
  by switching repeated furniture to instanced rendering -- Codrops
  "Building Efficient Three.js Scenes" (2025-02-11).
  (https://tympanus.net/codrops/2025/02/11/building-efficient-three-js-scenes-optimize-performance-while-maintaining-quality/, accessed 2026-05-05)
- VR Me Up devlog (2024-01-17), Quest 2 / ~2600 instances:
  - Naive (one `Mesh` per object): ~85 FPS
  - Naive `InstancedMesh` (no culling): ~55 FPS  <-- counterintuitively slower
  - After per-instance culling + LOD split: "nearly doubled" FPS
  (https://vrmeup.com/devlog/devlog_10_threejs_instancedmesh_performance_optimizations.html, accessed 2026-05-05)
- mrdoob/three.js issue #30352: with 5000 spheres at 50 segments, plain
  `Mesh` measured ~60 FPS while `InstancedMesh` measured ~25 FPS. Issue
  remained open without a maintainer fix as of access time. Implication:
  for fragment-heavy or fillrate-bound workloads, instancing alone is
  not a win -- you also need frustum culling and reasonable triangle
  budgets per instance.
  (https://github.com/mrdoob/three.js/issues/30352, accessed 2026-05-05)
- For our use case (low-poly node spheres, ~3000 instances, mostly CPU-
  driven force-layout updates), the dominant cost is JS overhead per
  `Mesh` (matrix updates, frustum calls, draw submission). `InstancedMesh`
  removes all of that, and our nodes are not fillrate-bound, so we are
  squarely in the "instancing wins big" regime rather than the #30352
  regression regime.

### 2. Dynamic add/remove: pre-allocate + mutate `.count`, do not recreate

- Official API: `count` is set in the constructor and there is no
  documented way to grow `instanceMatrix` after construction without
  allocating a new `InstancedMesh`. (threejs.org InstancedMesh docs)
- Forum-recommended pattern: "create the instanced mesh with the count =
  max number of instances you will ever have. Then ... set the
  `instancedMesh.count` property (always below the max), and only that
  amount of the matrices array will be rendered."
  (https://discourse.threejs.org/t/is-it-possible-to-optimize-instances-add-remove-instance-dynamically/44594, accessed 2026-05-05)
- Equivalent to a CPU-side "swap-with-last + decrement" pool. Removal of
  instance `i` = copy matrix at `count-1` into slot `i`, decrement
  `count`, mark `instanceMatrix.needsUpdate = true`.
- If you exceed the pre-allocated cap, you must dispose the old
  `InstancedMesh` and allocate a larger one (geometric growth
  recommended). `InstancedMesh.dispose()` "frees the GPU-related
  resources allocated by this instance." (threejs.org docs)
- Third-party `@three.ez/instanced-mesh` ("InstancedMesh2") wraps this
  pattern plus per-instance frustum culling, BVH-accelerated raycasting,
  per-instance visibility, and LOD. Forum-reported figures: 60+ FPS at
  50k animated instances with culling, "smooth at high framerates" with
  2M static instances at 85-88% cull rate.
  (https://discourse.threejs.org/t/instancedmesh2-easy-handling-and-frustum-culling/58622 -- accessed 2026-05-05;
   repo at https://github.com/agargaro/instanced-mesh)
  Treated below as an optional escape hatch, not a baseline dependency.

### 3. Picking: built-in raycaster works, returns `instanceId`

- `Raycaster.intersectObject(instancedMesh)` populates `intersection.instanceId`
  on hits. The official three.js example `webgl_instancing_raycast`
  demonstrates this pattern and is the standard reference.
  (https://discourse.threejs.org/t/raycaster-with-instancedmesh/10028 -- accessed 2026-05-05;
   https://discourse.threejs.org/t/raycast-highlight-with-instancedmesh/14777 -- accessed 2026-05-05)
- For ~3000 nodes at 60 Hz with one ray per mousemove, the CPU-side
  raycast with a fresh bounding sphere is fine. GPU picking only matters
  when (a) instances are animated in the vertex shader so the CPU side
  doesn't know their positions, or (b) you do many picks per frame.
  (https://discourse.threejs.org/t/best-way-to-do-instanced-mesh-picking-in-2024/59917 -- accessed 2026-05-05)
- Caveat: when `setMatrixAt` is called every frame (force-layout tick),
  you must call `computeBoundingSphere()` after the matrix flush or the
  raycaster will miss instances that drifted outside the stale sphere.
  Docs: "The engine automatically computes the bounding sphere when it
  is needed, e.g., for ray casting or view frustum culling" -- but only
  the first time; subsequent matrix changes invalidate it.
  (threejs.org InstancedMesh docs)

### 4. Per-instance metadata via a side-table indexed by `instanceId`

- The picking handler only gets back an integer `instanceId`. To map
  back to the ledger node ID, keep a parallel array
  `nodeBySlot: SourceNode[]` of length `count`, indexed by slot.
- When swap-with-last happens during removal, mirror the swap on
  `nodeBySlot` so indices stay coherent.
- This is the established pattern in every "instance picking" thread on
  the three.js forum -- nothing in three.js itself stores user data per
  instance. (synthesised from forum links above; no single canonical URL)

### 5. Per-instance color and scale: `instanceColor` is opt-in; bake scale into the matrix

- `instanceMatrix` is always allocated: 16 floats * 4 bytes = 64 bytes
  per instance. At `count=3000` that is 192 KB; at `count=10000` it is
  640 KB. Trivial vs N separate `Mesh` allocations (each Mesh carries
  its own `Object3D` + matrix world cache + `Layers` + frustum bookkeeping
  in JS, conservatively 1-2 KB JS heap each, plus driver-side overhead).
  (threejs.org InstancedMesh docs; "instanceMatrix stores 16 floats per
  instance ... composed out of several vec4 attributes" --
  https://discourse.threejs.org/t/instancedmesh-for-simple-geometries/28658 -- accessed 2026-05-05)
- `instanceColor` is `null` by default. It is allocated lazily on the
  first `setColorAt`. Cost: 3 floats per instance (12 bytes) when used.
  (threejs.org InstancedMesh docs; "Adding color would instantiate
  another buffer which is wasteful if not used" --
  https://discourse.threejs.org/t/how-to-change-texture-color-per-object-instance-in-instancedmesh/11271 -- accessed 2026-05-05)
- There is no built-in `instanceScale`. Per-instance scale is encoded
  by composing scale into the 4x4 you pass to `setMatrixAt`. Use a
  reusable scratch `Matrix4` + `Vector3` to avoid allocations in the
  per-frame layout loop.

### 6. Frustum culling and depth: `InstancedMesh` is one-frustum

- The whole `InstancedMesh` is culled or not as a unit by default. If
  the bounding sphere encloses all instances, every instance is
  rasterised even if 90% are off-screen. This is the trap that hit
  the VR Me Up devlog and forced their `mesh.count` LOD trick.
- Mitigations, in increasing order of effort:
  1. Split into multiple `InstancedMesh` by region or node-type so each
     sub-mesh has a tighter bounding sphere.
  2. Sort the matrix array by camera Z and shrink `count` to skip the
     far tail.
  3. Switch to `@three.ez/instanced-mesh` for free per-instance culling.
- For transparent materials, depth-sorting per instance is not
  performed; far-to-near order depends on the order matrices are
  packed in `instanceMatrix`. Keep node materials opaque (or use
  alpha-test rather than alpha-blend) to avoid this entirely.

### 7. `three-spritetext` labels: keep as a separate sprite layer; do NOT instance them

- `three-spritetext` produces one `THREE.Sprite` per label, each with
  its own canvas-rendered texture atlas. Sprites are already a 2-tri
  geometry rendered as a billboard, so the per-mesh overhead per label
  is small relative to a 32-segment sphere -- but at 10000 unique-text
  labels both `spriteText` and "canvas-as-texture" approaches were
  reported to "result in huge performance drops."
  (https://discourse.threejs.org/t/performant-approach-for-displaying-text-labels-10000/21863 -- accessed 2026-05-05)
- Forum consensus for label-heavy graphs: don't render every label.
  Use a pooled set (~100 labels rendered with Troika SDF text in the
  near band, sprite/plane indicators in the mid band, point sprites
  at far range, nothing below 1 px). Same source.
- Critical: per-label text differs (each node has a unique ID/snippet),
  so `InstancedMesh` does not apply -- the text-as-texture is exactly
  the per-instance variation that breaks instancing's "same material,
  same geometry" precondition. (Implicit from the InstancedMesh
  contract; called out in the forum thread above.)
- Recommendation: leave `three-spritetext` exactly as `3d-force-graph`
  installs it, but gate visibility by camera distance / hover so that
  on a 3000-node graph only ~50-150 labels are actually instantiated
  at any given time. The instancing migration applies to the node
  geometry only.

### 8. `3d-force-graph` integration surface

- `3d-force-graph` exposes `nodeThreeObject(node) -> Object3D` and
  `nodeThreeObjectExtend(false)` to fully replace the default node
  rendering. To use a single `InstancedMesh` for all nodes, the
  cleanest pattern is:
  - Set `nodeThreeObject` to return an empty `THREE.Object3D()` (so
    `3d-force-graph` still tracks node positions on it),
  - Add a single `InstancedMesh` to `graph.scene()`,
  - On the `onEngineTick` callback, copy each node's `.x/.y/.z` into
    the corresponding instance matrix.
  (https://github.com/vasturiano/3d-force-graph -- accessed 2026-05-05;
   https://www.npmjs.com/package/3d-force-graph -- accessed 2026-05-05)

## Recommendation for r1-server-ui-v2

Adopt the following concrete pattern. Each bullet is small enough to
become a single spec checklist item.

### Architecture: single `InstancedMesh` per node-type, tick-driven matrix sync

1. Vendor exactly what's already vendored: `three.min.js`,
   `three-spritetext.min.js`, `3d-force-graph.min.js`. Do NOT pull in
   `@three.ez/instanced-mesh` for the v2 milestone -- core
   `THREE.InstancedMesh` is sufficient at our scale and avoids a new
   vendor blob. (Reconsider if profiling shows >40% of frame time in
   GPU rasterisation of off-screen instances; see "Implementation
   gotchas" item on culling.)

2. Create one `InstancedMesh` per **node-type** (the ledger has a
   bounded set: `Mission`, `Branch`, `Loop`, `Decision`, `Skill`, etc.
   -- count from `ledger/nodes/`). Reasons: (a) different node types
   may want different geometries/materials in future, (b) per-type
   sub-meshes have tighter bounding spheres which improves
   per-frustum culling without per-instance culling, (c) easier to
   toggle visibility of a whole type for filter UI.

3. Pre-allocate each `InstancedMesh` with `INITIAL_CAP = 1024` slots.
   Track `count` separately in a JS pool object. On overflow, dispose
   the existing mesh and allocate a new one at `Math.max(cap*2,
   needed)`. Doubling keeps amortised cost O(1) per insertion.

4. Geometry: a single shared `THREE.SphereGeometry(1, 16, 12)`
   (or `IcosahedronGeometry(1, 1)` for fewer triangles) per node-type
   instance pool. 192-256 triangles per node is the sweet spot --
   well below the fillrate-bound regime that causes the
   issue-#30352 regression, while still looking smooth.

5. Material: `MeshBasicMaterial` (no lighting cost) or
   `MeshLambertMaterial` if we want subtle directional shading. Use
   `transparent: false` to avoid per-instance depth sort issues.
   Hover-highlight is done via per-instance color, not via opacity.

6. Per-instance scale (e.g. node "size") is baked into the matrix
   passed to `setMatrixAt`. Allocate one reusable
   `THREE.Matrix4`, `THREE.Vector3` (position), and
   `THREE.Vector3` (scale) at module scope -- never in the loop.

7. Per-instance color: enable `instanceColor` only when we actually
   want hover highlight or per-stance tinting. Allocate it lazily on
   first `setColorAt`. Default tint = node-type color from the
   palette, set once on insertion, mutated only on hover.

### Picking and hover

8. Use the built-in raycaster: `raycaster.intersectObjects([...allInstancedMeshes])`,
   pick the first hit, read `intersection.instanceId` and
   `intersection.object` (the per-type `InstancedMesh`), and look up
   `nodeBySlot[type][instanceId]` in our side-table to get the
   ledger node ID.

9. After every force-layout tick that calls `setMatrixAt` for any
   instance, set `instancedMesh.instanceMatrix.needsUpdate = true`
   AND call `instancedMesh.computeBoundingSphere()` once at the end
   of the tick (not per instance). Without the recompute, the
   raycaster will silently miss recently-moved nodes.

10. Throttle hover raycasts to mousemove + rAF, not to every native
    mousemove event. One raycast per frame is plenty.

11. On hover-in: `getColorAt(id, savedColor)`, then
    `setColorAt(id, hoverColor)`, set `instanceColor.needsUpdate = true`.
    On hover-out: restore `savedColor`. Same for "selected" state but
    with a different colour and remembered separately.

### Add / remove

12. Insert: pick first free slot in the pool (free-list or just
    `count++`), `setMatrixAt(slot, m)`, `setColorAt(slot, c)`,
    push `nodeBySlot[type][slot] = ledgerNode`, mark both buffers
    `needsUpdate = true`.

13. Remove: swap-with-last. Copy matrix and color from `count-1`
    into `slot`, update `nodeBySlot[type][slot]`, decrement `count`,
    mark buffers dirty. Notify any code holding `slot` references
    (e.g. selection state) that slot `count-1` moved to `slot`.
    Simplest: hold ledger-node IDs in selection state, not slot
    indices, and re-resolve to slot at use time.

14. Size grows past `cap`: build new `InstancedMesh` at `cap*2`,
    copy `instanceMatrix` and `instanceColor` BufferAttribute arrays
    over with `set()`, swap into scene, dispose old.

### Labels (`three-spritetext`)

15. Leave the existing `3d-force-graph` sprite-label layer unchanged.
    Do NOT attempt to instance text -- per-instance unique text
    breaks the InstancedMesh contract and forum consensus is that
    the alternatives (Troika SDF, MSDF, canvas-atlas) are out of
    scope for this milestone.

16. Add a per-frame visibility filter: only show sprite labels for
    nodes within distance `D` of the camera AND whose projected
    pixel size is >= 12 px. Cap to ~150 visible labels max. This
    keeps the sprite layer in the regime where it is fast.

### Memory budget at 3000 nodes

17. Per-node-type pool capacity 1024 (rounds up from typical type
    populations of 100-800), N node types ~= 22 (per `ledger/nodes/`)
    -> upper-bound 22 * 1024 = 22528 instance slots. Memory:
    `instanceMatrix` 22528 * 64 B = ~1.4 MB, `instanceColor` 22528 * 12 B =
    ~270 KB. Total under 2 MB of GPU buffer for the entire node layer.
    Compare to current path: 3000 * (`Mesh` JS overhead + per-mesh
    geometry binding) which is the actual culprit.

### Validation gates (suggested for the spec checklist)

18. FPS gate: render 3000 synthetic nodes, manually orbit the camera,
    sustain >= 50 FPS in Chromium on a 2024-class laptop iGPU. Fail
    the milestone otherwise.

19. Draw-call gate: open Spector.js (or Chrome DevTools Performance
    GPU panel), confirm node-layer draw-call count <= number of
    node-types (target: 22). Each `InstancedMesh` is one draw call.

20. Picking gate: hover-test must respond within one frame and must
    correctly resolve to the underlying ledger node ID for at least
    1000 random nodes (automated test, not manual).

## Implementation gotchas

- `instancedMesh.frustumCulled` defaults to `true`. The bounding
  sphere is computed from the geometry, NOT from the populated
  matrices. If you don't call `computeBoundingSphere()` after moving
  instances, the entire `InstancedMesh` may be culled when its
  per-geometry sphere drifts off-camera, even though instances are
  still on-camera. Always recompute after a tick. (threejs.org docs)
- `instanceMatrix.usage` defaults to `StaticDrawUsage`. For a
  force-layout that updates every frame, set
  `instancedMesh.instanceMatrix.setUsage(THREE.DynamicDrawUsage)`
  immediately after construction or you will get a static-buffer
  upload pattern that some drivers handle poorly.
- `setMatrixAt(i, m)` does NOT mark the buffer dirty. You MUST set
  `instanceMatrix.needsUpdate = true` after the batch. Single
  biggest source of "my changes don't show up" bug reports on the
  forum. (threejs.org docs)
- Same applies to `setColorAt` / `instanceColor.needsUpdate`.
- Disposing an `InstancedMesh` does NOT dispose its `geometry` or
  `material`. If you reallocate during grow, dispose the
  `InstancedMesh` but keep the geometry/material singletons.
- Bounding sphere recompute is O(count). At 22 sub-meshes * up to
  1024 instances each * 60 Hz this is ~1.3M ops/sec -- still fine,
  but if profiling shows it as hot, recompute every Nth tick or
  only when `count` changes.
- Bug report mrdoob/three.js #30352 (~5000 spheres at 50 segs:
  Mesh 60 FPS, InstancedMesh 25 FPS) is real and unresolved. Two
  defences: (a) keep our per-node geometry low-poly (16x12 segs,
  ~192 tris), (b) avoid heavy fragment shaders. We are not in the
  pathological regime.
- `Object3D.matrixAutoUpdate` does nothing on an `InstancedMesh`'s
  per-instance transforms -- you own the matrix array. The
  `InstancedMesh` itself, like any `Object3D`, still has its own
  world matrix; leave it at identity unless you intend to translate
  the entire graph.
- `3d-force-graph` calls `nodeThreeObject` once per node and reuses
  the returned `Object3D` to drive position. If you return an empty
  `Object3D()`, `3d-force-graph` will set its `.position` per tick
  -- you can read those positions in your own
  `onEngineTick` and copy into the instance matrix. Do NOT also
  `add` the empty object to a parent that gets rendered, or you
  pay the very `Object3D` overhead you were trying to remove.
- Hover-highlight via `instanceColor` requires the mesh's material
  to actually consume vertex color. `MeshBasicMaterial` and
  `MeshLambertMaterial` do this when `vertexColors` is true OR when
  `instanceColor` is non-null (three.js auto-injects the chunk).
  Confirm in a quick smoke test before committing the colour
  pipeline.
- `dispose()` on an `InstancedMesh` does not remove it from the
  scene graph. Call `scene.remove(mesh)` first, then `dispose()`.
- Selection / hover state should key off ledger node ID, NOT
  `(instanceMesh, slot)`. Slots move under you on every removal
  due to swap-with-last.
- If we ever need >5000 nodes per type or animated GPU-vertex
  motion, switch to `@three.ez/instanced-mesh` (per-instance
  culling, BVH picking, dynamic add/remove out of the box,
  reported 60+ FPS at 50k animated instances). Do not preempt
  this; revisit when measured.

## Sources

All URLs accessed 2026-05-05.

- three.js InstancedMesh official docs --
  https://threejs.org/docs/pages/InstancedMesh.html
- three.js Raycaster official docs --
  https://threejs.org/docs/pages/Raycaster.html
- mrdoob/three.js issue #30352, "InstancedMesh significantly slower
  than Mesh with shared attributes" --
  https://github.com/mrdoob/three.js/issues/30352
- mrdoob/three.js issue #17906, "InstancedMesh how to use raycast
  for every instance?" --
  https://github.com/mrdoob/three.js/issues/17906
- mrdoob/three.js PR #17505, original InstancedMesh implementation --
  https://github.com/mrdoob/three.js/pull/17505
- threejs forum, "Best way to do Instanced Mesh picking in 2024?" --
  https://discourse.threejs.org/t/best-way-to-do-instanced-mesh-picking-in-2024/59917
- threejs forum, "Raycast Highlight with InstancedMesh" --
  https://discourse.threejs.org/t/raycast-highlight-with-instancedmesh/14777
- threejs forum, "Raycaster with InstancedMesh" --
  https://discourse.threejs.org/t/raycaster-with-instancedmesh/10028
- threejs forum, "Highlighting an instance in InstancedMesh" --
  https://discourse.threejs.org/t/highlighting-an-instance-in-instancedmesh/14776
- threejs forum, "Is it possible to optimize instances - add/remove
  instance dynamically?" --
  https://discourse.threejs.org/t/is-it-possible-to-optimize-instances-add-remove-instance-dynamically/44594
- threejs forum, "Directly Remove InstancedMesh Instance?" --
  https://discourse.threejs.org/t/directly-remove-instancedmesh-instance/25504
- threejs forum, "InstancedMesh add/remove" --
  https://discourse.threejs.org/t/instancedmesh-add-remove/23222
- threejs forum, "Modified THREE.InstancedMesh dynamically instancecount" --
  https://discourse.threejs.org/t/modified-three-instancedmesh-dynamically-instancecount/18124
- threejs forum, "InstancedMesh for simple geometries?" --
  https://discourse.threejs.org/t/instancedmesh-for-simple-geometries/28658
- threejs forum, "How to change texture/color per object instance in
  InstancedMesh" --
  https://discourse.threejs.org/t/how-to-change-texture-color-per-object-instance-in-instancedmesh/11271
- threejs forum, "InstancedMesh2 - Easy handling and frustum culling" --
  https://discourse.threejs.org/t/instancedmesh2-easy-handling-and-frustum-culling/58622
- threejs forum, "Performant approach for displaying text labels ~10000" --
  https://discourse.threejs.org/t/performant-approach-for-displaying-text-labels-10000/21863
- threejs forum, "Bad performance when loading more than 3500 meshes
  into the scene" --
  https://discourse.threejs.org/t/bad-performance-when-loading-more-than-3500-meshes-into-the-scene/63960
- threejs forum, "InstancedMesh and Sprite" --
  https://discourse.threejs.org/t/instancedmesh-and-sprite/68091
- VR Me Up devlog (2024-01-17), "Three.js InstancedMesh Performance
  Optimizations" --
  https://vrmeup.com/devlog/devlog_10_threejs_instancedmesh_performance_optimizations.html
- Codrops (2025-02-11), "Building Efficient Three.js Scenes" --
  https://tympanus.net/codrops/2025/02/11/building-efficient-three-js-scenes-optimize-performance-while-maintaining-quality/
- Codrops (2025-07-10), "Three.js Instances: Rendering Multiple Objects
  Simultaneously" --
  https://tympanus.net/codrops/2025/07/10/three-js-instances-rendering-multiple-objects-simultaneously/
- Three.js Roadmap, "Draw Calls: The Silent Killer" --
  https://threejsroadmap.com/blog/draw-calls-the-silent-killer
- Utsubo (2026), "100 Three.js Tips That Actually Improve Performance" --
  https://www.utsubo.com/blog/threejs-best-practices-100-tips
- Three.js Journey, "Performance tips" --
  https://threejs-journey.com/lessons/performance-tips
- React Three Fiber, "Scaling performance" --
  https://r3f.docs.pmnd.rs/advanced/scaling-performance
- Wael Yasmina, "Instanced Rendering in Three.js" --
  https://waelyasmina.net/articles/instanced-rendering-in-three-js/
- Medium, Dusan Bosnjak, "Instancing with three.js -- Part 1" --
  https://medium.com/@pailhead011/instancing-with-three-js-36b4b62bc127
- Medium, Leanne Werner, "Troubleshooting InstancedMesh in Three.js" --
  https://medium.com/@leannewerner/troubleshooting-instancedmesh-in-three-js-65e68b0a9753
- vasturiano/3d-force-graph repo --
  https://github.com/vasturiano/3d-force-graph
- 3d-force-graph npm --
  https://www.npmjs.com/package/3d-force-graph
- 3d-force-graph live demo --
  https://vasturiano.github.io/3d-force-graph/
- agargaro/instanced-mesh ("InstancedMesh2" / @three.ez) --
  https://github.com/agargaro/instanced-mesh
- lume/three-instanced-mesh --
  https://github.com/lume/three-instanced-mesh
