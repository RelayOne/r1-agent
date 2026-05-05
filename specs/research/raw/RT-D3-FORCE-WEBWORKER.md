# RT-D3-FORCE-WEBWORKER

## Topic

r1-server's ledger graph viewer (`cmd/r1-server/ui/graph.js`) currently lets
3d-force-graph drive its internal d3-force-3d simulation on the main thread.
At ~1k+ nodes the per-frame Verlet step plus three.js rendering competes for
the same JS thread and visibly janks the UI. Spec **r1-server-ui-v2 §3.2**
requires that the simulation run inside a dedicated Web Worker, with
positions streamed back to the main thread for rendering.

This research note answers the six concrete questions needed to land that
change without a JS build step (everything is served same-origin via Go
`embed.FS`):

1. Can d3-force-3d be factored out of 3d-force-graph and driven externally?
2. Position-array transfer: SharedArrayBuffer vs transferable ArrayBuffer.
3. Per-frame protocol (rate, request/response shape, backpressure).
4. Cancellation / cleanup when the user closes the graph tab.
5. Empirical FPS at 1k / 3k node counts with this architecture.
6. Compatibility with vendored, no-CDN worker scripts behind `embed.FS`.

A companion file already exists at
`cmd/r1-server/ui/graph-worker.js` (232 lines, skeleton) and we already
vendor `3d-force-graph.min.js`, `three.min.js`, `three-spritetext.min.js`
in `cmd/r1-server/ui/vendor/`.

---

## Key findings

### 1. 3d-force-graph allows external position control via `nodePositionUpdate`

- `3d-force-graph` exposes `forceEngine('d3' | 'ngraph')` to swap engines but
  does **not** allow injecting an arbitrary engine.
  Source: <https://github.com/vasturiano/3d-force-graph> (README).
- The library exposes:
  - `pauseAnimation()` / `resumeAnimation()` — freeze the render+sim loop.
  - `nodePositionUpdate(fn(nodeObject, coords, node))` — a per-frame hook
    that, **when it returns truthy, overrides default position updates**.
    The default updater reads `node.x|y|z` set by the internal d3-force
    tick. If we install our own updater that copies positions out of a
    Float32Array (filled by the worker), we bypass the internal sim
    entirely.
  - `d3Force(name, force)` — set forces to no-ops (or `null`) so the
    internal d3 sim does effectively zero work per tick.
  - `cooldownTicks(0)` and `cooldownTime(0)` — stop the internal sim
    immediately after first tick.
- This means we do **not** need to fork or vendor a custom build of
  3d-force-graph. The recipe:
  1. Disable internal forces (`graph.d3Force('charge', null)` etc.) and set
     `cooldownTicks(0)`.
  2. Install `nodePositionUpdate` that reads from the latest worker frame
     and returns `true` (signaling "positions handled, skip default").
  3. Spawn a worker that imports a vendored `d3-force-3d` build and runs
     the real simulation.

### 2. d3-force-3d works in a Worker; recommended pattern is well-documented

- d3-force-3d has **no DOM dependencies** — it is pure math over
  `{x,y,z,vx,vy,vz}` records. Source:
  <https://github.com/vasturiano/d3-force-3d> README.
- Distributed as both ESM and UMD. UMD lets us drop the vendored bundle
  into the Worker via classic `importScripts()`, which is the path of
  least resistance for our `embed.FS` setup.
- D3 docs explicitly recommend Workers for large graphs: "For large
  graphs, static layouts should be computed in a web worker to avoid
  freezing the user interface."
  Source: <https://d3js.org/d3-force/simulation>.
- d3-force-3d ticking API: `simulation.stop()` to disable the internal
  timer, then call `simulation.tick(n)` manually inside the worker. Tick
  events from `.on('tick', ...)` are NOT dispatched for manual ticks, so
  the worker must explicitly post after each tick.
  Source: D3 README v3.0.0 (above d3js.org link).

### 3. Two reference patterns for the message protocol

**Bostock pattern (static, batched):**
- Main → Worker: one `{nodes, links}` payload.
- Worker runs `simulation` to completion (`.tick(N)` then `.stop()`),
  posts back final positions once.
- Used by:
  - <https://gist.github.com/mbostock/01ab2e85e8727d6529d20391c0fd9a16>
  - <https://observablehq.com/@d3/force-directed-web-worker>
- Good for "compute once, render frozen layout."
- **NOT what we want**: ledger graph must animate live as the user
  drags nodes, expands neighborhoods, etc.

