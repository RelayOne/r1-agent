// SPDX-License-Identifier: MIT
//
// Audit A034 — panels must not leave seeded placeholders (or hang on a
// loading row) when a host RPC rejects. A not_implemented (-32010)
// rejection renders a truthful "host verb unimplemented" state; any
// other rejection renders an error row.

import { afterEach, describe, expect, it, vi } from "vitest";

/** IpcError shape the Rust host propagates verbatim (subprocess.rs). */
const NOT_IMPLEMENTED = {
  code: -32010,
  stoke_code: "not_implemented",
  message: "verb: not implemented",
};

function makeRoot(): HTMLElement {
  const div = document.createElement("div");
  document.body.appendChild(div);
  return div;
}

async function flush(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

/**
 * Import a panel plus the ipc-stub from a fresh module registry and
 * point invokeStub at a rejecting invoke.
 */
async function importWithRejection(panelPath: string, rejection: unknown) {
  vi.resetModules();
  const stub = await import("./ipc-stub");
  stub.__setInvokeForTests(() => Promise.reject(rejection));
  const panel = (await import(/* @vite-ignore */ panelPath)) as {
    renderPanel: (root: HTMLElement) => void;
  };
  return panel;
}

afterEach(() => {
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("classifyIpcError", () => {
  it("flags -32010 / stoke_code not_implemented", async () => {
    const { classifyIpcError } = await import("./ipc-stub");
    expect(classifyIpcError(NOT_IMPLEMENTED).notImplemented).toBe(true);
    expect(
      classifyIpcError({
        code: -32601,
        message: "nope",
        data: { stoke_code: "not_implemented" },
      }).notImplemented,
    ).toBe(true);
  });

  it("keeps generic errors as failures with a message", async () => {
    const { classifyIpcError } = await import("./ipc-stub");
    const f = classifyIpcError(new Error("socket closed"));
    expect(f.notImplemented).toBe(false);
    expect(f.message).toContain("socket closed");
  });
});

describe("cost panel RPC failure handling (A034)", () => {
  it("renders a truthful unavailable state on not_implemented", async () => {
    const panel = await importWithRejection("./panels/cost-panel", NOT_IMPLEMENTED);
    const root = makeRoot();
    panel.renderPanel(root);
    await flush();

    const note = root.querySelector('[data-role="cost-unavailable"]');
    expect(note?.textContent).toContain("cost.get_current is unimplemented");
    // The seeded $0.00 summary must be gone.
    expect(root.querySelector('[data-role="cost-usd"]')).toBeNull();
  });

  it("renders an error row on other rejections", async () => {
    const panel = await importWithRejection(
      "./panels/cost-panel",
      new Error("subprocess stdin closed"),
    );
    const root = makeRoot();
    panel.renderPanel(root);
    await flush();

    const note = root.querySelector('[data-role="cost-unavailable"]');
    expect(note?.textContent).toContain("Couldn't load cost data");
    expect(note?.textContent).toContain("subprocess stdin closed");
  });
});

describe("ledger viewer RPC failure handling (A034)", () => {
  it("replaces the loading row with an unavailable row", async () => {
    const panel = await importWithRejection(
      "./panels/ledger-viewer",
      NOT_IMPLEMENTED,
    );
    const root = makeRoot();
    panel.renderPanel(root);
    await flush();

    const row = root.querySelector('[data-role="ledger-unavailable"]');
    expect(row?.textContent).toContain("ledger.list_events is unimplemented");
    expect(root.textContent).not.toContain("Loading recent events");
  });
});

describe("memory inspector RPC failure handling (A034)", () => {
  it("replaces the loading row with an unavailable row", async () => {
    const panel = await importWithRejection(
      "./panels/memory-inspector",
      NOT_IMPLEMENTED,
    );
    const root = makeRoot();
    panel.renderPanel(root);
    await flush();

    const cell = root.querySelector('[data-role="memory-unavailable"]');
    expect(cell?.textContent).toContain("memory.list_scopes is unimplemented");
  });
});

describe("descent ladder RPC failure handling (A034)", () => {
  it("surfaces an unavailable note instead of pinning seeded pending rows", async () => {
    const panel = await importWithRejection(
      "./panels/descent-ladder",
      NOT_IMPLEMENTED,
    );
    const root = makeRoot();
    root.dataset.sessionId = "S1";
    panel.renderPanel(root);
    await flush();

    const note = root.querySelector<HTMLElement>(
      '[data-role="descent-current-tier-note"]',
    );
    expect(note?.hidden).toBe(false);
    expect(note?.dataset.state).toBe("unavailable");
    expect(note?.textContent).toContain("descent.current_tier is unimplemented");
  });
});
