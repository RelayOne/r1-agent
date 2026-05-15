// SPDX-License-Identifier: MIT
//
// R1D-8 tests — MCP panel truthfulness.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderPanel } from "./panels/mcp-servers";

function makeRoot(): HTMLElement {
  const div = document.createElement("div");
  document.body.appendChild(div);
  return div;
}

function cleanup(root: HTMLElement): void {
  root.remove();
}

describe("mcp panel — R1D-8 truthfulness", () => {
  let root: HTMLElement;

  beforeEach(() => {
    vi.spyOn(console, "info").mockImplementation(() => {});
    root = makeRoot();
    renderPanel(root);
  });

  afterEach(() => {
    cleanup(root);
  });

  it("renders the unavailable state instead of server-management placeholders", () => {
    const unavailable = root.querySelector('[data-role="mcp-unavailable"]');
    expect(unavailable).not.toBeNull();
    expect(unavailable?.textContent).toContain(
      "does not implement the MCP IPC surface",
    );
    expect(root.textContent).not.toContain("Loading servers");
    expect(root.textContent).not.toContain("Add server");
  });

  it("does not route through invokeStub for missing mcp IPC verbs", () => {
    expect(console.info).not.toHaveBeenCalled();
  });
});
