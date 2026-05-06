// cmd/r1-server/ui/web/js/graph-worker.test.js
//
// Spec 2 §6 + checklist T10. Drives graph-worker.js's pure logic
// against a 50-node fixture without spawning a real Worker. The
// test injects a deterministic simulation factory via
// setSimulationFactory so the assertion shape is stable across
// re-runs and we don't drag in d3-force-3d's UMD bundle.
//
// The covered surface:
//   - init message hydrates state + emits positions on the right
//     transport (transferable ArrayBuffer when useSAB === false)
//   - tick advances the simulation and re-emits positions
//   - simulation alpha reaches < 0.02 within 200 ticks
//   - add inserts at mean of neighbours and restarts at α=0.3
//   - shutdown calls self.close() and stops the simulation
//
// Run via web/ workspace's vitest. The web/vitest.config.ts include
// pattern is updated in this commit to discover .test.js files
// under cmd/r1-server/ui/web/js/.

import { describe, it, expect, beforeEach } from 'vitest';
import {
  handleMessage,
  setSimulationFactory,
  writePositions,
  POS_FLOATS_PER_NODE,
  _resetState,
  _getState,
} from './graph-worker.js';

// Deterministic stub simulation: each tick decays alpha by 0.04 and
// nudges every node toward the centroid of its links by 1%. Position
// changes are tiny but non-zero so we can assert the worker emits
// fresh data each tick.
function makeStubSim() {
  let nodes = [];
  let edges = [];
  let alphaVal = 1;
  const decay = 0.04;
  const sim = {
    nodes(arr) { if (arr) { nodes = arr; return sim; } return nodes; },
    alpha(v) { if (v !== undefined) { alphaVal = v; return sim; } return alphaVal; },
    alphaDecay(_) { return sim; },
    stop() { return sim; },
    restart() { return sim; },
    tick() {
      // Fold every node 1% toward (0,0,0); jitter slightly so positions move.
      for (const n of nodes) {
        n.x = (n.x || 0) * 0.99 + 0.01 * Math.sin(nodes.indexOf(n));
        n.y = (n.y || 0) * 0.99 + 0.01 * Math.cos(nodes.indexOf(n));
        n.z = (n.z || 0) * 0.99;
      }
      alphaVal = Math.max(0, alphaVal - decay);
      return sim;
    },
  };
  return sim;
}

function makeFixture(n) {
  const nodes = [];
  for (let i = 0; i < n; i++) nodes.push({ id: 'n' + i, x: i, y: 0, z: 0 });
  const edges = [];
  for (let i = 1; i < n; i++) edges.push({ source: 'n' + (i - 1), target: 'n' + i });
  return { nodes, edges };
}

function captureSelf() {
  const inbox = [];
  return {
    inbox,
    closeCalls: 0,
    postMessage(msg, transferList) { inbox.push({ msg, transferList }); },
    close() { this.closeCalls += 1; },
  };
}

beforeEach(() => {
  _resetState();
  setSimulationFactory((nodes, _edges) => {
    const s = makeStubSim();
    s.nodes(nodes);
    return s;
  });
});

describe('graph-worker init', () => {
  it('hydrates state from init message', async () => {
    const { nodes, edges } = makeFixture(50);
    const sf = captureSelf();
    await handleMessage({ kind: 'init', nodes, edges, useSAB: false }, sf);
    expect(_getState().nodes.length).toBe(50);
    expect(_getState().edges.length).toBe(49);
    expect(_getState().useSAB).toBe(false);
    expect(_getState().sim).not.toBe(null);
  });

  it('emits positions on the transferable path', async () => {
    const { nodes, edges } = makeFixture(50);
    const sf = captureSelf();
    await handleMessage({ kind: 'init', nodes, edges, useSAB: false }, sf);
    expect(sf.inbox.length).toBe(1);
    const out = sf.inbox[0];
    expect(out.msg.kind).toBe('positions');
    expect(out.msg.positions).toBeInstanceOf(Float32Array);
    expect(out.msg.count).toBe(50);
    expect(out.transferList).toHaveLength(1);
    expect(out.transferList[0]).toBeInstanceOf(ArrayBuffer);
  });
});

describe('graph-worker tick', () => {
  it('advances the simulation and re-emits positions', async () => {
    const { nodes, edges } = makeFixture(50);
    const sf = captureSelf();
    await handleMessage({ kind: 'init', nodes, edges, useSAB: false }, sf);
    sf.inbox.length = 0;
    await handleMessage({ kind: 'tick' }, sf);
    expect(sf.inbox.length).toBe(1);
    expect(sf.inbox[0].msg.kind).toBe('positions');
    expect(sf.inbox[0].msg.alpha).toBeLessThan(1);
  });

  it('cools below alpha 0.02 within 200 ticks', async () => {
    const { nodes, edges } = makeFixture(50);
    const sf = captureSelf();
    await handleMessage({ kind: 'init', nodes, edges, useSAB: false }, sf);
    let lastAlpha = 1;
    for (let i = 0; i < 200; i++) {
      sf.inbox.length = 0;
      await handleMessage({ kind: 'tick' }, sf);
      lastAlpha = sf.inbox[0].msg.alpha;
      if (lastAlpha < 0.02) break;
    }
    expect(lastAlpha).toBeLessThan(0.02);
  });
});

describe('graph-worker add', () => {
  it('inserts at mean of neighbours and re-emits positions', async () => {
    const sf = captureSelf();
    await handleMessage({
      kind: 'init',
      nodes: [
        { id: 'a', x: 0, y: 0, z: 0 },
        { id: 'b', x: 10, y: 0, z: 0 },
      ],
      edges: [],
      useSAB: false,
    }, sf);
    sf.inbox.length = 0;
    await handleMessage({
      kind: 'add',
      node: { id: 'c' },
      neighbors: [{ id: 'a', x: 0, y: 0, z: 0 }, { id: 'b', x: 10, y: 0, z: 0 }],
    }, sf);
    const inserted = _getState().nodes.find(n => n.id === 'c');
    expect(inserted).toBeDefined();
    expect(inserted.x).toBe(5);
    expect(inserted.y).toBe(0);
    expect(inserted.z).toBe(0);
    expect(sf.inbox.length).toBe(1);
    expect(sf.inbox[0].msg.count).toBe(3);
  });
});

describe('graph-worker shutdown', () => {
  it('calls self.close() and stops the simulation', async () => {
    const { nodes, edges } = makeFixture(5);
    const sf = captureSelf();
    await handleMessage({ kind: 'init', nodes, edges, useSAB: false }, sf);
    await handleMessage({ kind: 'shutdown' }, sf);
    expect(sf.closeCalls).toBe(1);
  });
});

describe('writePositions', () => {
  it('packs x/y/z into a Float32Array in order', () => {
    const nodes = [
      { x: 1, y: 2, z: 3 },
      { x: 4, y: 5, z: 6 },
      { x: 7, y: 8, z: 9 },
    ];
    const out = new Float32Array(nodes.length * POS_FLOATS_PER_NODE);
    writePositions(out, nodes);
    expect(Array.from(out)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9]);
  });
});
