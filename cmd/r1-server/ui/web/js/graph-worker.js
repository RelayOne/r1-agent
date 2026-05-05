// cmd/r1-server/ui/web/js/graph-worker.js
//
// Spec 2 §3.2: an ES-module Web Worker that runs d3-force-3d off the main
// thread and posts position updates back via transferable ArrayBuffer (or a
// pre-allocated SharedArrayBuffer when crossOriginIsolated is true).
//
// Loaded by graph.js as:
//   new Worker('/ui/web/js/graph-worker.js', { type: 'module' })
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

import {
  forceSimulation,
  forceLink,
  forceManyBody,
  forceCenter,
} from 'd3-force-3d';

const POS_FLOATS_PER_NODE = 3; // x, y, z

const state = {
  sim: null,
  nodes: [],
  edges: [],
  useSAB: false,
  sabPositions: null, // Float32Array view over a SharedArrayBuffer
  recyclable: null,   // ArrayBuffer returned by main on previous tick
  frozen: false,
};

function postError(where, err) {
  self.postMessage({
    kind: 'error',
    where,
    message: (err && err.message) || String(err),
  });
}

function ensureSimulation() {
  if (state.sim) return state.sim;
  state.sim = forceSimulation(state.nodes, 3)
    .force('charge', forceManyBody().strength(-30))
    .force('link', forceLink(state.edges).id(d => d.id).distance(40))
    .force('center', forceCenter(0, 0, 0))
    .alpha(1)
    .alphaDecay(0.04)
    .stop();
  return state.sim;
}

function writePositions(out) {
  for (let i = 0; i < state.nodes.length; i++) {
    const n = state.nodes[i];
    const o = i * POS_FLOATS_PER_NODE;
    out[o] = n.x || 0;
    out[o + 1] = n.y || 0;
    out[o + 2] = n.z || 0;
  }
}

function tickOnce() {
  if (state.frozen) return;
  ensureSimulation().tick();
}

function emitPositions() {
  const count = state.nodes.length;
  const alpha = state.sim ? state.sim.alpha() : 0;

  if (state.useSAB && state.sabPositions) {
    writePositions(state.sabPositions);
    self.postMessage({ kind: 'positions', alpha, count });
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
  self.postMessage({ kind: 'positions', positions: view, alpha, count }, [buf]);
}

self.onmessage = (ev) => {
  const msg = ev.data || {};
  try {
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
        ensureSimulation();
        emitPositions();
        return;
      }
      case 'tick': {
        if (msg.buffer instanceof ArrayBuffer) state.recyclable = msg.buffer;
        tickOnce();
        emitPositions();
        return;
      }
      case 'add': {
        const node = msg.node;
        if (!node || !node.id) return;
        // Spec §3.2: insert at mean of neighbours, restart at α=0.3.
        const neigh = (msg.neighbors || []).filter(Boolean);
        let cx = 0, cy = 0, cz = 0;
        for (const n of neigh) { cx += n.x || 0; cy += n.y || 0; cz += n.z || 0; }
        if (neigh.length) { cx /= neigh.length; cy /= neigh.length; cz /= neigh.length; }
        state.nodes.push({ ...node, x: cx, y: cy, z: cz });
        state.sim.nodes(state.nodes).alpha(0.3).restart().stop();
        if (state.useSAB) {
          const sab = new SharedArrayBuffer(state.nodes.length * POS_FLOATS_PER_NODE * 4);
          state.sabPositions = new Float32Array(sab);
        }
        emitPositions();
        return;
      }
      case 'remove': {
        const idx = state.nodes.findIndex(n => n.id === msg.nodeId);
        if (idx < 0) return;
        state.nodes.splice(idx, 1);
        state.sim.nodes(state.nodes).alpha(0.1).restart().stop();
        emitPositions();
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
        self.close();
        return;
      }
      default:
        postError('onmessage', new Error('unknown message kind: ' + msg.kind));
    }
  } catch (err) {
    postError('onmessage:' + msg.kind, err);
  }
};
