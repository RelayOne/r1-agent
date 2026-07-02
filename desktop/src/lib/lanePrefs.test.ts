// SPDX-License-Identifier: MIT
//
// Lane density preference tests (audit A054).

import { beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({
  store: new Map<string, unknown>(),
  loadFails: false,
}));

vi.mock("@tauri-apps/plugin-store", () => ({
  load: vi.fn(async () => {
    if (h.loadFails) throw new Error("store unavailable");
    return {
      get: async (k: string) => h.store.get(k),
      set: async (k: string, v: unknown) => {
        h.store.set(k, v);
      },
      save: async () => {},
    };
  }),
}));

import {
  __resetForTests,
  getLaneDensity,
  LANE_DENSITY_EVENT,
  setLaneDensity,
} from "./lanePrefs";

beforeEach(() => {
  h.store.clear();
  h.loadFails = false;
  vi.clearAllMocks();
  __resetForTests();
});

describe("lane density persistence", () => {
  it("round-trips a valid density through prefs.json", async () => {
    expect(await getLaneDensity()).toBeNull();
    expect(await setLaneDensity("summary")).toBe(true);
    expect(h.store.get("lane_density")).toBe("summary");
    expect(await getLaneDensity()).toBe("summary");
  });

  it("ignores invalid stored values", async () => {
    h.store.set("lane_density", "extra-loud");
    expect(await getLaneDensity()).toBeNull();
  });

  it("broadcasts LANE_DENSITY_EVENT on successful save only", async () => {
    const seen: unknown[] = [];
    const onEvent = (ev: Event) => seen.push((ev as CustomEvent).detail);
    window.addEventListener(LANE_DENSITY_EVENT, onEvent);
    try {
      await setLaneDensity("verbose");
      expect(seen).toEqual(["verbose"]);

      h.loadFails = true;
      __resetForTests();
      expect(await setLaneDensity("summary")).toBe(false);
      expect(seen).toEqual(["verbose"]); // no broadcast on failure
    } finally {
      window.removeEventListener(LANE_DENSITY_EVENT, onEvent);
    }
  });

  it("degrades to null / false outside a Tauri runtime", async () => {
    h.loadFails = true;
    expect(await getLaneDensity()).toBeNull();
    expect(await setLaneDensity("normal")).toBe(false);
  });
});
