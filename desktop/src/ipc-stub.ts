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

/** Phase tag each panel attaches when logging a stub call. */
export type PhaseTag =
  | "R1D-1"
  | "R1D-2"
  | "R1D-3" // SOW tree + descent ladder
  | "R1D-4" // Skill catalog + marketplace + test modal
  | "R1D-5" // Ledger viewer
  | "R1D-6" // Memory inspector
  | "R1D-7" // Settings + vault + providers + governance
  | "R1D-8" // MCP servers panel
  | "R1D-9" // Cost panel / observability dashboard
  | "R1D-10" // Approval queue + scheduler
  | "R1D-11" // Packaging / first-launch onboarding
  | "R1D-12"
  | "R1D-augmentation"; // Settings panel daemon-status, autostart, lane-density (desktop-cortex-augmentation spec)

/**
 * Resolve Tauri's invoke function at runtime.
 *
 * Returns the real `invoke` when running inside a Tauri WebView, or
 * null when running in a plain browser / vitest environment.
 * Cached after first call so the dynamic import only fires once.
 */
let _tauriInvoke: TauriInvokeFn | null | undefined = undefined;

/**
 * Test-only invoke override. When set, invokeStub routes every call
 * through this function instead of the real Tauri bridge / dev stub,
 * so vitest can exercise panel error paths (e.g. a host verb
 * rejecting with the -32010 not_implemented taxonomy error).
 */
let _invokeOverride: TauriInvokeFn | null = null;

export function __setInvokeForTests(fn: TauriInvokeFn | null): void {
  _invokeOverride = fn;
}

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
  const tauriInvoke = _invokeOverride ?? (await getTauriInvoke());

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

// ---------------------------------------------------------------------------
// RPC failure classification (audit A034)
//
// The Rust host propagates subprocess JSON-RPC errors verbatim
// (subprocess.rs rpc_call), so a rejected invoke carries the IpcError
// shape `{code, stoke_code, message}` — or, for errors that travelled
// an extra JSON-RPC hop, `{code, message, data: {stoke_code}}`. Panels
// use classifyIpcError to distinguish "host verb unimplemented"
// (render a truthful unavailable state) from a genuine failure
// (render an error row).
// ---------------------------------------------------------------------------

/** Classification of a rejected invoke, consumed by panel loaders. */
export interface IpcFailure {
  /** True when the host reported not_implemented (-32010). */
  notImplemented: boolean;
  /** Human-readable detail extracted from the rejection. */
  message: string;
}

export function classifyIpcError(err: unknown): IpcFailure {
  let notImplemented = false;
  let message = "";

  if (err && typeof err === "object") {
    const rec = err as Record<string, unknown>;
    const data = rec.data as Record<string, unknown> | undefined;
    const stokeCode =
      (typeof rec.stoke_code === "string" && rec.stoke_code) ||
      (data && typeof data.stoke_code === "string" && data.stoke_code) ||
      "";
    notImplemented = rec.code === -32010 || stokeCode === "not_implemented";
    if (typeof rec.message === "string" && rec.message.length > 0) {
      message = rec.message;
    }
  }
  if (!message) {
    message = err instanceof Error ? err.message : String(err);
  }
  return { notImplemented, message };
}
