// SPDX-License-Identifier: MIT
//
// Test stub for the `d3-force-3d` bare specifier. Aliased in
// web/vitest.config.ts so graph-worker.test.js doesn't drag the
// vendored UMD bundle into Node — every test that needs simulation
// behaviour injects its own factory via setSimulationFactory.
//
// Each named export is the minimum fluent shape the runtime code
// references. None of these helpers are called by the test suite;
// they exist so the dynamic-import path in graph-worker.js
// resolves cleanly when vite analyses the module.
export function forceSimulation(nodes: unknown[], _dim?: number) {
  return chain({ nodes });
}
export function forceLink() { return chain(); }
export function forceManyBody() { return chain(); }
export function forceCenter() { return chain(); }

function chain(state: Record<string, unknown> = {}) {
  const obj = {
    state,
    force() { return obj; },
    nodes(arr?: unknown[]) {
      if (arr) { obj.state.nodes = arr; return obj; }
      return obj.state.nodes;
    },
    id() { return obj; },
    distance() { return obj; },
    strength() { return obj; },
    alpha(v?: number) { if (v !== undefined) { obj.state.alpha = v; return obj; } return obj.state.alpha ?? 0; },
    alphaDecay() { return obj; },
    tick() { return obj; },
    stop() { return obj; },
    restart() { return obj; },
  };
  return obj;
}
