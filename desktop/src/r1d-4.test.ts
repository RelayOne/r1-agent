// SPDX-License-Identifier: MIT
//
// R1D-4 tests — skill catalog truthfulness.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderPanel } from "./panels/skill-catalog";

function makeRoot(): HTMLElement {
  const div = document.createElement("div");
  document.body.appendChild(div);
  return div;
}

describe("R1D-4 skill catalog", () => {
  let root: HTMLElement;

  beforeEach(() => {
    vi.spyOn(console, "info").mockImplementation(() => {});
    root = makeRoot();
    renderPanel(root);
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("renders a read-only note and removes fake mutation actions", () => {
    const note = root.querySelector('[data-role="skill-readonly-note"]');
    expect(note?.textContent).toContain("host-cached catalog and manifest drawer only");
    expect(note?.textContent).toContain("Install, uninstall, bundled-pack, and test-skill");
    expect(root.querySelector('[data-role="skill-action"]')).toBeNull();
    expect(root.textContent).not.toContain("Install Actium Pack");
    expect(root.textContent).not.toContain("Test skill");
  });

  it("still uses the real read-only host methods only", async () => {
    await vi.waitFor(() => {
      expect(console.info).toHaveBeenCalledWith(
        '[r1-desktop] TODO R1D-4: invoke("skill_list") — scaffold stub returning empty',
        expect.any(Object),
      );
    });
    expect(root.querySelector('[data-role="count-available"]')?.textContent).toBe("0");
    expect(root.querySelector('[data-role="count-installed"]')?.textContent).toBe("0");
  });
});
