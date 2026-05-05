// SPDX-License-Identifier: MIT
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { PlanCard } from "@/components/chat/PlanCard";
import type { MessagePart } from "@/lib/api/types";

type PlanPart = Extract<MessagePart, { kind: "plan" }>;

const fullPlan: PlanPart = {
  kind: "plan",
  items: [
    { id: "p1", text: "scaffold project", status: "completed" },
    { id: "p2", text: "wire daemon socket", status: "completed" },
    { id: "p3", text: "build component tree", status: "in-progress" },
    { id: "p4", text: "tests + e2e", status: "pending" },
    { id: "p5", text: "tauri shell", status: "blocked" },
    { id: "p6", text: "old idea", status: "skipped" },
  ],
};

describe("<PlanCard>", () => {
  it("renders one row per item with stable plan-item-<id> testid", () => {
    render(<PlanCard part={fullPlan} />);
    for (const it of fullPlan.items) {
      expect(screen.getByTestId(`plan-item-${it.id}`)).toBeTruthy();
    }
  });

  it("counts completed items in the header progress label", () => {
    render(<PlanCard part={fullPlan} />);
    expect(screen.getByTestId("plan-card-0-progress").textContent).toBe("2 / 6");
  });

  it("surfaces blocked + in-progress chips when present", () => {
    render(<PlanCard part={fullPlan} />);
    expect(screen.getByTestId("plan-card-0-blocked-count").textContent).toContain(
      "1",
    );
    expect(
      screen.getByTestId("plan-card-0-in-progress-count").textContent,
    ).toContain("1");
  });

  it("hides the blocked chip when zero items are blocked", () => {
    render(
      <PlanCard
        part={{
          kind: "plan",
          items: [{ id: "a", text: "do thing", status: "completed" }],
        }}
      />,
    );
    expect(screen.queryByTestId("plan-card-0-blocked-count")).toBeNull();
    expect(screen.queryByTestId("plan-card-0-in-progress-count")).toBeNull();
  });

  it("renders the empty-state notice when the plan has no items", () => {
    render(<PlanCard part={{ kind: "plan", items: [] }} />);
    expect(screen.getByTestId("plan-card-0-empty")).toBeTruthy();
    expect(screen.queryByTestId("plan-card-0-items")).toBeNull();
  });

  it("namespaces the card testid by index", () => {
    render(<PlanCard part={fullPlan} index={4} />);
    expect(screen.getByTestId("plan-card-4")).toBeTruthy();
    expect(screen.getByTestId("plan-card-4-items")).toBeTruthy();
  });

  it("propagates per-item status via data-status for e2e selectors", () => {
    render(<PlanCard part={fullPlan} />);
    expect(
      screen.getByTestId("plan-item-p3").getAttribute("data-status"),
    ).toBe("in-progress");
    expect(
      screen.getByTestId("plan-item-p5").getAttribute("data-status"),
    ).toBe("blocked");
    expect(
      screen.getByTestId("plan-item-p6").getAttribute("data-status"),
    ).toBe("skipped");
  });

  it("aria-label on the card reports completion ratio", () => {
    render(<PlanCard part={fullPlan} />);
    expect(
      screen.getByTestId("plan-card-0").getAttribute("aria-label"),
    ).toBe("Plan, 2 of 6 completed");
  });
});
