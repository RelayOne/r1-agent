// SPDX-License-Identifier: MIT
//
// R1D-3 tests — SOW tree + descent ladder + evidence drawer.
//
// AC from work-r1-desktop-app.md R1D-3:
//   - SOW tree sidebar renders with ARIA tree roles.
//   - T1..T8 descent tiers render in the ladder.
//   - Evidence drawer mounts and opens/closes.
//   - TIER_COLORS covers all 8 tiers.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderPanel as renderSowTree } from "./panels/sow-tree";
import { renderPanel as renderDescentLadder, TIER_COLORS } from "./panels/descent-ladder";
import { mountDrawer, openDrawer, closeDrawer } from "./panels/descent-evidence";
import { ALL_DESCENT_TIERS } from "./types/ipc-const";

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

function makeRoot(): HTMLElement {
  const div = document.createElement("div");
  document.body.appendChild(div);
  return div;
}

function cleanup(root: HTMLElement): void {
  root.remove();
}

// -----------------------------------------------------------------------
// R1D-3.1 — SOW tree sidebar ARIA structure
// -----------------------------------------------------------------------

describe("sow-tree — R1D-3.1 ARIA tree structure", () => {
  let root: HTMLElement;
  beforeEach(() => {
    vi.spyOn(console, "info").mockImplementation(() => {});
    root = makeRoot();
    renderSowTree(root);
  });

  it("mounts with r1-panel-sow-tree class", () => {
    expect(root.classList.contains("r1-panel-sow-tree")).toEqual(true);
  });

  it("renders a top-level <ul> with role=tree", () => {
    const tree = root.querySelector('[data-role="sow-tree"]');
    expect(tree).not.toBeNull();
    expect(tree!.getAttribute("role")).toBe("tree");
  });

  it("tree has aria-label SOW tree", () => {
    const tree = root.querySelector('[data-role="sow-tree"]');
    expect(tree!.getAttribute("aria-label")).toBe("SOW tree");
  });

  it("tree has aria-live=polite for screen reader updates", () => {
    const tree = root.querySelector('[data-role="sow-tree"]');
    expect(tree!.getAttribute("aria-live")).toBe("polite");
  });

  it("renders a loading state or empty state before data arrives", () => {
    const tree = root.querySelector('[data-role="sow-tree"]');
    // Stub returns [] so empty state renders after async resolves,
    // but synchronously at mount time the Loading item is present first.
    expect(tree!.textContent!.length).toBeGreaterThan(0);
  });

  it("panel header contains SOW Tree title", () => {
    const h2 = root.querySelector(".r1-panel-header h2");
    expect(h2).not.toBeNull();
    expect(h2!.textContent).toContain("SOW Tree");
  });

  afterEach(() => cleanup(root));
});

// -----------------------------------------------------------------------
// R1D-3.3 — Descent ladder T1..T8 grid
// -----------------------------------------------------------------------

describe("descent-ladder — R1D-3.3 T1..T8 grid", () => {
  let root: HTMLElement;
  beforeEach(() => {
    vi.spyOn(console, "info").mockImplementation(() => {});
    root = makeRoot();
    renderDescentLadder(root);
  });

  it("mounts with r1-panel-descent-ladder class", () => {
    expect(root.classList.contains("r1-panel-descent-ladder")).toEqual(true);
  });

  it("renders 8 tier rows in the ladder", () => {
    const tiers = root.querySelectorAll(".r1-descent-tier");
    expect(tiers.length).toBe(8);
  });

  it("renders T1 as first tier", () => {
    const first = root.querySelector(".r1-descent-tier");
    expect(first!.getAttribute("data-tier")).toBe("T1");
  });

  it("renders T8 as last tier", () => {
    const tiers = root.querySelectorAll(".r1-descent-tier");
    const last = tiers[tiers.length - 1];
    expect(last!.getAttribute("data-tier")).toBe("T8");
  });

  it("all tiers start with pending status", () => {
    const tiers = root.querySelectorAll<HTMLElement>(".r1-descent-tier");
    tiers.forEach((tier) => {
      expect(tier.dataset.status).toBe("pending");
    });
  });

  it("each tier has an Evidence button", () => {
    const buttons = root.querySelectorAll('[data-role="evidence"]');
    expect(buttons.length).toBe(8);
  });

  it("Evidence buttons have tier-specific aria-label", () => {
    const t3Btn = root.querySelector<HTMLButtonElement>('[data-tier="T3"][data-role="evidence"]');
    expect(t3Btn).not.toBeNull();
    expect(t3Btn!.getAttribute("aria-label")).toContain("T3");
  });

  it("panel header has Descent Ladder title", () => {
    const h2 = root.querySelector(".r1-panel-header h2");
    expect(h2!.textContent).toContain("Descent Ladder");
  });

  afterEach(() => cleanup(root));
});

// -----------------------------------------------------------------------
// TIER_COLORS — all 8 tiers have color values
// -----------------------------------------------------------------------

