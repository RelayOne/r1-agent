// SPDX-License-Identifier: MIT
// Ambient declarations for graph-worker.js so the Spec 5 vitest TS
// test (web/src/test/graph-worker.test.ts) imports cleanly. The JS
// module's runtime entry point is a Web Worker; these declarations
// describe the named exports the test harness drives.

export const POS_FLOATS_PER_NODE: number;

export function setSimulationFactory(fn: (nodes: unknown[], edges?: unknown[]) => unknown): void;

export function _resetState(): void;
export function _getState(): {
  sim: unknown;
  nodes: Array<{ id?: string; x?: number; y?: number; z?: number }>;
  edges: unknown[];
  useSAB: boolean;
};
export function _ensureSimulationSync(): unknown;

export function writePositions(out: Float32Array, nodes?: Array<{ x?: number; y?: number; z?: number }>): void;

export interface WorkerLike {
  postMessage(msg: unknown, transferList?: ArrayBuffer[]): void;
  close?(): void;
}

export function handleMessage(msg: { kind: string; [k: string]: unknown }, self_?: WorkerLike): Promise<void>;
