// SPDX-License-Identifier: MIT
//
// R1D-3 / R1D-9 truthfulness tests for the cost and descent panels.

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

async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

describe("cost panel — host-backed current snapshot truthfulness", () => {
  beforeEach(() => {
    vi.resetModules();
    invokeStub.mockReset();
    document.body.innerHTML = "";
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("labels the panel as a current snapshot instead of claiming all-time spend", async () => {
    invokeStub.mockResolvedValue({
      usd: 0,
      tokens_in: 0,
      tokens_out: 0,
      as_of: "",
    });
    const { renderPanel } = await import("./panels/cost-panel");
    const root = makeRoot();

    renderPanel(root);
    await flush();

    expect(root.textContent).toContain("current cost snapshot");
    expect(root.textContent).not.toContain("all-time spend");
  });

  it("passes the selected session id to cost_get_current when present", async () => {
    invokeStub.mockResolvedValue({
      usd: 12.34,
      tokens_in: 456,
      tokens_out: 789,
      as_of: "2026-05-15T18:22:00Z",
    });
    const { renderPanel } = await import("./panels/cost-panel");
    const root = makeRoot();
    root.dataset.sessionId = "session-42";

    renderPanel(root);
    await flush();

    expect(invokeStub).toHaveBeenCalledWith(
      "cost_get_current",
      "R1D-9",
      { usd: 0, tokens_in: 0, tokens_out: 0, as_of: "" },
      { session_id: "session-42" },
    );
  });
});

describe("descent ladder — host-backed current tier truthfulness", () => {
  beforeEach(() => {
    vi.resetModules();
    invokeStub.mockReset();
    document.body.innerHTML = "";
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("does not claim live current-tier status without an active session context", async () => {
    invokeStub.mockResolvedValue([]);
    const { renderPanel } = await import("./panels/descent-ladder");
    const root = makeRoot();

    renderPanel(root);
    await flush();

    expect(invokeStub).not.toHaveBeenCalled();
    expect(root.textContent).toContain("Select an active session");
  });

  it("uses descent_current_tier with the selected session and AC ids", async () => {
    invokeStub.mockResolvedValue([
      { ac_id: "ac-7", tier: "T3", status: "running" },
    ]);
    const { renderPanel } = await import("./panels/descent-ladder");
    const root = makeRoot();
    root.dataset.sessionId = "session-99";
    root.dataset.acId = "ac-7";

    renderPanel(root);
    await flush();

    expect(invokeStub).toHaveBeenCalledWith(
      "descent_current_tier",
      "R1D-3",
      [],
      { session_id: "session-99", ac_id: "ac-7" },
    );
    expect(root.querySelector('[data-tier="T3"]')?.getAttribute("data-status")).toBe("running");
  });
});

describe("descent evidence drawer — unsupported host truthfulness", () => {
  beforeEach(() => {
    vi.resetModules();
    invokeStub.mockReset();
    document.body.innerHTML = "";
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("renders an explicit unavailable state instead of routing through a missing descent_evidence verb", async () => {
    const { mountDrawer, openDrawer } = await import("./panels/descent-evidence");
    const root = makeRoot();

    mountDrawer(root);
    await openDrawer("T4", "session-abc", "ac-4");

    expect(invokeStub).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("does not implement descent evidence IPC");
    expect(document.body.textContent).not.toContain("will wire this");
  });
});
