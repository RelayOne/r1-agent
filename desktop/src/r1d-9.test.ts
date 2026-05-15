// SPDX-License-Identifier: MIT
//
// R1D-9 tests — observability panel truthfulness.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderPanel } from "./panels/observability";

function makeRoot(): HTMLElement {
  const div = document.createElement("div");
  document.body.appendChild(div);
  return div;
}

function cleanup(root: HTMLElement): void {
  root.remove();
}

describe("observability panel — R1D-9 truthfulness", () => {
  let root: HTMLElement;

  beforeEach(() => {
    vi.spyOn(console, "info").mockImplementation(() => {});
    root = makeRoot();
    renderPanel(root);
  });

  afterEach(() => {
    cleanup(root);
  });

  it("renders the unavailable state instead of loading dashboard placeholders", () => {
    const unavailable = root.querySelector('[data-role="obs-unavailable"]');
    expect(unavailable).not.toBeNull();
    expect(unavailable?.textContent).toContain(
      "does not implement the Observability IPC surface",
    );
    expect(root.textContent).not.toContain("Loading KPIs");
  });

  it("does not route through invokeStub for missing obs IPC verbs", () => {
    expect(console.info).not.toHaveBeenCalled();
  });
});
