// SPDX-License-Identifier: MIT
//
// R1D-6 tests — memory panel truthfulness.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./ipc-stub", () => ({
  invokeStub: vi.fn(async (method: string, _phase: string, empty: unknown, args?: Record<string, unknown>) => {
    if (method === "memory_list_scopes") {
      return { scopes: ["Session", "Global"] };
    }
    if (method === "memory_query" && args?.scope === "Session") {
      return {
        entries: [
          { key: "mission.current", value: "build desktop", updated_at: "2026-05-15T12:00:00Z" },
        ],
        truncated: true,
      };
    }
    if (method === "memory_query") {
      return {
        entries: [
          { key: "global.flag", value: "on", updated_at: "2026-05-15T13:00:00Z" },
        ],
        truncated: false,
      };
    }
    return empty;
  }),
}));

import { renderPanel } from "./panels/memory-inspector";

function makeRoot(): HTMLElement {
  const div = document.createElement("div");
  document.body.appendChild(div);
  return div;
}

async function flushUi(): Promise<void> {
  await new Promise((resolve) => window.setTimeout(resolve, 0));
}

describe("R1D-6 memory panel", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = makeRoot();
    renderPanel(root);
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("renders the read-only note and removes fake mutation controls", async () => {
    await flushUi();
    const note = root.querySelector('[data-role="memory-readonly-note"]');
    expect(note?.textContent).toContain("read-only memory browsing only");
    expect(note?.textContent).toContain("History, import, delete, and write controls");
    expect(root.querySelector('[data-role="export"]')).toBeNull();
    expect(root.querySelector('[data-role="import"]')).toBeNull();
    expect(root.querySelector('[data-role="delete"]')).toBeNull();
    expect(root.textContent).not.toContain("Author");
    expect(root.textContent).not.toContain("Reads");
    expect(root.textContent).not.toContain("Writes");
  });

  it("uses host-backed scopes and query rows, and surfaces truncation truthfully", async () => {
    await flushUi();
    expect(root.textContent).toContain("mission.current");
    expect(root.textContent).toContain("build desktop");
    expect(root.querySelector('[data-role="memory-truncated"]')?.textContent).toContain(
      "partial result set for Session",
    );

    const globalTab = root.querySelector<HTMLButtonElement>('#r1-memory-tab-Global');
    globalTab?.click();
    await flushUi();

    expect(root.textContent).toContain("global.flag");
    expect(
      root.querySelector<HTMLDivElement>('[data-role="memory-truncated"]')?.hidden,
    ).toBe(true);
  });
});
