// SPDX-License-Identifier: MIT
//
// Auto-start glue tests (audit/complete-systems-2026-07-01.md A054).
//
// Pins the persistence + rollback contract of setAutostart and every
// reconcileAutostart drift rule documented in autostart.ts. The
// plugin bridge is mocked so the suite runs without a Tauri runtime.

import { beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({
  os: {
    enabled: false,
    probeFails: false,
    enableFails: false,
    disableFails: false,
  },
  store: new Map<string, unknown>(),
}));

vi.mock("@tauri-apps/plugin-autostart", () => ({
  enable: vi.fn(async () => {
    if (h.os.enableFails) throw new Error("enable denied");
    h.os.enabled = true;
  }),
  disable: vi.fn(async () => {
    if (h.os.disableFails) throw new Error("disable denied");
    h.os.enabled = false;
  }),
  isEnabled: vi.fn(async () => {
    if (h.os.probeFails) throw new Error("probe failed");
    return h.os.enabled;
  }),
}));

vi.mock("@tauri-apps/plugin-store", () => ({
  load: vi.fn(async () => ({
    get: async (k: string) => h.store.get(k),
    set: async (k: string, v: unknown) => {
      h.store.set(k, v);
    },
    save: async () => {},
  })),
}));

import { enable, disable } from "@tauri-apps/plugin-autostart";
import {
  __resetForTests,
  getAutostart,
  reconcileAutostart,
  setAutostart,
} from "./autostart";

const KEY = "autostart_enabled";

beforeEach(() => {
  h.os.enabled = false;
  h.os.probeFails = false;
  h.os.enableFails = false;
  h.os.disableFails = false;
  h.store.clear();
  vi.clearAllMocks();
  __resetForTests();
});

describe("setAutostart", () => {
  it("persists the preference and registers the OS hook", async () => {
    const res = await setAutostart(true);
    expect(res.ok).toBe(true);
    expect(res.state).toEqual({ desired: true, actual: true });
    expect(h.store.get(KEY)).toBe(true);

    const probe = await getAutostart();
    expect(probe).toEqual({ desired: true, actual: true });
  });

  it("rolls the persisted preference back when the OS hook fails", async () => {
    h.os.enableFails = true;
    const res = await setAutostart(true);
    expect(res.ok).toBe(false);
    expect(res.error).toContain("enable denied");
    expect(res.state.desired).toBe(false);
    expect(h.store.get(KEY)).toBe(false);
  });
});

describe("reconcileAutostart drift rules", () => {
  it("re-enables when desired=true but the OS hook was removed out of band", async () => {
    h.store.set(KEY, true);
    h.os.enabled = false;
    const res = await reconcileAutostart();
    expect(vi.mocked(enable)).toHaveBeenCalledTimes(1);
    expect(res).toEqual({ ok: true, state: { desired: true, actual: true } });
  });

  it("disables when the persisted preference is off but the OS hook is on", async () => {
    h.store.set(KEY, false);
    h.os.enabled = true;
    const res = await reconcileAutostart();
    expect(vi.mocked(disable)).toHaveBeenCalledTimes(1);
    expect(h.os.enabled).toBe(false);
    expect(res.ok).toBe(true);
  });

  it("is a no-op when desired matches actual", async () => {
    h.store.set(KEY, true);
    h.os.enabled = true;
    const res = await reconcileAutostart();
    expect(vi.mocked(enable)).not.toHaveBeenCalled();
    expect(vi.mocked(disable)).not.toHaveBeenCalled();
    expect(res).toEqual({ ok: true, state: { desired: true, actual: true } });
  });

  it("trusts the persisted state and does not toggle when probing fails", async () => {
    h.store.set(KEY, true);
    h.os.probeFails = true;
    const res = await reconcileAutostart();
    expect(vi.mocked(enable)).not.toHaveBeenCalled();
    expect(vi.mocked(disable)).not.toHaveBeenCalled();
    expect(res).toEqual({ ok: true, state: { desired: true, actual: null } });
  });

  it("surfaces a structured error when the corrective toggle fails", async () => {
    h.store.set(KEY, true);
    h.os.enabled = false;
    h.os.enableFails = true;
    const res = await reconcileAutostart();
    expect(res.ok).toBe(false);
    expect(res.error).toContain("enable denied");
  });
});
