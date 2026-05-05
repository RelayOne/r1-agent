// SPDX-License-Identifier: MIT
//
// Auto-start glue — wraps tauri-plugin-autostart with persistence
// and reconciliation.
//
// Spec desktop-cortex-augmentation §10 + checklist item 29:
// the user-visible "Start at login" toggle persists desired state
// in `prefs.json` via tauri-plugin-store; on every app start we
// reconcile actual OS-side hook state against the persisted
// preference, fixing drift if the user (e.g.) deleted the
// LaunchAgent plist or disabled the Run-key value out of band.
//
// All five public functions return Promises and never throw on
// expected error paths (plugin not loaded, permission denied);
// instead they fall back to the persisted preference and surface
// failures via a structured `AutostartResult`.

import { enable, disable, isEnabled } from "@tauri-apps/plugin-autostart";
import { load, type Store } from "@tauri-apps/plugin-store";

const PREFS_FILE = "prefs.json";
const AUTOSTART_KEY = "autostart_enabled";

// -------------------------------------------------------------------
// Types
// -------------------------------------------------------------------

export interface AutostartState {
  /** Persisted desired state (truth-of-record for the UI). */
  desired: boolean;
  /** Actual OS-side hook state, when probable. `null` = couldn't probe. */
  actual: boolean | null;
}

export interface AutostartResult {
  ok: boolean;
  state: AutostartState;
  error?: string;
}

// -------------------------------------------------------------------
// Internal store handle (lazy, idempotent)
// -------------------------------------------------------------------

let prefsPromise: Promise<Store> | null = null;

async function prefs(): Promise<Store> {
  if (!prefsPromise) {
    prefsPromise = load(PREFS_FILE, { autoSave: true });
  }
  return prefsPromise;
}

/** Test-only reset so unit tests can swap a mock plugin-store. */
export function __resetForTests(): void {
  prefsPromise = null;
}

// -------------------------------------------------------------------
// Read / probe
// -------------------------------------------------------------------

async function readDesired(): Promise<boolean> {
  const s = await prefs();
  const v = await s.get<boolean>(AUTOSTART_KEY);
  return typeof v === "boolean" ? v : false;
}

async function writeDesired(value: boolean): Promise<void> {
  const s = await prefs();
  await s.set(AUTOSTART_KEY, value);
}

async function probeActual(): Promise<boolean | null> {
  try {
    return await isEnabled();
  } catch {
    return null;
  }
}

// -------------------------------------------------------------------
// Public surface
// -------------------------------------------------------------------

/**
 * Read the current autostart state without mutating anything.
 * `desired` is the persisted preference; `actual` is the live OS
 * hook state, or null if probing failed.
 */
export async function getAutostart(): Promise<AutostartState> {
  const desired = await readDesired();
  const actual = await probeActual();
  return { desired, actual };
}

/**
 * Set autostart on. Persists the preference, then asks the OS hook
 * to register. On OS-side failure the preference is rolled back so
 * the UI never lies about what's stored.
 */
export async function setAutostart(value: boolean): Promise<AutostartResult> {
  const previous = await readDesired();
  await writeDesired(value);
  try {
    if (value) await enable();
    else await disable();
  } catch (err) {
    // Roll back the persisted preference so it stays consistent
    // with the OS-side state.
    await writeDesired(previous);
    return {
      ok: false,
      state: {
        desired: previous,
        actual: await probeActual(),
      },
      error: err instanceof Error ? err.message : String(err),
    };
  }
  return {
    ok: true,
    state: { desired: value, actual: await probeActual() },
  };
}

/**
 * Reconcile OS-side state with the persisted preference. Called
 * once at app start so a drift (e.g. the user deleted the
 * LaunchAgent plist while the app was off) is auto-corrected.
 *
 * Reconciliation rules:
 *
 *   * desired=true, actual=false → re-enable (and report as fixed).
 *   * desired=false, actual=true → disable (treat persisted off as
 *     authoritative; user clicked off in the UI before, OS-side
 *     drift shouldn't override that).
 *   * desired matches actual → no-op.
 *   * actual=null (couldn't probe) → no-op; trust persisted state
 *     so we don't toggle the OS hook off blindly.
 */
export async function reconcileAutostart(): Promise<AutostartResult> {
  const desired = await readDesired();
  const actual = await probeActual();
  if (actual === null) {
    return { ok: true, state: { desired, actual: null } };
  }
  if (desired === actual) {
    return { ok: true, state: { desired, actual } };
  }
  try {
    if (desired) await enable();
    else await disable();
  } catch (err) {
    return {
      ok: false,
      state: { desired, actual },
      error: err instanceof Error ? err.message : String(err),
    };
  }
  return {
    ok: true,
    state: { desired, actual: desired },
  };
}