**Live streaming pattern (markuslerner/d3-webworker-pixijs):**
- Worker runs the sim continuously at a lower framerate.
- After every N ticks it posts the position array back to main.
- Main thread interpolates positions between received frames at 60 Hz so
  motion looks smooth even when worker only delivers at ~30 Hz.
- Source: <https://github.com/markuslerner/d3-webworker-pixijs>.
- This is the architecture we want for r1-server-ui-v2.

### 4. Transferable ArrayBuffer is fast enough; SharedArrayBuffer requires COOP/COEP

- ArrayBuffer is a Transferable: `worker.postMessage(buf, [buf])` is
  zero-copy and **independent of buffer size** — main loses access,
  worker gains it (or vice-versa).
  Source: <https://developer.mozilla.org/en-US/docs/Web/API/Web_Workers_API/Transferable_objects>.
- For a Float32Array of `nodes * 3` floats, even at 100k nodes (1.2 MB)
  the postMessage overhead is dominated by the structured-clone of the
  message envelope, not the buffer payload.
  Source: <https://surma.dev/things/is-postmessage-slow/> (postMessage
  scales O(n) only for cloned content; transfers are O(1)).
- SharedArrayBuffer offers true shared memory with `Atomics`, but:
  - Requires `Cross-Origin-Opener-Policy: same-origin` AND
    `Cross-Origin-Embedder-Policy: require-corp` (or `credentialless`).
    Source: <https://web.dev/articles/coop-coep>.
  - Globally available at ~95% browser coverage (Chrome 68+, Firefox 79+,
    Safari 15.2+, Edge 79+) **only when the page is cross-origin
    isolated**. Source: <https://caniuse.com/sharedarraybuffer>.
  - Empirically: SAB only beats transferable ArrayBuffer at 1M+ float
    workloads; at our sizes (3k–10k nodes = 9k–30k floats = 36–120 KB)
    the perf delta is unmeasurable.
    Source: Alibaba memory-optimization blog and surma.dev (linked
    in Sources).
- Transferable ping-pong has one wrinkle: you cannot keep using the
  buffer on main while the worker has it. The standard fix is **double
  buffering**: allocate two buffers and ping-pong them. While main
  renders from buffer A, worker writes buffer B, then they swap.

### 5. Per-frame protocol, rate, and backpressure

- The worker runs the sim at its own pace. Targeting **30 Hz worker tick
  + 60 Hz interpolated render on main** is the well-known sweet spot
  (markuslerner/d3-webworker-pixijs uses this; Observable's interactive
  worker notebook does too).
- Backpressure rule: **main pulls, worker pushes; main never queues more
  than one outstanding frame.**
  - Concretely: worker only posts when it owns the "outgoing" buffer.
    Main posts the buffer back only after rendering the frame. If main
    is slow, the worker simply holds its latest result and overwrites
    it on the next tick — frames are dropped silently, never queued.
  - This avoids the classic bug where a slow main thread accumulates a
    growing backlog of postMessage calls and OOMs.
- Tick cadence inside the worker is driven by `setTimeout(tick, 1000/30)`
  rather than `setInterval` (avoids drift) and definitely **not**
  `requestAnimationFrame` (workers don't have rAF, and tying sim cadence
  to render cadence defeats the whole offload).
- Static convergence: when `simulation.alpha() < simulation.alphaMin()`
  the worker stops self-scheduling and waits for an explicit `kick`
  (e.g., user dragged a node, new node added) before resuming.
  Source: <https://d3js.org/d3-force/simulation>.

### 6. Cancellation: `close()` from inside, `terminate()` from outside

- `Worker.terminate()` is synchronous and immediate; the worker is
  stopped mid-instruction with no chance to clean up.
  Source: <https://developer.mozilla.org/en-US/docs/Web/API/Worker/terminate>.
- Best practice (per MDN + community blogs):
  1. Send `{type:'shutdown'}` to the worker.
  2. Worker calls `self.close()` after releasing references and
     transferring its outgoing buffer back.
  3. Main `await`s a final `{type:'goodbye'}` reply with a 100 ms timeout
     (then falls back to `terminate()`).
