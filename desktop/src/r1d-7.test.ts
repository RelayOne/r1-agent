// SPDX-License-Identifier: MIT
//
// R1D-7 tests — settings-panel truthfulness.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

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

  it("renders the autostart section as unavailable", async () => {
    await mountAndOpen("autostart");
    expect(document.querySelector('[data-role="autostart-unavailable"]')?.textContent).toContain(
      "Auto-start remains unchanged by this desktop build",
    );
    expect(console.info).not.toHaveBeenCalled();
  });

  it("renders the lane-density section as unavailable", async () => {
    await mountAndOpen("lanes");
    expect(document.querySelector('[data-role="lanes-unavailable"]')?.textContent).toContain(
      "read-only",
    );
    expect(console.info).not.toHaveBeenCalled();
  });

  it("keeps the daemon subsection live", async () => {
    await mountAndOpen("daemon");

    expect(document.querySelector('[data-role="daemon-reconnect"]')).not.toBeNull();
    expect(document.querySelector('[data-role="daemon-install"]')).not.toBeNull();
    expect(document.querySelector('[data-role="daemon-url"]')).not.toBeNull();
    expect(document.querySelector('[data-role="vault-unavailable"]')).toBeNull();
  });
});
