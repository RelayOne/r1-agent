// SPDX-License-Identifier: MIT
//
// Spec 5 §5 + §10 T2 — TypeScript-typed sibling to the
// cmd/r1-server/ui/web/js/graph-worker.test.js suite Spec 2 shipped.
//
// This test loads the shared graph-worker.js module through the same
// vitest config but exercises it against a JSON fixture
// (testdata/graph-50.json) so the failure mode "the worker won't
// converge a real-shape graph" is caught at the TS level too. The
// graph-worker.d.ts ambient declarations give the JS module a typed
// surface so this test compiles without unsafe casts.
//
// Note: this is a vitest jsdom test. It does NOT spawn a real Web
// Worker; it drives handleMessage directly with a captureSelf shim
// (same pattern as graph-worker.test.js). A real-Worker test that
// spawns the .js file via `new Worker(url, {type:'module'})` is a
// future spec — happy-dom + vitest's worker surface needs Node 22 +
// jsdom 29, which this branch's CI doesn't have until
// ci/node-22-lts-bump merges.

import { describe, it, expect, beforeEach } from 'vitest';
import fixture from './testdata/graph-50.json';
import {
  handleMessage,
  setSimulationFactory,
  _resetState,
  type WorkerLike,
} from '../../../cmd/r1-server/ui/web/js/graph-worker.js';

interface CapturedMsg {
  kind: string;
  alpha?: number;
  count?: number;
  positions?: Float32Array;
}

interface CapturedSelf extends WorkerLike {
  inbox: Array<{ msg: CapturedMsg; transferList?: ArrayBuffer[] }>;
  closeCalls: number;
}

function captureSelf(): CapturedSelf {
  const inbox: CapturedSelf['inbox'] = [];
  const sf: CapturedSelf = {
    inbox,
    closeCalls: 0,
    postMessage(msg, transferList) {
      inbox.push({ msg: msg as CapturedMsg, transferList });
    },
    close() { sf.closeCalls += 1; },
  };
  return sf;
}

interface FluentSim {
  nodes(arr?: unknown[]): FluentSim | unknown[];
  alpha(v?: number): FluentSim | number;
  alphaDecay(v: number): FluentSim;
  stop(): FluentSim;
  restart(): FluentSim;
  tick(): FluentSim;
}

beforeEach(() => {
  _resetState();
  let alphaVal = 1;
  const decay = 0.04;
  let nodes: Array<{ id?: string; x?: number; y?: number; z?: number }> = [];
  const sim: FluentSim = {
    nodes(arr) { if (arr) { nodes = arr as typeof nodes; return sim; } return nodes; },
    alpha(v) { if (v !== undefined) { alphaVal = v; return sim; } return alphaVal; },
    alphaDecay() { return sim; },
    stop() { return sim; },
    restart() { return sim; },
    tick() {
      for (const n of nodes) {
        n.x = (n.x || 0) * 0.99 + 0.01 * Math.sin(nodes.indexOf(n));
        n.y = (n.y || 0) * 0.99 + 0.01 * Math.cos(nodes.indexOf(n));
        n.z = (n.z || 0) * 0.99;
      }
      alphaVal = Math.max(0, alphaVal - decay);
      return sim;
    },
  };
  setSimulationFactory((incomingNodes) => {
    nodes = incomingNodes as typeof nodes;
    sim.nodes(nodes);
    return sim;
  });
});

describe('graph-worker.ts (50-node JSON fixture)', () => {
  it('converges the layout in <200 ticks (alpha < 0.02)', async () => {
    const sf = captureSelf();
    await handleMessage({ kind: 'init', nodes: fixture.nodes, edges: fixture.edges, useSAB: false }, sf);

    let lastAlpha = 1;
    let convergedAt = -1;
    for (let i = 0; i < 200; i++) {
      sf.inbox.length = 0;
      await handleMessage({ kind: 'tick' }, sf);
      const out = sf.inbox[0]?.msg;
      if (out && typeof out.alpha === 'number') {
        lastAlpha = out.alpha;
        if (lastAlpha < 0.02) { convergedAt = i; break; }
      }
    }
    expect(lastAlpha).toBeLessThan(0.02);
    expect(convergedAt).toBeGreaterThanOrEqual(0);
    expect(convergedAt).toBeLessThan(200);
  });

  it('emits Float32Array positions sized 3 × node count on init', async () => {
    const sf = captureSelf();
    await handleMessage({ kind: 'init', nodes: fixture.nodes, edges: fixture.edges, useSAB: false }, sf);
    expect(sf.inbox.length).toBe(1);
    const out = sf.inbox[0].msg;
    expect(out.kind).toBe('positions');
    expect(out.count).toBe(50);
    expect(out.positions).toBeInstanceOf(Float32Array);
    expect((out.positions as Float32Array).length).toBe(50 * 3);
  });
});