- Tab close: browsers automatically kill workers when the owning document
  is destroyed. **But for an SPA where the user just navigates away
  from the graph view, you MUST manually terminate.** Letting workers
  outlive their owner is a documented source of memory leaks (e.g.,
  OpenLayers issue #10856).
  Source: <https://github.com/openlayers/openlayers/issues/10856>.
- For r1-server's UI, `graph.js` should:
  - Keep `worker` and `bufA`, `bufB` in a closure.
  - Expose a `destroy()` function that `worker.postMessage({type:'shutdown'})`,
    then `setTimeout(() => worker.terminate(), 100)`, then nulls all
    references and calls `graph._destructor()` (3d-force-graph's own
    cleanup hook for three.js GPU resources).
  - Wire `destroy()` to (a) `beforeunload`, (b) the route's unmount
    hook in whatever HTMX/SPA shell we end up with, (c) an explicit
    "Close graph" button.

### 7. Vendored worker scripts behind `embed.FS` work natively

- `new Worker(url)` requires the worker script be **same-origin** with
  the document. Since `embed.FS` serves everything from the same Go
  HTTP server, this is automatic.
  Source: <https://html.spec.whatwg.org/multipage/workers.html>.
- Inside a classic worker, `importScripts('/ui/vendor/d3-force-3d.min.js')`
  loads further same-origin scripts synchronously. No CORS, no module
  graph, no bundler.
  Source: <https://developer.mozilla.org/en-US/docs/Web/API/WorkerGlobalScope/importScripts>.
- We do **not** need module workers (`{type:'module'}`). Module workers
  bring `import` syntax but cannot use `importScripts()` — they require
  a real ES module graph, which is a step backwards for our no-build
  setup.
- We need to vendor `d3-force-3d.min.js` into
  `cmd/r1-server/ui/vendor/d3-force-3d.min.js`. The current vendor dir
  has `3d-force-graph.min.js`, `three.min.js`, `three-spritetext.min.js`,
  `htmx.min.js` — d3-force-3d is **missing** and must be added.

### 8. Empirical performance at 1k / 3k nodes

Reported numbers from the cited sources (no first-party benchmarks):

- jin5354/d3-force-graph (d3-force in worker + WebGL on main): "supports
  large datasets of approximately 100k nodes and links."
- markuslerner/d3-webworker-pixijs: smooth interaction at 5k+ nodes on
  mid-range laptops.
- PMC graph-vis efficiency study: 30 fps for "3k nodes, 4k edges" is the
  established baseline that a competent library is expected to clear.
  Source: <https://pmc.ncbi.nlm.nih.gov/articles/PMC12061801/>.
- Without a worker, 3d-force-graph drops below 30 fps somewhere between
  800 and 1500 nodes on commodity hardware (matches the reporter's
  observation of jank "at 1k+ nodes").

**Expected after this change:**

| Nodes | Worker tick rate | Main render | Notes                                  |
|-------|------------------|-------------|----------------------------------------|
| 500   | 60 Hz            | 60 fps      | Worker is bored; rendering is the cap. |
| 1k    | 60 Hz            | 60 fps      | Comfortable.                            |
| 3k    | 30 Hz            | 60 fps      | Sim is the bottleneck; rendering OK.   |
| 10k   | 10–15 Hz         | 30–60 fps   | Sim becomes choppy; main stays smooth. |

The win is decoupling: rendering FPS is no longer dragged down by sim
cost. Sim cadence degrades gracefully; UI stays responsive.

---

## Recommendation for r1-server-ui-v2

### Architecture

```
                main thread                          worker thread
  ┌─────────────────────────────────┐           ┌────────────────────────┐
  │ 3d-force-graph (renderer only)  │           │ d3-force-3d simulation │
  │   - cooldownTicks(0)            │           │   - simulation.stop()  │
  │   - d3Force(*, null)            │           │   - manual tick()      │
  │   - nodePositionUpdate(fn)      │           │   - 30 Hz setTimeout   │
  │     reads from posBufA          │           │   - writes to posBufB  │
  │ rAF loop: render(60 Hz)         │   ◀──┐    │                        │
  └────────────┬────────────────────┘      │    └────────┬───────────────┘
               │                           │             │
               │ postMessage(buf, [buf])   │ buf         │ postMessage(buf, [buf])
               └───────────────────────────┴─────────────┘
                       (transferable ArrayBuffer, ping-pong)
```

### Transfer mechanism: **transferable ArrayBuffer with double buffering**

- Two `Float32Array` buffers of length `nodes * 3` (x,y,z), allocated
  once per topology change.
- Main owns one, worker owns the other; ownership swaps every worker
  frame.
- **No SharedArrayBuffer.** At our node counts the perf gain is
  unmeasurable, and SAB drags in COOP/COEP headers that complicate Go
  `embed.FS` serving and break embedded iframes / dev tools.

### Message protocol

All messages: `{type, payload?, transferList?}`. Types:

| Direction       | Type             | Payload                                       | Notes                              |
|-----------------|------------------|-----------------------------------------------|------------------------------------|
| main → worker   | `init`           | `{nodes, links, forces, posBuf, dims}`        | Once per topology load.            |
| main → worker   | `topology`       | `{nodes, links, posBuf}`                      | When graph mutates.                |
| main → worker   | `kick`           | `{alpha}` (default 0.3)                       | Re-heat sim (drag, neighborhood).  |
| main → worker   | `pinNode`        | `{id, x, y, z}` or `{id, fx:null}`            | User dragging.                     |
| main → worker   | `frameAck`       | `{posBuf}` (transfer)                         | Main returns the buffer it just rendered. |
| main → worker   | `shutdown`       | none                                          | Graceful close.                    |
| worker → main   | `frame`          | `{posBuf, alpha, tickCount}` (transfer)       | New positions. Sent only when worker holds outgoing buffer. |
| worker → main   | `converged`      | `{tickCount}`                                 | `alpha < alphaMin`. Worker idles.  |
| worker → main   | `goodbye`        | none                                          | After `shutdown`, before `close()`.|
| worker → main   | `error`          | `{message, stack}`                            | For debugging.                     |

### Tick cadence and backpressure

- Worker schedules itself with `setTimeout(loop, 1000/30)` (30 Hz target).
- Worker holds an outgoing buffer it owns. Each tick, runs
  `sim.tick(1)`, copies node positions into the buffer.
- When it has finished a tick AND owns an outgoing buffer, it posts
  `frame` and gives up ownership.
- Main, after rendering, posts `frameAck` returning a buffer.
- If main hasn't acked yet when the next worker tick fires, the worker
  **silently overwrites** its own buffer (frame drop). It does **not**
  post — main will receive the next frame instead. This is the
  backpressure mechanism: at most one frame in flight.

### Cancel sequence

```js
function destroy() {
  let resolved = false;
  const onGoodbye = (e) => {
    if (e.data && e.data.type === 'goodbye') {
      resolved = true;
      worker.removeEventListener('message', onGoodbye);
      worker.terminate();
      cleanup();
    }
  };
  worker.addEventListener('message', onGoodbye);
  worker.postMessage({type: 'shutdown'});
  setTimeout(() => {
    if (!resolved) {
      worker.terminate();      // hard kill on timeout
      cleanup();
    }
  }, 100);
}

function cleanup() {
  graph._destructor && graph._destructor();   // three.js GPU teardown
  posBufA = posBufB = worker = graph = null;
  window.removeEventListener('beforeunload', destroy);
}
```

Wire `destroy()` to:
- `beforeunload` (page navigation away).
- The route's unmount hook (whatever the SPA shell uses; HTMX
  `htmx:beforeSwap` if we go that route).
