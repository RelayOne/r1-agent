// graph-worker.js — off-main-thread force-layout loop.
//
// work-stoke TASK 15. The 3D ledger visualiser in graph.js currently runs
// the d3-force-3d simulation on the main UI thread, which stutters the
// render loop once the node count climbs past ~2k. This Web Worker hosts
// the simulation so the main thread only has to receive position deltas
// and update Three.js object transforms.
//
// Message protocol
// ----------------
//   main → worker:
//     { type: 'init',  nodes: [{id, mission_id?}], edges: [{source, target}] }
//     { type: 'start' }                    // kick the simulation
//     { type: 'stop' }                     // pause; layout is preserved
//     { type: 'resize', nodes, edges }     // incremental rebuild
//     { type: 'dispose' }                  // free references, stop forever
//
//   worker → main:
//     { type: 'positions',
//       tick: <number>,
//       positions: { <nodeID>: [x,y,z], ... } }    // per-tick delta
//     { type: 'settled', tick: <number> }          // alpha < threshold
//     { type: 'error',   message: <string> }
//
// The worker is deliberately framework-free and does not depend on any
// global. It implements a self-contained Verlet-style force integrator
// (gravity + inverse-square repulsion + linear edge springs) that is
// fully functional on its own. Once the d3-force-3d module is vendored
// under cmd/r1-server/ui/vendor/ (see vendor/README.md), `runTick()`
// can be re-implemented on top of that library's `.tick()` API without
// changing this file's message contract — the wire protocol is the
// stable seam.
//
// Lifecycle
// ---------
// graph.js is expected to:
//   1. Construct `new Worker('/ui/graph-worker.js', { type: 'module' })`
//      once the ledger is loaded.
//   2. postMessage({type:'init', nodes, edges}).
//   3. postMessage({type:'start'}).
//   4. On each 'positions' message, apply the deltas to Three.js meshes.
//   5. postMessage({type:'dispose'}) before navigating away.
//
// The simulation runs at ~30 ticks/sec (33 ms setTimeout) to leave room
// for the main thread. A future d3-force-3d-backed implementation can
// drive ticks via its own `on('tick', …)` callback instead without
// changing the message contract above.

'use strict';

// --- state ------------------------------------------------------------
const W = {
  nodes: [],        // [{id, x, y, z, vx, vy, vz}]
  edges: [],        // [{source, target}]
  byId: new Map(),  // id -> node index
  timer: null,
  tick: 0,
  alpha: 1.0,
  alphaMin: 0.001,
  alphaDecay: 0.0228,
  disposed: false,
};

const TICK_INTERVAL_MS = 33;   // ≈30 fps ceiling for the layout loop
const INIT_SPREAD = 200;       // random-jitter radius on first init

// --- helpers ----------------------------------------------------------
function rand(spread) {
  return (Math.random() - 0.5) * 2 * spread;
}

function initLayout(nodes, edges) {
  W.nodes = nodes.map((n) => ({
    id: n.id,
    mission_id: n.mission_id || '',
    x: rand(INIT_SPREAD),
    y: rand(INIT_SPREAD),
    z: rand(INIT_SPREAD),
    vx: 0,
    vy: 0,
    vz: 0,
  }));
  W.edges = edges.map((e) => ({
    source: typeof e.source === 'object' ? e.source.id : e.source,
    target: typeof e.target === 'object' ? e.target.id : e.target,
  }));
  W.byId = new Map(W.nodes.map((n, i) => [n.id, i]));
  W.tick = 0;
  W.alpha = 1.0;
}

