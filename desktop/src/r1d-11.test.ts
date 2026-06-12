// SPDX-License-Identifier: MIT
//
// R1D-11 tests — onboarding truthfulness.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const invokeStub = vi.fn();

vi.mock("./ipc-stub", () => ({
  invokeStub,
}));

function makeRoot(): HTMLElement {
  const div = document.createElement("div");
  document.body.appendChild(div);
  return div;
}

function clickTextButton(label: string): void {
  const buttons = Array.from(document.querySelectorAll<HTMLButtonElement>("button"));
  const btn = buttons.find((node) => node.textContent?.trim() === label);
  if (!btn) {
    throw new Error(`button not found: ${label}`);
  }
  btn.click();
}

async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

describe("onboarding wizard — host parity truthfulness", () => {
  beforeEach(() => {
    vi.resetModules();
    invokeStub.mockReset();
    window.localStorage.clear();
    document.body.innerHTML = "";
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("uses the registered folder-picker host verb for Browse", async () => {
    invokeStub.mockResolvedValue({ path: "/tmp/r1-data" });
    const { mountOnboarding } = await import("./onboarding/onboarding");
    mountOnboarding(makeRoot());

    clickTextButton("Get started");
    const browse = Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find(
      (node) => node.textContent?.trim() === "Browse",
    );
    if (!browse) {
      throw new Error("browse button missing");
    }
    browse.click();
    await flush();

    expect(invokeStub).toHaveBeenCalledWith(
      "app_open_folder_picker",
      "R1D-11",
      { path: "~/.r1/" },
      { params: { title: "Choose R1 data directory" } },
    );
    expect((document.getElementById("r1-onboarding-datadir") as HTMLInputElement | null)?.value).toBe("/tmp/r1-data");
  });

  it("persists required provider keys locally without invoking a fake host vault verb", async () => {
    const { persistApiKey } = await import("./onboarding/onboarding");

    const result = await persistApiKey("claude", " sk-ant-test ", true);

    expect(result).toEqual({
      ok: true,
      vault_id: "r1.onboarding.api_key.claude",
    });
    expect(window.localStorage.getItem("r1.onboarding.api_key.claude")).toBe("sk-ant-test");
    expect(invokeStub).not.toHaveBeenCalled();
  });

  it("renders the demo step as unavailable and continues without calling a fake demo verb", async () => {
    const { mountOnboarding } = await import("./onboarding/onboarding");
    mountOnboarding(makeRoot());

    clickTextButton("Get started");
    clickTextButton("Next");
    const ollama = document.querySelector<HTMLInputElement>('input[type="radio"][value="ollama"]');
    if (!ollama) {
      throw new Error("ollama radio missing");
    }
    ollama.click();
    ollama.dispatchEvent(new Event("change", { bubbles: true }));
    await flush();
    clickTextButton("Next");
    await flush();

    expect(document.body.textContent).toContain("not wired in this desktop build yet");
    expect(document.body.textContent).toContain("read-only for now");
    expect(document.querySelector('.r1-onboarding-toggle')).toBeNull();

    clickTextButton("Continue");
    expect(document.body.textContent).toContain("unavailable in this build");
    expect(invokeStub).not.toHaveBeenCalled();
  });
});