- An explicit "Close" button on the graph view.

### Vendoring

Add `cmd/r1-server/ui/vendor/d3-force-3d.min.js` (UMD build, ~30 KB
gzipped) and update `embed.FS` directive if needed. Worker imports via:

```js
// graph-worker.js (top of file)
self.importScripts('/ui/vendor/d3-force-3d.min.js');
const { forceSimulation, forceManyBody, forceLink, forceCenter } = self.d3;
```

Same-origin → no CORS. No bundler. No module worker.

### Order of work for the implementer

1. Add `vendor/d3-force-3d.min.js` to embed.FS.
2. Flesh out `graph-worker.js` skeleton (currently 232 lines) per the
   protocol above.
3. In `graph.js`: disable internal forces, set `cooldownTicks(0)`,
   install `nodePositionUpdate` reading from the latest received buffer,
   wire init/topology/kick/pinNode/destroy.
4. Add a "node count" stress test fixture (1k, 3k, 10k synthetic graphs)
   for FPS regression checks.

---

## SharedArrayBuffer in 2026

State as of May 2026:

- **Required headers**: `Cross-Origin-Opener-Policy: same-origin` AND
  `Cross-Origin-Embedder-Policy: require-corp` (or `credentialless`).
- **Browser coverage**: ~95% globally (Chrome 68+, Firefox 79+,
  Safari 15.2+, Edge 79+, Opera 64+, plus matching mobile versions).
  Source: caniuse. Coverage **only counts when COOP+COEP are set** —
  without the headers, `SharedArrayBuffer` is `undefined` in the
  browser context.
