// SPDX-License-Identifier: MIT
//
// R1D-5 tests — ledger browser truthfulness.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./ipc-stub", () => ({
  invokeStub: vi.fn(async (method: string, _phase: string, empty: unknown, args?: Record<string, unknown>) => {
    if (method === "ledger_list_events") {
      return {
        events: [
          {
            hash: "node-hash-1234567890",
            type: "session_started",
            at: "2026-05-15T12:00:00Z",
          },
        ],
      };
    }
    if (method === "ledger_get_node") {
      return {
        id: args?.hash ?? "node-hash-1234567890",
        kind: "session_started",
        timestamp: "2026-05-15T12:00:00Z",
        content_hash: args?.hash ?? "node-hash-1234567890",
        parent_hash: "",
        payload: {
          session_id: "sess-1",
          prompt: "Inspect README",
        },
        shredded: false,
      };
    }
    return empty;
  }),
}));

import { renderPanel } from "./panels/ledger-viewer";
import { mountNodeDrawer } from "./panels/ledger-node-drawer";

function makeRoot(): HTMLElement {
  const div = document.createElement("div");
  document.body.appendChild(div);
  return div;
}

async function flushUi(): Promise<void> {
  await new Promise((resolve) => window.setTimeout(resolve, 0));
}

describe("R1D-5 ledger browser", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = makeRoot();
    mountNodeDrawer(document.body);
    renderPanel(root);
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("renders the read-only note and removes fake session controls", async () => {
    await flushUi();
    const note = root.querySelector('[data-role="ledger-readonly-note"]');
    expect(note?.textContent).toContain("read-only ledger browsing only");
    expect(note?.textContent).toContain("verify-chain, export, and crypto-shred");
    expect(root.querySelector('[data-role="verify-chain"]')).toBeNull();
    expect(root.querySelector('[data-role="session-list"]')).toBeNull();
    expect(root.textContent).not.toContain("Export");
  });

  it("uses the host-backed event list and opens a read-only node drawer", async () => {
    await flushUi();
    const row = root.querySelector<HTMLLIElement>(".r1-ledger-event");
    expect(row?.textContent).toContain("session_started");

    row?.click();
    await flushUi();

    const drawer = document.getElementById("r1-ledger-node-drawer");
    expect(drawer?.hidden).toBe(false);
    expect(drawer?.textContent).toContain("Read-only node detail");
    expect(drawer?.textContent).toContain("Inspect README");
    expect(drawer?.textContent).not.toContain("Crypto-shred");
  });
});
