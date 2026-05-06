// cmd/r1-server/ui/js/graph-worker.js
//
// Spec 2 §3.2: an ES-module Web Worker that runs d3-force-3d off the main
// thread and posts position updates back via transferable ArrayBuffer (or a
// pre-allocated SharedArrayBuffer when crossOriginIsolated is true).
//
// Loaded by graph.js as:
//   new Worker('/ui/js/graph-worker.js', { type: 'module' })
//
// The 9-message protocol (synthesized cluster, RT-D3-FORCE-WEBWORKER):
//
//   main → worker
//   ────────────
//     { kind: 'init',      nodes, edges, useSAB? }
//     { kind: 'tick' }
//     { kind: 'add',       node, neighbors }
//     { kind: 'remove',    nodeId }
//     { kind: 'set-alpha', alpha }
//     { kind: 'freeze' }
//     { kind: 'shutdown' }
//
//   worker → main
//   ─────────────
//     { kind: 'positions', positions: Float32Array, alpha, count }   // transferred
//     { kind: 'error',     message, where }
//
// When a SharedArrayBuffer is allocated (`useSAB === true`), `positions` is
// the SAB view returned in the first response only; subsequent ticks just
// notify with `{ kind: 'positions', alpha, count }` and the main thread
// reads the same SAB. Without SAB, every tick posts a fresh Float32Array
// whose underlying ArrayBuffer is transferred (zero-copy). The main thread
// returns the buffer via `{ kind: 'tick', buffer }` when it's done so the
// worker can recycle the allocation; first tick uses a freshly-allocated
// buffer.

// d3-force-3d is loaded lazily so test environments that mock the
// simulation can drop in their own factory without dragging in the
// UMD bundle. The default factory uses the import map.
let simulationFactory = null;
async function getSimulationFactory() {
  if (simulationFactory) return simulationFactory;
  // Path used at runtime in the browser.
  if (typeof window !== 'undefined' || typeof importScripts !== 'undefined') {
    const m = await import('d3-force-3d');
    simulationFactory = (nodes, edges) => m.forceSimulation(nodes, 3)
      .force('charge', m.forceManyBody().strength(-30))
      .force('link', m.forceLink(edges).id(d => d.id).distance(40))
      .force('center', m.forceCenter(0, 0, 0))
      .alpha(1).alphaDecay(0.04).stop();
    return simulationFactory;
  }
  throw new Error('no simulationFactory configured for this environment');
}

// setSimulationFactory lets tests inject a deterministic stub
// without importing d3-force-3d (which is the UMD build and not
// vitest-friendly). The factory's signature is (nodes, edges) →
// { tick(), nodes(arr), alpha(v?), alphaDecay(v), stop(), restart() }.
export function setSimulationFactory(fn) {
  simulationFactory = fn;
}

export const POS_FLOATS_PER_NODE = 3; // x, y, z exported for tests.

const state = {
  sim: null,
  nodes: [],
  edges: [],
  useSAB: false,
  sabPositions: null, // Float32Array view over a SharedArrayBuffer
  recyclable: null,   // ArrayBuffer returned by main on previous tick
  frozen: false,
};

export function _resetState() {
  state.sim = null;
  state.nodes = [];
  state.edges = [];
  state.useSAB = false;
  state.sabPositions = null;
  state.recyclable = null;
  state.frozen = false;
}

export function _getState() { return state; }

function postError(where, err) {
  self.postMessage({
    kind: 'error',
    where,
    message: (err && err.message) || String(err),
  });
}

async function ensureSimulation() {
  if (state.sim) return state.sim;
  const factory = await getSimulationFactory();
  state.sim = factory(state.nodes, state.edges);
  return state.sim;
}

export function _ensureSimulationSync() {
  if (state.sim) return state.sim;
  if (!simulationFactory) throw new Error('simulationFactory not set');
  state.sim = simulationFactory(state.nodes, state.edges);
  return state.sim;
}