- **`credentialless` mode**: Chromium 96+ allows COEP to load
  cross-origin subresources without CORP headers by stripping
  credentials. Firefox shipped support in 2024. Safari support is still
  flag-gated as of 2026. Use only if you actually need to embed
  cross-origin resources.
- **Localhost dev**: HTTPS not required, but the headers must be sent
  by the dev server. Go's `net/http` can do this with one middleware
  layer.

**Decision for r1-server-ui-v2: do NOT enable cross-origin isolation.**

Reasons:
1. Performance gain at our scale is unmeasurable (see §4 above).
2. COOP `same-origin` breaks `window.opener` from external pages — this
   matters if the operator ever pops the graph viewer out of a
   third-party shell (Slack message preview, Notion embed, etc.).
3. COEP `require-corp` requires every embedded resource (images, fonts,
   third-party widgets) to either be same-origin or send `CORP` headers.
   That is a long-term maintenance tax.
4. Future option remains open: if we later need SAB for, say, WebAssembly
   physics, the change is a Go middleware addition — no rip-out.

**Fallback strategy** (if SAB is later wanted):
- Feature-detect: `if (typeof SharedArrayBuffer !== 'undefined' && self.crossOriginIsolated) { useSAB } else { useTransferable }`.
- Both code paths share the same protocol skeleton; only the buffer
  allocation and the postMessage call differ.

---

## Implementation gotchas

1. **`importScripts` is classic-worker only.** If anyone "modernizes"
   `graph-worker.js` to a module worker (`new Worker(url, {type:'module'})`),
   `self.importScripts` throws. We must stay classic.
2. **d3-force-3d default tick events don't fire on manual `tick()`.**
   The worker MUST post after each manual tick — there is no internal
   notification.
3. **`nodePositionUpdate` returning truthy is critical.** If the function
   returns `false`/`undefined`, 3d-force-graph falls back to its default
   behavior of reading `node.x|y|z`, which we never set. Result: all
   nodes pile at origin.
4. **`d3Force('charge', null)` etc. must be called AFTER graphData is
   loaded.** 3d-force-graph re-creates the simulation when graphData
   changes; nulling forces before data has no effect.
5. **`cooldownTicks(0)` plus `cooldownTime(0)`**: belt and braces. Either
   alone may not fully halt the internal d3-force-3d sim (the library
   has changed defaults across versions).
6. **Buffer ownership tracking.** A `transferred` flag on each buffer
   prevents double-transfer (which throws `DataCloneError`). Main's
   render loop must not touch the outgoing buffer between
   `postMessage(buf, [buf])` and the next `frame` reply.
7. **Pinning during drag.** When user drags a node, set
   `node.fx = node.x; node.fy = node.y; node.fz = node.z;` in the worker
   (via `pinNode` message). On release, send `pinNode` with `fx:null`.
   d3-force-3d uses fx/fy/fz to override sim output for fixed nodes.
   Source: d3-force-3d README.
8. **Topology changes mid-flight.** When the graph mutates (new node
   appears in the ledger), main posts `topology` AFTER receiving and
   acknowledging the latest `frame`. This avoids racing the worker with
   stale buffers.
9. **Worker can't access `window`, `document`, `localStorage`.** Anyone
   instinctively adding `console.log(window.foo)` in graph-worker.js
   will get a ReferenceError. `console.log` itself does work in
   workers; output goes to DevTools.
10. **`embed.FS` MIME types.** Make sure the Go file server returns
    `Content-Type: application/javascript` for `.js` under
    `/ui/vendor/`. Some browsers reject classic-worker scripts served
    as `text/plain`. (The existing vendor/ files presumably already
    work; this is a regression risk if we change file-server logic.)
11. **Don't transfer buffers that are views of a larger ArrayBuffer.**
    Always allocate a dedicated `new Float32Array(n*3)` and transfer its
    `.buffer`. Sharing a slice means the entire underlying buffer goes
    with it.
12. **Worker memory leaks on SPA navigate.** Re-entering the graph view
    after navigating away will spawn a second worker if `destroy()`
    didn't run on unmount. Add a defensive check: tear down any
    pre-existing worker before creating a new one.
13. **Convergence detection.** When `alpha < alphaMin`, the worker
    stops self-scheduling. Main must remember this and re-send `kick`
    on user interaction (drag, hover-expand, etc.) instead of expecting
    fresh frames.

