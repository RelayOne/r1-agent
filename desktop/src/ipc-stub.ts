// SPDX-License-Identifier: MIT
//
// Invoke stub used by every R1D-2 panel skeleton.
//
// At scaffold time the Tauri runtime is not yet wired (R1D-1.1 /
// R1D-1.2 lands that). Panels still want to exercise their IPC surface
// so the code paths are real; this module provides a tiny shim that
// logs the call with its method-name + phase tag and returns a typed
// empty value that matches the contract's result shape.
//
// When R1D-1 wires Tauri, this file becomes a thin passthrough to
// `@tauri-apps/api/core`'s `invoke<T>(method, args)`.

import type { InvokeMethod } from "./types/ipc";

/** Phase tag each panel attaches when logging its TODO stub call. */
export type PhaseTag =
  | "R1D-3" // SOW tree + descent ladder
  | "R1D-4" // Skill catalog + marketplace + test modal
  | "R1D-5" // Ledger viewer
  | "R1D-6" // Memory inspector
  | "R1D-9"; // Cost panel / observability

/**
 * invokeStub logs a structured TODO line and resolves with the
 * caller-supplied empty/zero value. The shape of `empty` is the panel's
 * responsibility; each panel imports the matching Result type from
 * `types/ipc` and constructs a correctly-typed empty object.
 */
export async function invokeStub<T>(
  method: InvokeMethod,
  phase: PhaseTag,
  empty: T,
  args?: Record<string, unknown>,
): Promise<T> {
  console.info(
    `[r1-desktop] TODO ${phase}: invoke("${method}") — scaffold stub returning empty`,
    args ?? {},
  );
  return empty;
}