export function writePositions(out, nodes = state.nodes) {
  for (let i = 0; i < nodes.length; i++) {
    const n = nodes[i];
    const o = i * POS_FLOATS_PER_NODE;
    out[o] = n.x || 0;
    out[o + 1] = n.y || 0;
    out[o + 2] = n.z || 0;
  }
}

async function tickOnce() {
  if (state.frozen) return;
  const sim = await ensureSimulation();
  sim.tick();
}


export async function handleMessage(msg, self_) {
  const sf = self_ || self;
  switch (msg.kind) {
    case 'init': {
      state.nodes = (msg.nodes || []).map(n => ({ ...n }));
      state.edges = (msg.edges || []).map(e => ({ ...e }));
      state.useSAB = !!msg.useSAB && typeof SharedArrayBuffer !== 'undefined';
      state.frozen = false;
      if (state.useSAB) {
        const sab = new SharedArrayBuffer(state.nodes.length * POS_FLOATS_PER_NODE * 4);
        state.sabPositions = new Float32Array(sab);
      }
      await ensureSimulation();
      _emit(sf);
      return;
    }
    case 'tick': {
      if (msg.buffer instanceof ArrayBuffer) state.recyclable = msg.buffer;
      await tickOnce();
      _emit(sf);
      return;
    }
    case 'add': {
      const node = msg.node;
      if (!node || !node.id) return;
      const neigh = (msg.neighbors || []).filter(Boolean);
      let cx = 0, cy = 0, cz = 0;
      for (const n of neigh) { cx += n.x || 0; cy += n.y || 0; cz += n.z || 0; }
      if (neigh.length) { cx /= neigh.length; cy /= neigh.length; cz /= neigh.length; }
      state.nodes.push({ ...node, x: cx, y: cy, z: cz });
      const sim = await ensureSimulation();
      sim.nodes(state.nodes).alpha(0.3).restart().stop();
      if (state.useSAB) {
        const sab = new SharedArrayBuffer(state.nodes.length * POS_FLOATS_PER_NODE * 4);
        state.sabPositions = new Float32Array(sab);
      }
      _emit(sf);
      return;
    }
    case 'remove': {
      const idx = state.nodes.findIndex(n => n.id === msg.nodeId);
      if (idx < 0) return;
      state.nodes.splice(idx, 1);
      const sim = await ensureSimulation();
      sim.nodes(state.nodes).alpha(0.1).restart().stop();
      _emit(sf);
      return;
    }
    case 'set-alpha': {
      if (state.sim) state.sim.alpha(Math.max(0, Math.min(1, msg.alpha || 0)));
      return;
    }
    case 'freeze': {
      state.frozen = true;
      if (state.sim) state.sim.alpha(0);
      return;
    }
    case 'shutdown': {
      if (state.sim) state.sim.stop();
      if (sf && typeof sf.close === 'function') sf.close();
      return;
    }
    default:
      throw new Error('unknown message kind: ' + msg.kind);
  }
}

function _emit(sf) {
  const count = state.nodes.length;
  const alpha = state.sim ? state.sim.alpha() : 0;
  if (state.useSAB && state.sabPositions) {
    writePositions(state.sabPositions);
    sf.postMessage({ kind: 'positions', alpha, count });
    return;
  }
  let buf = state.recyclable;
  state.recyclable = null;
  const wantBytes = count * POS_FLOATS_PER_NODE * 4;
  if (!buf || buf.byteLength < wantBytes) {
    buf = new ArrayBuffer(wantBytes);
  }
  const view = new Float32Array(buf, 0, count * POS_FLOATS_PER_NODE);
  writePositions(view);
  sf.postMessage({ kind: 'positions', positions: view, alpha, count }, [buf]);
}

if (typeof self !== 'undefined' && typeof self.onmessage !== 'undefined') {
  self.onmessage = (ev) => {
    handleMessage(ev.data || {}, self).catch(err => postError('onmessage:' + (ev.data && ev.data.kind), err));
  };
}
