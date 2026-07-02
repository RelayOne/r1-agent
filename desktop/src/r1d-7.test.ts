// SPDX-License-Identifier: MIT
//
// R1D-7 tests — settings-panel truthfulness.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Plugin mocks for the live Auto-start / Lanes sections (audit A054).
// settings.ts pulls lib/autostart + lib/lanePrefs, which import the
// tauri plugins at module scope; mock them so vitest exercises the
// real wiring without a Tauri runtime.
const pluginMocks = vi.hoisted(() => ({
  os: { enabled: false },
  store: new Map<string, unknown>(),
}));

vi.mock("@tauri-apps/plugin-autostart", () => ({
  enable: vi.fn(async () => {
    pluginMocks.os.enabled = true;
  }),
  disable: vi.fn(async () => {
    pluginMocks.os.enabled = false;
  }),
  isEnabled: vi.fn(async () => pluginMocks.os.enabled),
}));

vi.mock("@tauri-apps/plugin-store", () => ({
  load: vi.fn(async () => ({
    get: async (k: string) => pluginMocks.store.get(k),
    set: async (k: string, v: unknown) => {
      pluginMocks.store.set(k, v);
    },
    save: async () => {},
  })),
}));

import { enable as autostartEnable } from "@tauri-apps/plugin-autostart";

function makeRoot(): HTMLElement {
  const div = document.createElement("div");
  document.body.appendChild(div);
  return div;
}

function cleanupDom(): void {
  document.body.innerHTML = "";
}

async function mountAndOpen(section: "providers" | "vault" | "governance" | "daemon" | "autostart" | "lanes") {
  vi.resetModules();
  const settings = await import("./panels/settings");
  const root = makeRoot();
  settings.mountSettings(root);
  settings.openSettings(section);
  return { root, settings };
}

describe("settings panel — R1D-7 truthfulness", () => {
  beforeEach(() => {
    vi.spyOn(console, "info").mockImplementation(() => {});
    pluginMocks.os.enabled = false;
    pluginMocks.store.clear();
    vi.mocked(autostartEnable).mockClear();
  });

  afterEach(() => {
    cleanupDom();
  });

  it("renders providers as read-only instead of exposing unsupported test/save controls", async () => {
    await mountAndOpen("providers");

    const unavailable = document.querySelector('[data-role="providers-unavailable"]');
    expect(unavailable?.textContent).toContain("read-only");
    expect(document.querySelector('[data-role="test-btn"]')).toBeNull();
    expect(console.info).not.toHaveBeenCalled();
  });

  it("renders the vault section as unavailable", async () => {
    await mountAndOpen("vault");
    expect(document.querySelector('[data-role="vault-unavailable"]')?.textContent).toContain(
      "does not implement the vault IPC surface",
    );
    expect(console.info).not.toHaveBeenCalled();
  });

  it("renders the governance section as unavailable", async () => {
    await mountAndOpen("governance");
    expect(document.querySelector('[data-role="gov-unavailable"]')?.textContent).toContain(
      "read-only",
    );
    expect(console.info).not.toHaveBeenCalled();
  });

  it("renders a live autostart toggle bound to tauri-plugin-autostart (A054)", async () => {
    await mountAndOpen("autostart");

    // The section probes getAutostart() async; wait for the enabled
    // checkbox. No "unavailable" notice may remain.
    const toggle = await vi.waitFor(() => {
      const el = document.querySelector<HTMLInputElement>(
        '[data-role="autostart-toggle"]',
      );
      expect(el).not.toBeNull();
      expect(el!.disabled).toBe(false);
      return el!;
    });
    expect(document.querySelector('[data-role="autostart-unavailable"]')).toBeNull();
    expect(toggle.checked).toBe(false);

    toggle.checked = true;
    toggle.dispatchEvent(new Event("change"));

    await vi.waitFor(() => {
      expect(vi.mocked(autostartEnable)).toHaveBeenCalledTimes(1);
    });
    // Preference persisted to the (mocked) prefs.json store and the
    // re-rendered checkbox reflects the new desired state.
    await vi.waitFor(() => {
      expect(pluginMocks.store.get("autostart_enabled")).toBe(true);
      const el = document.querySelector<HTMLInputElement>(
        '[data-role="autostart-toggle"]',
      );
      expect(el?.checked).toBe(true);
    });
  });

  it("renders a live lane-density radio group persisted via plugin-store (A054)", async () => {
    await mountAndOpen("lanes");

    const radios = await vi.waitFor(() => {
      const els = document.querySelectorAll<HTMLInputElement>(
        '[data-role="lanes-density-input"]',
      );
      expect(els.length).toBe(3);
      expect(els[0].disabled).toBe(false);
      return els;
    });
    expect(document.querySelector('[data-role="lanes-unavailable"]')).toBeNull();
    // Default density "normal" pre-selected.
    expect(radios[1].checked).toBe(true);

    radios[2].checked = true;
    radios[2].dispatchEvent(new Event("change"));

    await vi.waitFor(() => {
      expect(pluginMocks.store.get("lane_density")).toBe("summary");
    });
    await vi.waitFor(() => {
      const els = document.querySelectorAll<HTMLInputElement>(
        '[data-role="lanes-density-input"]',
      );
      expect(els[2].checked).toBe(true);
    });
  });

  it("keeps the daemon subsection live", async () => {
    await mountAndOpen("daemon");

    expect(document.querySelector('[data-role="daemon-reconnect"]')).not.toBeNull();
    expect(document.querySelector('[data-role="daemon-install"]')).not.toBeNull();
    expect(document.querySelector('[data-role="daemon-url"]')).not.toBeNull();
    expect(document.querySelector('[data-role="vault-unavailable"]')).toBeNull();
  });
});
