// LaneSidebar test — spec desktop-cortex-augmentation §11.1 + item 31.
//
// Covers the two non-trivial guarantees the sidebar makes:
//
//   1. Stable creation-time ordering. Status changes must NOT
//      reshuffle the list (RT-SURFACES "lane ordering churn"
//      mitigation).
//   2. Diff-only repaint. When only one lane's props change the
//      sibling LaneCards must NOT re-render. We assert this by
//      counting LaneCard renders via React.memo's bypass — when a
//      memo'd component's incoming props are referentially equal
//      it returns the cached render. We verify this by spying on
//      LaneCard via vi.mock and asserting the spy is called only
//      for the changed lane on the second render.

import * as React from "react";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, cleanup } from "@testing-library/react";

// Mock the LaneCard module BEFORE importing LaneSidebar so the spy
// is in place at module-load time. The spy renders a minimal
// element so React Testing Library can find them; the spy itself
// records each render's props so we can assert per-card render
// counts.
const renderSpy = vi.fn<[unknown], void>();
vi.mock("../LaneCard", async () => {
  const React = await import("react");
  function MockLaneCard(props: { laneId: string; title: string }) {
    renderSpy(props);
    return React.createElement(
      "div",
      { "data-lane-id": props.laneId, "data-testid": "mock-lane-card" },
      props.title,
    );
  }
  return {
    LaneCard: React.memo(MockLaneCard),
  };
});

import { LaneSidebar, type LaneSidebarItem } from "../LaneSidebar";

beforeEach(() => {
  renderSpy.mockClear();
  cleanup();
});

function makeItem(
  overrides: Partial<LaneSidebarItem> & Pick<LaneSidebarItem, "laneId">,
): LaneSidebarItem {
  return {
    title: `Lane ${overrides.laneId}`,
    status: "running",
    createdAt: "2026-05-01T00:00:00Z",
    ...overrides,
  };
}

describe("LaneSidebar", () => {
  it("renders empty state when items is empty", () => {
    const { container } = render(
      <LaneSidebar items={[]} emptyMessage="No lanes" />,
    );
    expect(container.querySelector(".lane-sidebar--empty")).toBeTruthy();
    expect(container.textContent).toContain("No lanes");
  });

  it("orders lanes by createdAt ascending, regardless of insertion order", () => {
    const items: LaneSidebarItem[] = [
      makeItem({ laneId: "B", createdAt: "2026-05-02T00:00:00Z" }),
      makeItem({ laneId: "A", createdAt: "2026-05-01T00:00:00Z" }),
      makeItem({ laneId: "C", createdAt: "2026-05-03T00:00:00Z" }),
    ];
    const { container } = render(<LaneSidebar items={items} />);
    const ids = Array.from(
      container.querySelectorAll<HTMLElement>("[data-lane-id]"),
    ).map((el) => el.dataset.laneId);
    expect(ids).toEqual(["A", "B", "C"]);
  });

  it("breaks createdAt ties with lane id for determinism", () => {
    const items: LaneSidebarItem[] = [
      makeItem({ laneId: "Z", createdAt: "2026-05-01T00:00:00Z" }),
      makeItem({ laneId: "A", createdAt: "2026-05-01T00:00:00Z" }),
      makeItem({ laneId: "M", createdAt: "2026-05-01T00:00:00Z" }),
    ];
    const { container } = render(<LaneSidebar items={items} />);
    const ids = Array.from(
      container.querySelectorAll<HTMLElement>("[data-lane-id]"),
    ).map((el) => el.dataset.laneId);
    expect(ids).toEqual(["A", "M", "Z"]);
  });

  it("does NOT reshuffle when one lane's status flips (RT-SURFACES anti-churn)", () => {
    const ts = (n: number) => `2026-05-0${n}T00:00:00Z`;
    const initial: LaneSidebarItem[] = [
      makeItem({ laneId: "A", createdAt: ts(1) }),
      makeItem({ laneId: "B", createdAt: ts(2), status: "running" }),
      makeItem({ laneId: "C", createdAt: ts(3) }),
    ];
    const dom = render(<LaneSidebar items={initial} />);
    const idsOf = () =>
      Array.from(
        dom.container.querySelectorAll<HTMLElement>("[data-lane-id]"),
      ).map((el) => el.dataset.laneId);
    const before = idsOf();
    expect(before).toEqual(["A", "B", "C"]);
    // Flip B to done; sibling order MUST stay stable.
    const next = initial.map((it) =>
      it.laneId === "B" ? { ...it, status: "done" as const } : it,
    );
    dom.rerender(<LaneSidebar items={next} />);
    expect(idsOf()).toEqual(before);
  });

  it("only re-renders the changed LaneCard on diff (D-S2 perf gate)", () => {
    const items: LaneSidebarItem[] = [
      makeItem({ laneId: "A", createdAt: "2026-05-01T00:00:00Z" }),
      makeItem({ laneId: "B", createdAt: "2026-05-02T00:00:00Z" }),
      makeItem({ laneId: "C", createdAt: "2026-05-03T00:00:00Z" }),
    ];
    const { rerender } = render(<LaneSidebar items={items} />);
    expect(renderSpy).toHaveBeenCalledTimes(3);

    renderSpy.mockClear();

    // Only B changes — A and C keep the same item refs.
    const next: LaneSidebarItem[] = [
      items[0],
      { ...items[1], status: "done" },
      items[2],
    ];
    rerender(<LaneSidebar items={next} />);
    // A and C are React.memo'd; with referentially-equal props they
    // must be skipped. Only B's props changed.
    const renderedIds = renderSpy.mock.calls.map(
      (call) => (call[0] as { laneId: string }).laneId,
    );
    expect(renderedIds).toEqual(["B"]);
  });

  it("hides last-event preview in summary density", () => {
    const items: LaneSidebarItem[] = [
      makeItem({ laneId: "A", lastEventPreview: "should-not-render" }),
    ];
    render(<LaneSidebar items={items} density="summary" />);
    const previewArg = renderSpy.mock.calls[0][0] as {
      lastEventPreview?: string;
    };
    expect(previewArg.lastEventPreview).toBeUndefined();
  });

  it("passes through last-event preview in normal density", () => {
    const items: LaneSidebarItem[] = [
      makeItem({ laneId: "A", lastEventPreview: "reading file" }),
    ];
    render(<LaneSidebar items={items} density="normal" />);
    const previewArg = renderSpy.mock.calls[0][0] as {
      lastEventPreview?: string;
    };
    expect(previewArg.lastEventPreview).toBe("reading file");
  });

  it("threads selectedLaneId to each LaneCard's selected prop", () => {
    const items: LaneSidebarItem[] = [
      makeItem({ laneId: "A" }),
      makeItem({ laneId: "B" }),
    ];
    render(<LaneSidebar items={items} selectedLaneId="B" />);
    const calls = renderSpy.mock.calls.map(
      (c) => c[0] as { laneId: string; selected?: boolean },
    );
    const selectedById = new Map(calls.map((c) => [c.laneId, c.selected]));
    expect(selectedById.get("A")).toBe(false);
    expect(selectedById.get("B")).toBe(!false);
  });
});