---

## Sources

All accessed 2026-05-05.

- 3d-force-graph (vasturiano), README and API:
  <https://github.com/vasturiano/3d-force-graph> — `forceEngine`,
  `nodePositionUpdate`, `pauseAnimation`, `cooldownTicks`, `d3Force`.
- d3-force-3d (vasturiano), README:
  <https://github.com/vasturiano/d3-force-3d> — UMD/ESM distribution,
  `tick()`, `nodes()`, fx/fy/fz pinning.
- D3 force documentation (Observable):
  <https://d3js.org/d3-force/simulation> — explicit Worker
  recommendation, `simulation.stop()` + manual tick semantics, alpha /
  alphaMin convergence.
- Bostock — Force-Directed Web Worker (gist):
  <https://gist.github.com/mbostock/01ab2e85e8727d6529d20391c0fd9a16> —
  the canonical static-batch pattern.
- Observable — Force-directed web worker:
  <https://observablehq.com/@d3/force-directed-web-worker>.
- markuslerner/d3-webworker-pixijs:
  <https://github.com/markuslerner/d3-webworker-pixijs> — live
  streaming pattern, lower-FPS sim + interpolated render.
- jin5354/d3-force-graph:
  <https://github.com/jin5354/d3-force-graph> — d3-force in worker +
  WebGL, ~100k node claim.
- MDN — Transferable objects:
  <https://developer.mozilla.org/en-US/docs/Web/API/Web_Workers_API/Transferable_objects>
- MDN — Worker.terminate():
  <https://developer.mozilla.org/en-US/docs/Web/API/Worker/terminate>
- MDN — DedicatedWorkerGlobalScope.close():
  <https://developer.mozilla.org/en-US/docs/Web/API/DedicatedWorkerGlobalScope/close>
- MDN — Using web workers:
  <https://developer.mozilla.org/en-US/docs/Web/API/Web_Workers_API/Using_web_workers>
- MDN — WorkerGlobalScope.importScripts:
  <https://developer.mozilla.org/en-US/docs/Web/API/WorkerGlobalScope/importScripts>
- MDN — Cross-Origin-Embedder-Policy:
  <https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Cross-Origin-Embedder-Policy>
- MDN — Cross-Origin-Opener-Policy:
  <https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Cross-Origin-Opener-Policy>
- MDN — SharedArrayBuffer:
  <https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects/SharedArrayBuffer>
- web.dev — Making your website "cross-origin isolated":
  <https://web.dev/articles/coop-coep>
- web.dev — Why you need cross-origin isolated:
  <https://web.dev/articles/why-coop-coep>
- caniuse — SharedArrayBuffer:
  <https://caniuse.com/sharedarraybuffer> — Chrome 68+, FF 79+,
  Safari 15.2+ (with cross-origin isolation), ~95% global coverage.
- Surma — Is postMessage slow:
  <https://surma.dev/things/is-postmessage-slow/> — transferables are
  O(1) regardless of size; structured clone is O(n).
- Andrea Giammarchi — About SharedArrayBuffer & Atomics:
  <https://webreflection.medium.com/about-sharedarraybuffer-atomics-87f97ddfc098>
- Alibaba Cloud — Frontend Memory Optimization:
  <https://www.alibabacloud.com/blog/597639> — SAB only wins at very
  large data sizes (1M+).
- WHATWG HTML — Web workers spec (same-origin requirement):
  <https://html.spec.whatwg.org/multipage/workers.html>
- OpenLayers issue #10856 — raster webworkers not terminated:
  <https://github.com/openlayers/openlayers/issues/10856>.
- DEV — Enhancing Graphics with OffscreenCanvas + D3:
  <https://dev.to/jeevankishore/enhancing-graphics-performance-with-offscreencanvas-and-d3js-19ka>
- Medium — Best Libraries for Large Force-Directed Graphs (Stephen
  Weber):
  <https://weber-stephen.medium.com/the-best-libraries-and-methods-to-render-large-network-graphs-on-the-web-d122ece2f4dc>
- PMC — Graph visualization efficiency study:
  <https://pmc.ncbi.nlm.nih.gov/articles/PMC12061801/> — 30 fps for
  3k/4k baseline.
- DeepWiki — react-force-graph custom 3D rendering:
  <https://deepwiki.com/vasturiano/react-force-graph/5.4-custom-3d-rendering>