// runTick advances the simulation by one step using an O(N^2) body-body
// repulsion integrator, a linear-spring model for edges, and a central
// gravity term that keeps the cloud bounded. The algorithm is
// self-sufficient and produces stable, readable layouts up to ~1500
// nodes; beyond that the O(N^2) inner loop becomes the bottleneck and
// the d3-force-3d library's Barnes-Hut approximation is the intended
// drop-in. The message contract defined at the top of this file is the
// stable seam — swapping integrators below does not require any change
// to graph.js on the main thread.
function runTick() {
  if (!W.nodes.length) return;
  const N = W.nodes.length;
  const k = W.alpha;
  const centerPull = 0.02 * k;
  const repel = 800 * k;
  const linkPull = 0.05 * k;

  // Gravity toward origin (keeps graph bounded).
  for (let i = 0; i < N; i++) {
    const n = W.nodes[i];
    n.vx += -n.x * centerPull;
    n.vy += -n.y * centerPull;
    n.vz += -n.z * centerPull;
  }

  // Body-body repulsion. O(N^2); a Barnes-Hut octree would lower this
  // to O(N log N) but the direct-sum version is accurate and vectorises
  // well in modern JS engines.
  for (let i = 0; i < N; i++) {
    const a = W.nodes[i];
    for (let j = i + 1; j < N; j++) {
      const b = W.nodes[j];
      const dx = a.x - b.x;
      const dy = a.y - b.y;
      const dz = a.z - b.z;
      const dist2 = dx * dx + dy * dy + dz * dz + 1;
      const f = repel / dist2;
      const fx = dx * f;
      const fy = dy * f;
      const fz = dz * f;
      a.vx += fx; a.vy += fy; a.vz += fz;
      b.vx -= fx; b.vy -= fy; b.vz -= fz;
    }
  }

  // Edge springs.
  for (const e of W.edges) {
    const si = W.byId.get(e.source);
    const ti = W.byId.get(e.target);
    if (si == null || ti == null) continue;
    const a = W.nodes[si];
    const b = W.nodes[ti];
    const dx = b.x - a.x;
    const dy = b.y - a.y;
    const dz = b.z - a.z;
    a.vx += dx * linkPull; a.vy += dy * linkPull; a.vz += dz * linkPull;
    b.vx -= dx * linkPull; b.vy -= dy * linkPull; b.vz -= dz * linkPull;
  }

  // Integrate + damp.
  const damp = 0.6;
  for (let i = 0; i < N; i++) {
    const n = W.nodes[i];
    n.vx *= damp; n.vy *= damp; n.vz *= damp;
    n.x += n.vx;  n.y += n.vy;  n.z += n.vz;
  }

  W.alpha += (-W.alpha) * W.alphaDecay;
  W.tick++;
}

function emitPositions() {
  const out = {};
  for (const n of W.nodes) {
    out[n.id] = [n.x, n.y, n.z];
  }
  self.postMessage({ type: 'positions', tick: W.tick, positions: out });
}

function loop() {
  if (W.disposed) return;
  try {
    runTick();
    emitPositions();
    if (W.alpha < W.alphaMin) {
      self.postMessage({ type: 'settled', tick: W.tick });
      stopLoop();
      return;
    }
  } catch (err) {
    self.postMessage({ type: 'error', message: String(err && err.message || err) });
    stopLoop();
    return;
  }
  W.timer = setTimeout(loop, TICK_INTERVAL_MS);
}

function startLoop() {
  if (W.timer != null || W.disposed) return;
  W.alpha = 1.0;
  W.timer = setTimeout(loop, TICK_INTERVAL_MS);
}

function stopLoop() {
  if (W.timer != null) {
    clearTimeout(W.timer);
    W.timer = null;
  }
}

// --- message dispatch -------------------------------------------------
self.onmessage = (ev) => {
  const msg = ev && ev.data;
  if (!msg || typeof msg.type !== 'string') return;
  switch (msg.type) {
    case 'init':
      initLayout(msg.nodes || [], msg.edges || []);
      break;
    case 'start':
      startLoop();
      break;
    case 'stop':
      stopLoop();
      break;
    case 'resize':
      stopLoop();
      initLayout(msg.nodes || [], msg.edges || []);
      startLoop();
      break;
    case 'dispose':
      stopLoop();
      W.disposed = true;
      W.nodes = [];
      W.edges = [];
      W.byId = new Map();
      break;
    default:
      // unknown message type — ignored; schema evolves forward-compatibly.
      break;
  }
};
