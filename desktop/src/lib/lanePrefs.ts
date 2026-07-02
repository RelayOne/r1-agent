// SPDX-License-Identifier: MIT
//
// Lane preference glue — persists the lane-card density choice to
// `prefs.json` via tauri-plugin-store and broadcasts changes so open
// lane rails re-render immediately.
//
// Spec desktop-cortex-augmentation §8 + checklist item 27: the
// Settings → Lanes radio group (Verbose / Normal / Summary) controls
// how much detail each lane card renders. Wired per
// audit/complete-systems-2026-07-01.md A054 — previously the section
// rendered a "host preferences handler not present" notice while the
// store plugin was registered and unused.
//
// Both public functions degrade gracefully outside a Tauri runtime:
// `getLaneDensity` resolves null (caller keeps its default) and
// `setLaneDensity` resolves false so the UI can surface the failure.

import { load, type Store } from "@tauri-apps/plugin-store";

const PREFS_FILE = "prefs.json";
const DENSITY_KEY = "lane_density";

/** window CustomEvent topic fired after a successful density save. */
export const LANE_DENSITY_EVENT = "r1:lane-density-changed";

export type LaneDensity = "verbose" | "normal" | "summary";

export function isLaneDensity(v: unknown): v is LaneDensity {
  return v === "verbose" || v === "normal" || v === "summary";
}

let prefsPromise: Promise<Store> | null = null;

async function prefs(): Promise<Store> {
  if (!prefsPromise) {
    prefsPromise = load(PREFS_FILE, { autoSave: true, defaults: {} });
  }
  return prefsPromise;
}

/** Test-only reset so unit tests can swap a mock plugin-store. */
export function __resetForTests(): void {
  prefsPromise = null;
}

/**
 * Read the persisted density. Resolves null when nothing valid is
 * stored or the store plugin is unavailable (non-Tauri build) — the
 * caller keeps its in-memory default in both cases.
 */
export async function getLaneDensity(): Promise<LaneDensity | null> {
  try {
    const s = await prefs();
    const v = await s.get<string>(DENSITY_KEY);
    return isLaneDensity(v) ? v : null;
  } catch {
    return null;
  }
}

/**
 * Persist a density choice and notify open lane rails via
 * LANE_DENSITY_EVENT. Resolves false (and skips the broadcast) when
 * persistence failed so the settings panel can render a truthful
 * error instead of pretending the preference stuck.
 */
export async function setLaneDensity(density: LaneDensity): Promise<boolean> {
  try {
    const s = await prefs();
    await s.set(DENSITY_KEY, density);
  } catch {
    return false;
  }
  if (typeof window !== "undefined") {
    window.dispatchEvent(
      new CustomEvent<LaneDensity>(LANE_DENSITY_EVENT, { detail: density }),
    );
  }
  return true;
}