describe("TIER_COLORS — all 8 tiers defined", () => {
  it("exports exactly 8 tier colors", () => {
    const keys = Object.keys(TIER_COLORS);
    expect(keys.length).toBe(8);
  });

  it("each tier in ALL_DESCENT_TIERS has a color", () => {
    for (const tier of ALL_DESCENT_TIERS) {
      expect(TIER_COLORS[tier]).toBeTruthy();
    }
  });

  it("colors are hex strings starting with #", () => {
    for (const tier of ALL_DESCENT_TIERS) {
      expect(TIER_COLORS[tier]).toMatch(/^#[0-9a-fA-F]{6}$/);
    }
  });

  it("T1 is cooler (blue) and T8 is warmer (red) — cold-to-warm palette", () => {
    // T1 color starts with a blue-ish hue: the first two hex digits (red) < T8's.
    const t1R = parseInt(TIER_COLORS.T1.slice(1, 3), 16);
    const t8R = parseInt(TIER_COLORS.T8.slice(1, 3), 16);
    expect(t8R).toBeGreaterThan(t1R);
  });
});

// -----------------------------------------------------------------------
// R1D-3.4 — Evidence drawer mount + open/close
// -----------------------------------------------------------------------

describe("descent-evidence drawer — R1D-3.4 open/close", () => {
  let parent: HTMLElement;
  beforeEach(() => {
    vi.spyOn(console, "info").mockImplementation(() => {});
    parent = document.createElement("div");
    document.body.appendChild(parent);
    // Reset module-level state by ensuring the drawer doesn't already exist
    const existing = document.getElementById("r1-descent-evidence-drawer");
    existing?.remove();
    const existingBd = document.getElementById("r1-descent-evidence-backdrop");
    existingBd?.remove();
    mountDrawer(parent);
  });

  it("mounts a drawer element with role=dialog", () => {
    const drawer = document.getElementById("r1-descent-evidence-drawer");
    expect(drawer).not.toBeNull();
    expect(drawer!.getAttribute("role")).toBe("dialog");
  });

  it("drawer starts hidden", () => {
    const drawer = document.getElementById("r1-descent-evidence-drawer") as HTMLElement;
    expect(drawer.hidden).toEqual(true);
  });

  it("backdrop starts hidden", () => {
    const backdrop = document.getElementById("r1-descent-evidence-backdrop") as HTMLElement;
    expect(backdrop.hidden).toEqual(true);
  });

  it("drawer has aria-modal=true", () => {
    const drawer = document.getElementById("r1-descent-evidence-drawer");
    expect(drawer!.getAttribute("aria-modal")).toBe("true");
  });

  it("openDrawer makes drawer visible and adds is-open class", async () => {
    await openDrawer("T3", "session-abc");
    const drawer = document.getElementById("r1-descent-evidence-drawer") as HTMLElement;
    expect(drawer.hidden).toEqual(false);
    expect(drawer.classList.contains("is-open")).toEqual(true);
  });

  it("openDrawer sets tier in the title", async () => {
    await openDrawer("T5", "session-xyz");
    const title = document.getElementById("r1-descent-evidence-drawer-title");
    expect(title!.textContent).toContain("T5");
  });

  it("closeDrawer hides the drawer", async () => {
    await openDrawer("T1", "session-1");
    closeDrawer();
    const drawer = document.getElementById("r1-descent-evidence-drawer") as HTMLElement;
    expect(drawer.hidden).toEqual(true);
    expect(drawer.classList.contains("is-open")).toEqual(false);
  });

  it("closeDrawer hides the backdrop", async () => {
    await openDrawer("T2", "session-2");
    closeDrawer();
    const backdrop = document.getElementById("r1-descent-evidence-backdrop") as HTMLElement;
    expect(backdrop.hidden).toEqual(true);
  });

  it("Escape key closes the drawer", async () => {
    await openDrawer("T4", "session-4");
    const event = new KeyboardEvent("keydown", { key: "Escape", bubbles: true });
    document.dispatchEvent(event);
    const drawer = document.getElementById("r1-descent-evidence-drawer") as HTMLElement;
    expect(drawer.hidden).toEqual(true);
  });

  it("drawer close button is present", () => {
    const closeBtn = document.querySelector('[data-role="drawer-close"]');
    expect(closeBtn).not.toBeNull();
  });

  afterEach(() => {
    parent.remove();
    const drawer = document.getElementById("r1-descent-evidence-drawer");
    drawer?.remove();
    const backdrop = document.getElementById("r1-descent-evidence-backdrop");
    backdrop?.remove();
  });
});

// -----------------------------------------------------------------------
// ALL_DESCENT_TIERS constant (ipc-const)
// -----------------------------------------------------------------------

describe("ALL_DESCENT_TIERS — ipc-const contract", () => {
  it("contains exactly 8 tiers", () => {
    expect(ALL_DESCENT_TIERS.length).toBe(8);
  });

  it("starts at T1 and ends at T8", () => {
    expect(ALL_DESCENT_TIERS[0]).toBe("T1");
    expect(ALL_DESCENT_TIERS[7]).toBe("T8");
  });

  it("all entries match the T[1-8] pattern", () => {
    for (const tier of ALL_DESCENT_TIERS) {
      expect(tier).toMatch(/^T[1-8]$/);
    }
  });
});
