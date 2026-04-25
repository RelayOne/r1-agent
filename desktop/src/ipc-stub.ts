// SPDX-License-Identifier: MIT
//
// R1 Desktop IPC bridge — real Tauri invoke with dev-stub fallback.
//
// R1D-1.2 wiring: when the Tauri runtime is available (window.__TAURI__
// is defined) this module delegates to `@tauri-apps/api/core`'s invoke<T>.
// In vitest / browser-without-Tauri environments the original stub
// behaviour is preserved — log a structured TODO and return the empty
// value supplied by the caller.
//
// All R1D-2/3 panels already call invokeStub; replacing this file means
// they get real IPC with zero further changes.

import type { InvokeMethod } from "./types/ipc";

// Tauri v2 invoke — imported dynamically to avoid breaking non-Tauri builds.
// The type is only used for the conditional import path.
type TauriInvokeFn = <T>(cmd: string, args?: Record<string, unknown>) => Promise<T>;

/** Phase tag each panel attaches when logging a TODO stub call. */
export type PhaseTag =
  | "R1D-1"
  | "R1D-2"
  | "R1D-3"
  | "R1D-4"
  | "R1D-5"
  | "R1D-6"
  | "R1D-7"
  | "R1D-8"
  | "R1D-9"
  | "R1D-10"
  | "R1D-11"
  | "R1D-12";

/**
 * Resolve Tauri's invoke function at runtime.
 *
 * Returns the real `invoke` when running inside a Tauri WebView, or
 * null when running in a plain browser / vitest environment.
 * Cached after first call so the dynamic import only fires once.
 */
let _tauriInvoke: TauriInvokeFn | null | undefined = undefined;

async function getTauriInvoke(): Promise<TauriInvokeFn | null> {
  if (_tauriInvoke !== undefined) return _tauriInvoke;
  // window.__TAURI__ is injected by the Tauri WebView runtime.
  if (typeof window !== "undefined" && "__TAURI__" in window) {
    try {
      const mod = await import("@tauri-apps/api/core");
      _tauriInvoke = mod.invoke as TauriInvokeFn;
    } catch {
      _tauriInvoke = null;
    }
  } else {
    _tauriInvoke = null;
  }
  return _tauriInvoke;
}

/**
 * invokeStub — call the Tauri host command `method` with `args`.
 *
 * • When running inside a real Tauri WebView: delegates to the Rust
 *   `invoke_handler`, which round-trips to the r1 subprocess.
 * • When running in vitest / plain browser: logs a structured TODO
 *   and resolves with the caller-supplied `empty` value.
 *
 * The `phase` tag and `empty` value are only used by the stub path;
 * they are no-ops in the Tauri path. Callers need not change their
 * call sites when Tauri is wired.
 */
export async function invokeStub<T>(
  method: InvokeMethod,
  phase: PhaseTag,
  empty: T,
  args?: Record<string, unknown>,
): Promise<T> {
  const tauriInvoke = await getTauriInvoke();

  if (tauriInvoke !== null) {
    // Real Tauri path — forward to the Rust invoke_handler.
    return tauriInvoke<T>(method, args);
  }

  // Dev-stub path — log and return empty value.
  console.info(
    `[r1-desktop] TODO ${phase}: invoke("${method}") — scaffold stub returning empty`,
    args ?? {},
  );
  return empty;
}
